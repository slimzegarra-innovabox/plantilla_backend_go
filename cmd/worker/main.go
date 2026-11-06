// Comando worker es la raíz de composición de los procesos en segundo plano:
// el servidor de tareas de Asynq (REDIS_ENABLED) y el consumidor de SQS (SQS_ENABLED).
//
// No expone HTTP. Ante SIGTERM deja de tomar trabajo nuevo, espera lo que está en
// curso (WORKER_SHUTDOWN_TIMEOUT para Asynq, VisibilityTimeout por mensaje de SQS)
// y cierra la base de datos.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"golang.org/x/sync/errgroup"

	"myapp.v1.0.0/internal/shared/infrastructure/clock"
	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/external/asynq"
	"myapp.v1.0.0/internal/shared/infrastructure/external/awsconfig"
	"myapp.v1.0.0/internal/shared/infrastructure/external/brevo"
	"myapp.v1.0.0/internal/shared/infrastructure/external/s3"
	"myapp.v1.0.0/internal/shared/infrastructure/external/sqs"
	"myapp.v1.0.0/internal/shared/infrastructure/health"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
	"myapp.v1.0.0/internal/shared/infrastructure/module"
	"myapp.v1.0.0/internal/shared/infrastructure/outbox"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/telemetry"
)

// version se inyecta al compilar: -ldflags "-X main.version=<tag>".
var version = "dev"

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
		slog.Error("el worker terminó con error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if err := config.LoadDotEnv(); err != nil {
		return err
	}
	cfg, err := config.LoadWorker()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return errInvalidConfig
	}

	log := logging.New(os.Stdout, cfg.Log.Level, cfg.IsDevelop()).With("process", "worker")
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry.Setup(ctx, cfg.OTel, cfg.Env, version)
	if err != nil {
		return err
	}
	flushTelemetry := func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			log.Warn("vaciar telemetría", "error", err)
		}
	}

	//------------------------------------------------------------
	// INFRAESTRUCTURA COMPARTIDA
	//------------------------------------------------------------
	db, err := persistence.Open(ctx, cfg.DB, log, cfg.IsDevelop())
	if err != nil {
		flushTelemetry()
		return err
	}
	defer func() {
		if err := persistence.Close(db); err != nil {
			log.Error("cerrar la base de datos", "error", err)
		}
	}()
	// Declarado después para correr antes: la última recolección de métricas consulta la base.
	defer flushTelemetry()

	deps := module.Deps{
		DB:    db,
		Tx:    persistence.NewGormTransactionManager(db),
		Clock: clock.System{},
	}
	var awsCfg aws.Config
	if cfg.S3.Enabled || cfg.SQS.Enabled {
		if awsCfg, err = awsconfig.Load(ctx, cfg.AWS); err != nil {
			return err
		}
	}
	if cfg.S3.Enabled {
		deps.Storage = s3.New(awsCfg, cfg.S3)
	}
	if cfg.Brevo.Enabled {
		deps.Mail = brevo.New(cfg.Brevo)
	}
	if cfg.Redis.Enabled {
		// El worker también puede encolar (encadenar tareas).
		deps.Outbox = outbox.NewWriter(db, clock.System{})
		if err := outbox.RegisterBacklogMetrics(db, log); err != nil {
			return fmt.Errorf("métricas del outbox: %w", err)
		}
		deps.Tasks = asynq.NewPublisher(cfg.Redis)
		defer func() { _ = deps.Tasks.Close() }()
	}
	log.Info("worker iniciando", "env", cfg.Env, "version", version,
		"asynq", cfg.Redis.Enabled, "sqs", cfg.SQS.Enabled, "s3", cfg.S3.Enabled, "brevo", cfg.Brevo.Enabled, "otel", cfg.OTel.Enabled)

	//------------------------------------------------------------
	// FUENTES DE TRABAJO: viven lo mismo que el proceso
	//------------------------------------------------------------
	// Tras la primera señal, una segunda (p. ej. otro Ctrl+C) termina el proceso sin esperar.
	context.AfterFunc(ctx, stop)

	g, gctx := errgroup.WithContext(ctx)

	// Liveness: cada bucle registra un latido mientras avanza (health.Heartbeats).
	beats := health.NewHeartbeats()
	stopHealth, err := serveHealth(ctx, cfg.Queue.HealthAddr, beats, log)
	if err != nil {
		return err
	}
	defer stopHealth()

	if cfg.Redis.Enabled {
		srv := asynq.NewServer(cfg.Redis, cfg.Queue, log, beats.Register("asynq", 4*asynq.HealthCheckInterval))
		mux := asynq.NewServeMux(log)
		registerTasks(mux, deps)
		if err := srv.Start(mux); err != nil {
			return fmt.Errorf("iniciar el servidor de tareas: %w", err)
		}
		// Outbox → Asynq: publica los mensajes que los casos de uso escribieron en su
		// transacción. Varias réplicas del worker pueden correrlo a la vez (SKIP LOCKED).
		outboxCfg := outbox.DefaultConfig
		outboxCfg.Heartbeat = beats.Register("outbox", 2*time.Minute)
		dispatcher, err := outbox.NewDispatcher(db, outbox.AsynqPublisher(deps.Tasks), clock.System{}, log, outboxCfg)
		if err != nil {
			return fmt.Errorf("crear el despachador del outbox: %w", err)
		}
		g.Go(func() error { return dispatcher.Run(gctx) })

		g.Go(func() error {
			<-gctx.Done()
			log.Info("apagando el servidor de tareas", "timeout", cfg.Queue.ShutdownTimeout.String())
			srv.Shutdown() // espera las tareas en curso hasta ShutdownTimeout
			return nil
		})
	}

	if cfg.SQS.Enabled {
		sqsClient := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
			o.TracerProvider, o.MeterProvider = awsconfig.TracerProvider(), awsconfig.MeterProvider()
		})
		consumer := sqs.NewConsumer(sqsClient, cfg.SQS, sqsHandler(deps), log).
			WithHeartbeat(beats.Register("sqs", sqs.MaxLoopGap(cfg.SQS)))
		g.Go(func() error { return consumer.Run(gctx) })
	}

	err = g.Wait()
	log.Info("worker detenido")
	return err
}
