// Comando api es la raíz de composición del servidor HTTP: carga la configuración,
// crea la infraestructura compartida, registra los módulos y gestiona el apagado.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/shared/infrastructure/clock"
	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/external/asynq"
	"myapp.v1.0.0/internal/shared/infrastructure/external/awsconfig"
	"myapp.v1.0.0/internal/shared/infrastructure/external/brevo"
	"myapp.v1.0.0/internal/shared/infrastructure/external/s3"
	"myapp.v1.0.0/internal/shared/infrastructure/health"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
	"myapp.v1.0.0/internal/shared/infrastructure/outbox"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/security"
	"myapp.v1.0.0/internal/shared/infrastructure/telemetry"
)

// version se inyecta al compilar: -ldflags "-X main.version=<tag>".
var version = "dev"

// errInvalidConfig indica que la configuración ya se informó por stderr.
var errInvalidConfig = errors.New("configuración inválida")

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}

	err := run()
	switch {
	case err == nil:
	case errors.Is(err, errInvalidConfig):
		os.Exit(2)
	default:
		slog.Error("el servidor terminó con error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	//------------------------------------------------------------
	// CONFIGURACIÓN: fallo inmediato con la lista completa de problemas
	//------------------------------------------------------------
	if err := config.LoadDotEnv(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		// Antes de tener logger: texto plano y legible, con la lista completa.
		fmt.Fprintln(os.Stderr, err)
		return errInvalidConfig
	}

	log := logging.New(os.Stdout, cfg.Log.Level, cfg.IsDevelop())
	slog.SetDefault(log)
	if !cfg.IsDevelop() {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Antes que todo lo demás: la base de datos y los clientes toman los proveedores globales.
	shutdownTelemetry, err := telemetry.Setup(ctx, cfg.OTel, cfg.Env, version)
	if err != nil {
		return err
	}

	//------------------------------------------------------------
	// INFRAESTRUCTURA COMPARTIDA
	//------------------------------------------------------------
	db, err := persistence.Open(ctx, cfg.DB, log, cfg.IsDevelop())
	if err != nil {
		flushTelemetry(log, shutdownTelemetry)
		return err
	}
	defer func() {
		if err := persistence.Close(db); err != nil {
			log.Error("cerrar la base de datos", "error", err)
		}
	}()
	// Declarado después para correr antes: la última recolección de métricas consulta la base.
	defer flushTelemetry(log, shutdownTelemetry)

	engine, err := sharedhttp.NewEngine(sharedhttp.EngineOptions{
		Logger:         log,
		HSTS:           !cfg.IsDevelop(),
		TrustedProxies: cfg.HTTP.TrustedProxies,
		CORSOrigins:    cfg.HTTP.CORSOrigins,
		BodyLimitBytes: cfg.HTTP.BodyLimitBytes,
		RequestTimeout: cfg.HTTP.RequestTimeout,
		RateLimitRPS:   cfg.HTTP.RateLimitRPS,
		RateLimitBurst: cfg.HTTP.RateLimitBurst,
		ServiceName:    cfg.OTel.ServiceName,
	})
	if err != nil {
		return err
	}
	idempotencyStore := idempotency.NewPostgresStore(db, clock.System{}, idempotency.DefaultTTL, idempotency.LockFor(cfg.HTTP.RequestTimeout))
	go cleanupIdempotencyKeys(ctx, idempotencyStore, log)

	deps := module.Deps{
		DB:         db,
		Tx:         persistence.NewGormTransactionManager(db),
		Clock:      clock.System{},
		Auth:       sharedhttp.NewAuthenticator(security.NewJWTVerifier(cfg.JWT)),
		Idempotent: sharedhttp.Idempotent(idempotencyStore),
	}
	dbHealth, err := persistence.NewHealthChecker(cfg.DB)
	if err != nil {
		return err
	}
	defer func() { _ = dbHealth.Close() }()
	checks := []health.Check{{Name: "postgres", Fn: dbHealth.Ping}}

	//------------------------------------------------------------
	// INTEGRACIONES OPCIONALES (nil en Deps si están desactivadas)
	//------------------------------------------------------------
	if cfg.S3.Enabled {
		awsCfg, err := awsconfig.Load(ctx, cfg.AWS)
		if err != nil {
			return err
		}
		deps.Storage = s3.New(awsCfg, cfg.S3)
	}
	if cfg.Brevo.Enabled {
		deps.Mail = brevo.New(cfg.Brevo)
	}
	if cfg.Redis.Enabled {
		deps.Outbox = outbox.NewWriter(db, clock.System{})
		if err := outbox.RegisterBacklogMetrics(db, log); err != nil {
			return fmt.Errorf("métricas del outbox: %w", err)
		}
		deps.Tasks = asynq.NewPublisher(cfg.Redis)
		defer func() {
			if err := deps.Tasks.Close(); err != nil {
				log.Error("cerrar el publicador de tareas", "error", err)
			}
		}()
		checks = append(checks, health.Check{Name: "redis", Fn: deps.Tasks.Ping})
	}
	if !cfg.Redis.Enabled {
		log.Warn("REDIS_ENABLED=false: el outbox está desactivado y los eventos de los módulos se descartan")
	}
	log.Info("integraciones opcionales", "s3", cfg.S3.Enabled, "brevo", cfg.Brevo.Enabled, "redis", cfg.Redis.Enabled, "otel", cfg.OTel.Enabled)

	gate := &health.Gate{}
	health.Register(engine, gate, 2*time.Second, checks...)
	registerModules(engine.Group("/api/v1"), deps)

	//------------------------------------------------------------
	// SERVIDOR Y APAGADO ORDENADO
	//------------------------------------------------------------
	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("servidor escuchando", "addr", cfg.HTTP.Addr, "env", cfg.Env, "version", version)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("servidor HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	// Una segunda señal (p. ej. otro Ctrl+C) termina el proceso sin esperar.
	stop()

	// 1. Drenaje: readiness pasa a 503 y el balanceador retira la instancia, mientras
	//    el servidor sigue atendiendo lo que aún le llega. Sin keep-alive, los clientes
	//    abren conexiones nuevas, que el balanceador ya envía a otra instancia.
	gate.Drain()
	srv.SetKeepAlivesEnabled(false)
	if cfg.HTTP.DrainDelay > 0 {
		log.Info("apagando: drenando tráfico", "drain_delay", cfg.HTTP.DrainDelay.String())
		select {
		case <-time.After(cfg.HTTP.DrainDelay):
		case err := <-serveErr:
			return fmt.Errorf("servidor HTTP durante el drenaje: %w", err)
		}
	}

	// 2. Cierre: no se aceptan conexiones y se esperan las peticiones en curso.
	log.Info("apagando: se dejan de aceptar conexiones", "timeout", cfg.HTTP.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("apagado ordenado: %w", err)
	}
	log.Info("servidor detenido")
	return nil
}

// cleanupIdempotencyKeys borra las claves vencidas al arrancar y cada hora: si solo
// esperara la primera hora, un proceso que se reinicia con más frecuencia no limpiaría
// nunca. Varias réplicas pueden hacerlo a la vez: borrar lo ya borrado no tiene efecto.
func cleanupIdempotencyKeys(ctx context.Context, store *idempotency.PostgresStore, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if _, err := store.Cleanup(ctx); err != nil && ctx.Err() == nil {
			log.Warn("limpiar claves de idempotencia", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func flushTelemetry(log *slog.Logger, shutdown telemetry.Shutdown) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		log.Warn("vaciar telemetría", "error", err)
	}
}
