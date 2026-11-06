// Package asynq es la integración técnica con Asynq (tareas en segundo plano sobre Redis).
//
//   - Publisher: lo usa cmd/api (o el worker) para encolar. No implementa un puerto: el
//     módulo define su puerto (p. ej. ThumbnailTaskPublisher) y su adapter en
//     <modulo>/infrastructure/external arma el payload y delega aquí.
//   - NewServer: el servidor de tareas de cmd/worker, con logging en slog y un único
//     registro por fallo (CLAUDE.md §11), sin el payload de la tarea.
//
// Trazas (ADR 0005): Publish inyecta el contexto de traza W3C en las cabeceras de la
// tarea (no en el payload) y el ServeMux lo extrae, así la tarea es hija de la petición
// que la encoló aunque se procese en otro proceso y minutos después.
//
// Una tarea se reintenta cuando el handler devuelve error. Si el fallo es definitivo
// (payload inválido, regla de negocio), el handler devuelve un error envuelto con
// SkipRetry para no reintentar.
package asynq

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

// Colas del proyecto, con su prioridad relativa en el worker.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
)

const scopeName = "myapp.v1.0.0/internal/shared/infrastructure/external/asynq"

var queuePriorities = map[string]int{QueueCritical: 6, QueueDefault: 3, QueueLow: 1}

// SkipRetry marca un error como definitivo: errors.Join(asynq.SkipRetry, err) o
// fmt.Errorf("...: %w", asynq.SkipRetry).
var SkipRetry = asynq.SkipRetry

// RedisOpt traduce la configuración de Redis a la de Asynq.
func RedisOpt(cfg config.RedisConfig) asynq.RedisClientOpt {
	opt := asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password.Reveal(),
		DB:       cfg.DB,
	}
	if cfg.TLS {
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return opt
}

//--------------------------------------------------------------
// PUBLICADOR
//--------------------------------------------------------------

// Publisher encola tareas.
type Publisher struct {
	client *asynq.Client
}

// NewPublisher crea el publicador. Cerrar con Close al apagar.
func NewPublisher(cfg config.RedisConfig) *Publisher {
	return &Publisher{client: asynq.NewClient(RedisOpt(cfg))}
}

// Publish serializa payload como JSON y encola la tarea. Devuelve el id de la tarea.
// payload debe ser un struct de infraestructura con tags json (nunca un dto de application).
// Por defecto va a QueueDefault con 5 reintentos; opts puede cambiarlo
// (asynq.Queue, asynq.MaxRetry, asynq.TaskID para idempotencia, asynq.ProcessIn…).
func (p *Publisher) Publish(ctx context.Context, taskType string, payload any, opts ...asynq.Option) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("asynq: serializar %s: %w", taskType, err)
	}
	defaults := []asynq.Option{asynq.Queue(QueueDefault), asynq.MaxRetry(5)}
	ctx, span := otel.Tracer(scopeName).Start(ctx, "publish "+taskType, trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attribute.String("messaging.system", "asynq"), attribute.String("messaging.destination.name", taskType)))
	defer span.End()
	// Se inyecta el span del publish: el consumidor queda como hijo de esta operación.
	headers := map[string]string{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))

	info, err := p.client.EnqueueContext(ctx, asynq.NewTaskWithHeaders(taskType, raw, headers), append(defaults, opts...)...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "encolar")
		return "", fmt.Errorf("asynq: encolar %s: %w", taskType, err)
	}
	span.SetAttributes(attribute.String("messaging.message.id", info.ID))
	return info.ID, nil
}

// Ping comprueba la conexión con Redis respetando el plazo del context (para /health/ready).
func (p *Publisher) Ping(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- p.client.Ping() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("redis: %w", ctx.Err())
	}
}

// Close libera las conexiones.
func (p *Publisher) Close() error { return p.client.Close() }

//--------------------------------------------------------------
// SERVIDOR (cmd/worker)
//--------------------------------------------------------------

// HealthCheckInterval es cada cuánto Asynq comprueba Redis. Cada comprobación, con
// éxito o sin él, es un latido: prueba que el servidor sigue vivo.
const HealthCheckInterval = 15 * time.Second

// NewServer crea el servidor de tareas. Se arranca con Start(mux) y se detiene con
// Shutdown(), que espera las tareas en curso hasta cfg.ShutdownTimeout.
// heartbeat (puede ser nil) se llama en cada comprobación periódica de Asynq.
func NewServer(redis config.RedisConfig, cfg config.QueueConfig, log *slog.Logger, heartbeat func()) *asynq.Server {
	if heartbeat == nil {
		heartbeat = func() {}
	}
	return asynq.NewServer(RedisOpt(redis), asynq.Config{
		Concurrency:         cfg.Concurrency,
		Queues:              queuePriorities,
		ShutdownTimeout:     cfg.ShutdownTimeout,
		Logger:              slogAdapter{log: log.With("component", "asynq")},
		LogLevel:            asynq.InfoLevel,
		HealthCheckInterval: HealthCheckInterval,
		HealthCheckFunc: func(err error) {
			heartbeat()
			if err != nil {
				log.Warn("asynq no alcanza Redis", "error", err)
			}
		},
	})
}

// NewServeMux crea el enrutador de tareas con el middleware de observabilidad: cada
// handler recibe en el context el span de la tarea (hijo del publish) y un logger con
// task_type y task_id (logging.FromContext). Registra la métrica
// messaging.process.duration por tipo de tarea y resultado.
func NewServeMux(log *slog.Logger) *asynq.ServeMux {
	tracer := otel.Tracer(scopeName)
	duration, _ := otel.Meter(scopeName).Float64Histogram("messaging.process.duration", //nolint:errcheck // nunca falla con un nombre válido
		metric.WithUnit("s"), metric.WithDescription("Duración del procesamiento de una tarea."))
	mux := asynq.NewServeMux()
	mux.Use(func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
			id, _ := asynq.GetTaskID(ctx)
			ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(t.Headers()))
			ctx, span := tracer.Start(ctx, "process "+t.Type(), trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(attribute.String("messaging.system", "asynq"),
					attribute.String("messaging.destination.name", t.Type()), attribute.String("messaging.message.id", id)))
			defer span.End()

			l := log.With("task_type", t.Type(), "task_id", id)
			ctx = logging.WithLogger(ctx, l)
			start := time.Now()
			err := next.ProcessTask(ctx, t)
			outcome := "ok"
			if err != nil {
				outcome = "error"
				span.RecordError(err)
				span.SetStatus(codes.Error, "la tarea falló")
				// Único registro del fallo: tipo, id e intento, con trace_id; nunca el payload.
				retried, _ := asynq.GetRetryCount(ctx)
				maxRetry, _ := asynq.GetMaxRetry(ctx)
				l.ErrorContext(ctx, "la tarea falló", "retry", retried, "max_retry", maxRetry, "error", err)
			} else {
				l.InfoContext(ctx, "tarea procesada", "duration_ms", time.Since(start).Milliseconds())
			}
			duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
				attribute.String("messaging.destination.name", t.Type()), attribute.String("outcome", outcome)))
			return err
		})
	})
	return mux
}

// DecodePayload deserializa el payload de una tarea. Un payload inválido no se
// arregla reintentando: el error ya viene marcado con SkipRetry.
func DecodePayload(t *asynq.Task, dst any) error {
	if err := json.Unmarshal(t.Payload(), dst); err != nil {
		return fmt.Errorf("payload de %s inválido: %w: %w", t.Type(), err, asynq.SkipRetry)
	}
	return nil
}

// slogAdapter adapta slog a la interfaz Logger de Asynq.
type slogAdapter struct{ log *slog.Logger }

func (a slogAdapter) Debug(args ...any) { a.log.Debug(fmt.Sprint(args...)) }
func (a slogAdapter) Info(args ...any)  { a.log.Info(fmt.Sprint(args...)) }
func (a slogAdapter) Warn(args ...any)  { a.log.Warn(fmt.Sprint(args...)) }
func (a slogAdapter) Error(args ...any) { a.log.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Fatal(args ...any) { a.log.Error(fmt.Sprint(args...)) }
