package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	sharedport "myapp.v1.0.0/internal/shared/application/port"
)

const scopeName = "myapp.v1.0.0/internal/shared/infrastructure/outbox"

// backlogQueryTimeout acota la lectura del atraso en cada recolección de métricas.
const backlogQueryTimeout = 5 * time.Second

// cleanupBatch es cuántos publicados se borran de una vez, para no bloquear.
const cleanupBatch = 10000

// pingTimeout acota la comprobación del destino tras una publicación fallida.
const pingTimeout = 2 * time.Second

// ErrDestinationUnavailable indica que la ronda se cortó porque el destino está caído.
// Los mensajes siguen pendientes y sin gastar intentos.
var ErrDestinationUnavailable = errors.New("outbox: destino no disponible")

// Publisher entrega un mensaje a su destino (Asynq, SQS…). Debe ser idempotente por
// Message.ID cuando el destino lo permite: un mensaje puede publicarse más de una vez.
type Publisher interface {
	Publish(ctx context.Context, m Message) error
	// Ping dice si el destino responde. Tras un Publish fallido separa una caída, que no
	// gasta intentos, de un fallo de ese mensaje, que sí. Si el destino no lo permite
	// comprobar, devuelve nil.
	Ping(ctx context.Context) error
}

// DispatcherConfig ajusta el despachador. Los valores cero toman los de DefaultConfig.
type DispatcherConfig struct {
	// Poll es la espera entre rondas cuando no hay pendientes.
	Poll time.Duration
	// Batch es cuántos mensajes se toman por ronda.
	Batch int
	// Lease es cuánto tiempo un mensaje tomado queda reservado: si el despachador cae
	// antes de marcarlo, otro lo retoma al vencer. Debe superar PublishTimeout.
	Lease time.Duration
	// PublishTimeout acota cada publicación.
	PublishTimeout time.Duration
	// MaxAttempts: tras este número de intentos fallidos con el destino disponible, el
	// mensaje queda «muerto». Una caída del destino no cuenta.
	MaxAttempts int
	// UnavailableDelay es la espera hasta la siguiente ronda con el destino caído.
	UnavailableDelay time.Duration
	// Retention es cuánto se conservan los mensajes publicados antes de borrarlos.
	Retention time.Duration
	// Heartbeat, si no es nil, se llama en cada ronda y en cada mensaje publicado:
	// es la liveness del despachador (health.Heartbeats).
	Heartbeat func()
}

// DefaultConfig es la configuración por defecto.
var DefaultConfig = DispatcherConfig{
	Poll: time.Second, Batch: 100, Lease: 30 * time.Second, PublishTimeout: 10 * time.Second,
	MaxAttempts: 20, UnavailableDelay: 30 * time.Second, Retention: 7 * 24 * time.Hour,
}

// Dispatcher publica los mensajes pendientes del outbox. Varias réplicas pueden correr
// a la vez: cada mensaje lo toma una sola (FOR UPDATE SKIP LOCKED + lease).
type Dispatcher struct {
	db        *gorm.DB
	publisher Publisher
	clock     sharedport.Clock
	log       *slog.Logger
	cfg       DispatcherConfig
	results   metric.Int64Counter
}

// NewDispatcher crea el despachador.
func NewDispatcher(db *gorm.DB, publisher Publisher, clock sharedport.Clock, log *slog.Logger, cfg DispatcherConfig) (*Dispatcher, error) {
	cfg = withDefaults(cfg)
	if cfg.Lease <= cfg.PublishTimeout {
		return nil, errors.New("outbox: Lease debe superar PublishTimeout")
	}
	d := &Dispatcher{db: db, publisher: publisher, clock: clock, log: log.With("component", "outbox"), cfg: cfg}
	meter := otel.Meter(scopeName)
	var err error
	if d.results, err = meter.Int64Counter("outbox.publish.count", metric.WithDescription("Publicaciones por resultado (published | retry | dead | unavailable).")); err != nil {
		return nil, err
	}
	return d, nil
}

func withDefaults(c DispatcherConfig) DispatcherConfig {
	d := DefaultConfig
	if c.Poll > 0 {
		d.Poll = c.Poll
	}
	if c.Batch > 0 {
		d.Batch = c.Batch
	}
	if c.Lease > 0 {
		d.Lease = c.Lease
	}
	if c.PublishTimeout > 0 {
		d.PublishTimeout = c.PublishTimeout
	}
	if c.MaxAttempts > 0 {
		d.MaxAttempts = c.MaxAttempts
	}
	if c.UnavailableDelay > 0 {
		d.UnavailableDelay = c.UnavailableDelay
	}
	if c.Retention > 0 {
		d.Retention = c.Retention
	}
	d.Heartbeat = c.Heartbeat
	if d.Heartbeat == nil {
		d.Heartbeat = func() {}
	}
	return d
}

// Run despacha hasta que ctx se cancela. Al apagar termina la publicación en curso y
// devuelve el resto de la ronda (DispatchOnce).
func (d *Dispatcher) Run(ctx context.Context) error {
	d.log.Info("despachador del outbox iniciado")
	lastCleanup, moreToClean := time.Time{}, false
	for {
		if ctx.Err() != nil {
			d.log.Info("despachador del outbox detenido")
			return nil
		}
		d.cfg.Heartbeat()
		n, err := d.DispatchOnce(ctx)
		wait := d.cfg.Poll
		switch {
		case errors.Is(err, ErrDestinationUnavailable):
			d.log.Warn("destino del outbox caído; los mensajes siguen pendientes", "error", err, "retry_in", d.cfg.UnavailableDelay.String())
			wait = d.cfg.UnavailableDelay
		case err != nil:
			d.log.Error("ronda del outbox", "error", err)
		}
		// Una tanda por vuelta, para no frenar la publicación. Si quedó historial por
		// borrar, se sigue en la vuelta siguiente sin esperar la hora.
		if now := d.clock.Now(); moreToClean || now.Sub(lastCleanup) > time.Hour {
			deleted, err := d.cleanup(ctx, now)
			if err != nil && ctx.Err() == nil {
				d.log.Warn("limpieza del outbox", "error", err)
			}
			moreToClean = err == nil && deleted == cleanupBatch
			lastCleanup = now
		}
		if n == d.cfg.Batch && err == nil {
			continue // hay más pendientes: sin esperar
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// claimed es lo que devuelve la reserva.
type claimed struct {
	ID       int64
	UUID     uuid.UUID
	Topic    string
	Payload  json.RawMessage
	Headers  json.RawMessage
	Attempts int
}

// DispatchOnce toma una ronda de pendientes, los publica y devuelve cuántos tomó.
//
// El lease se reserva una vez para toda la tanda, pero se publica en serie. Por eso no se
// empieza una publicación que podría terminar después de vencer el lease: otra réplica
// retomaría el mensaje. Lo no publicado se devuelve sin gastar intentos. Lo mismo ocurre
// si ctx se cancela: lo ya empezado termina, y las escrituras no dependen de ctx.
//
// Devuelve ErrDestinationUnavailable si el destino está caído.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if ctx.Err() != nil {
		return 0, nil
	}
	work := context.WithoutCancel(ctx)
	now := d.clock.Now()
	lastStart := now.Add(d.cfg.Lease - d.cfg.PublishTimeout)
	var batch []claimed
	// Una sola sentencia: reserva (lease + intento) y devuelve las filas. SKIP LOCKED
	// hace que dos despachadores nunca tomen la misma fila.
	err := d.db.WithContext(work).Raw(`
		UPDATE outbox SET available_at = ?, attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM outbox
			WHERE published_at IS NULL AND dead_at IS NULL AND available_at <= ?
			ORDER BY available_at, id
			LIMIT ?
			FOR UPDATE SKIP LOCKED)
		RETURNING id, uuid, topic, payload, headers, attempts`,
		now.Add(d.cfg.Lease), now, d.cfg.Batch).Scan(&batch).Error
	if err != nil {
		return 0, fmt.Errorf("reservar mensajes: %w", err)
	}
	for i, m := range batch {
		if ctx.Err() != nil || !d.clock.Now().Before(lastStart) {
			d.release(work, batch[i:])
			return len(batch), nil
		}
		err := d.publish(work, m)
		d.cfg.Heartbeat()
		if err != nil {
			d.release(work, batch[i+1:])
			return len(batch), err
		}
	}
	return len(batch), nil
}

// release devuelve mensajes tomados y no publicados: quedan disponibles y sin gastar el
// intento. Si falla, se retoman al vencer el lease.
func (d *Dispatcher) release(ctx context.Context, msgs []claimed) {
	if len(msgs) == 0 {
		return
	}
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	d.mark(ctx, d.log, `UPDATE outbox SET available_at = ?, attempts = attempts - 1 WHERE id IN ?`, d.clock.Now(), ids)
}

// publish entrega un mensaje y registra el resultado. Solo devuelve un error, siempre
// ErrDestinationUnavailable, cuando el destino está caído.
func (d *Dispatcher) publish(parent context.Context, c claimed) error {
	var headers map[string]string
	_ = json.Unmarshal(c.Headers, &headers) // sin cabeceras válidas, la traza empieza aquí
	ctx := otel.GetTextMapPropagator().Extract(parent, propagation.MapCarrier(headers))
	ctx, span := otel.Tracer(scopeName).Start(ctx, "outbox publish "+c.Topic, trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attribute.String("messaging.destination.name", c.Topic),
			attribute.String("messaging.message.id", c.UUID.String()), attribute.Int("outbox.attempt", c.Attempts)))
	defer span.End()
	log := d.log.With("message_id", c.UUID.String(), "topic", c.Topic, "attempt", c.Attempts)

	pubCtx, cancel := context.WithTimeout(ctx, d.cfg.PublishTimeout)
	err := d.publisher.Publish(pubCtx, Message{ID: c.UUID, Topic: c.Topic, Payload: c.Payload, Headers: headers})
	cancel()

	now := d.clock.Now()
	if err == nil {
		if err := d.db.WithContext(ctx).Exec(`UPDATE outbox SET published_at = ?, last_error = NULL WHERE id = ?`, now, c.ID).Error; err != nil {
			// Se republicará al vencer el lease: al menos una vez.
			log.ErrorContext(ctx, "marcar mensaje publicado", "error", err)
		}
		d.results.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "published"), attribute.String("topic", c.Topic)))
		return nil
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, "publicar")
	lastErr := truncate(err.Error(), 1000)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	pingErr := d.publisher.Ping(pingCtx)
	cancel()
	if pingErr != nil {
		d.mark(ctx, log, `UPDATE outbox SET available_at = ?, attempts = attempts - 1, last_error = ? WHERE id = ?`, now, lastErr, c.ID)
		d.results.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "unavailable"), attribute.String("topic", c.Topic)))
		return fmt.Errorf("%w: %w", ErrDestinationUnavailable, pingErr)
	}

	if c.Attempts >= d.cfg.MaxAttempts {
		// Único registro del fallo definitivo: requiere intervención (docs/outbox.md).
		log.ErrorContext(ctx, "mensaje del outbox agotó sus intentos", "error", err)
		d.mark(ctx, log, `UPDATE outbox SET dead_at = ?, last_error = ? WHERE id = ?`, now, lastErr, c.ID)
		d.results.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "dead"), attribute.String("topic", c.Topic)))
		return nil
	}
	retryAt := now.Add(backoff(c.Attempts))
	log.WarnContext(ctx, "publicar mensaje del outbox (se reintentará)", "error", err, "retry_at", retryAt)
	d.mark(ctx, log, `UPDATE outbox SET available_at = ?, last_error = ? WHERE id = ?`, retryAt, lastErr, c.ID)
	d.results.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "retry"), attribute.String("topic", c.Topic)))
	return nil
}

func (d *Dispatcher) mark(ctx context.Context, log *slog.Logger, sql string, args ...any) {
	if err := d.db.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		log.ErrorContext(ctx, "actualizar mensaje del outbox", "error", err)
	}
}

// backoff exponencial: 1s, 2s, 4s… hasta 5 minutos.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := time.Second << min(attempt-1, 20)
	return min(d, 5*time.Minute)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// cleanup borra una tanda de publicados más antiguos que Retention y devuelve cuántos borró.
func (d *Dispatcher) cleanup(ctx context.Context, now time.Time) (int64, error) {
	res := d.db.WithContext(ctx).Exec(`
		DELETE FROM outbox WHERE id IN (
			SELECT id FROM outbox WHERE published_at IS NOT NULL AND published_at < ?
			LIMIT ? FOR UPDATE SKIP LOCKED)`,
		now.Add(-d.cfg.Retention), cleanupBatch)
	return res.RowsAffected, res.Error
}

// RegisterBacklogMetrics publica el atraso del outbox: la alarma clave es la antigüedad
// del pendiente más viejo (si crece, el despachador no está publicando).
//
// La registra también cmd/api, no solo el worker: si el worker muere, debe seguir
// habiendo quien informe que los mensajes se acumulan. Varias réplicas informan el
// mismo valor, leído de la base.
//
// Si la consulta falla, se registra y no se observa nada: un error devuelto aquí haría
// que el SDK descartara la exportación de TODAS las métricas del proceso, justo cuando
// la base está caída y más se necesitan. La consulta tiene su propio plazo porque
// comparte con la exportación el de la recolección. Sin datos, la alarma de estas
// métricas los trata como incumplimiento (docs/outbox.md).
//
// La consulta corre en cada recolección y en cada réplica: usa solo índices parciales
// (idx_outbox_pending, idx_outbox_dead), para no recorrer el historial de publicados.
func RegisterBacklogMetrics(db *gorm.DB, log *slog.Logger) error {
	meter := otel.Meter(scopeName)
	pending, err := meter.Int64ObservableGauge("outbox.pending", metric.WithDescription("Mensajes pendientes de publicar."))
	if err != nil {
		return err
	}
	oldest, err := meter.Float64ObservableGauge("outbox.oldest_pending_age", metric.WithUnit("s"),
		metric.WithDescription("Antigüedad del mensaje pendiente más viejo."))
	if err != nil {
		return err
	}
	dead, err := meter.Int64ObservableGauge("outbox.dead", metric.WithDescription("Mensajes que agotaron sus intentos."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		var s struct {
			Pending int64
			Oldest  float64
			Dead    int64
		}
		queryCtx, cancel := context.WithTimeout(ctx, backlogQueryTimeout)
		defer cancel()
		err := db.WithContext(queryCtx).Raw(`
			SELECT p.pending, p.oldest, (SELECT count(*) FROM outbox WHERE dead_at IS NOT NULL) AS dead
			FROM (SELECT count(*) AS pending, COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0) AS oldest
			      FROM outbox WHERE published_at IS NULL AND dead_at IS NULL) p`).Scan(&s).Error
		if err != nil {
			log.WarnContext(ctx, "leer el atraso del outbox", "component", "outbox", "error", err)
			return nil
		}
		o.ObserveInt64(pending, s.Pending)
		o.ObserveFloat64(oldest, s.Oldest)
		o.ObserveInt64(dead, s.Dead)
		return nil
	}, pending, oldest, dead)
	return err
}
