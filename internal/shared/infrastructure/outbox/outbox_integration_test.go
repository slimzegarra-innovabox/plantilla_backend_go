//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"myapp.v1.0.0/internal/shared/infrastructure/outbox"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// clock controlable desde la prueba.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

// recorder es un Publisher que registra lo que recibe y puede fallar.
type recorder struct {
	mu     sync.Mutex
	got    []outbox.Message
	traces []trace.TraceID
	fail   error
	// ping es lo que devuelve Ping.
	ping error
	// calls cuenta las llamadas a Publish, y before corre antes de cada una.
	calls  int
	before func()
}

func (r *recorder) Ping(context.Context) error { r.mu.Lock(); defer r.mu.Unlock(); return r.ping }

func (r *recorder) set(fail, ping error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail, r.ping = fail, ping
}

func (r *recorder) callCount() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

func (r *recorder) Publish(ctx context.Context, m outbox.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.before != nil {
		r.before()
	}
	if r.fail != nil {
		return r.fail
	}
	r.got = append(r.got, m)
	r.traces = append(r.traces, trace.SpanContextFromContext(ctx).TraceID())
	return nil
}

func (r *recorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) }

type event struct {
	ExampleUUID string `json:"example_uuid"`
}

func setup(t *testing.T) (*outbox.Writer, *clock, *persistence.GormTransactionManager) {
	t.Helper()
	pgtest.Truncate(t, "outbox")
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	return outbox.NewWriter(pgtest.DB(t), c), c, persistence.NewGormTransactionManager(pgtest.DB(t))
}

func write(ctx context.Context, t *testing.T, w *outbox.Writer, tm *persistence.GormTransactionManager, n int) {
	t.Helper()
	require.NoError(t, tm.WithinTransaction(ctx, func(ctx context.Context) error {
		for range n {
			if _, err := w.Write(ctx, "example:status_changed", event{ExampleUUID: uuid.NewString()}); err != nil {
				return err
			}
		}
		return nil
	}))
}

func newDispatcher(t *testing.T, p outbox.Publisher, c interface{ Now() time.Time }, cfg outbox.DispatcherConfig) *outbox.Dispatcher {
	t.Helper()
	d, err := outbox.NewDispatcher(pgtest.DB(t), p, c, slog.New(slog.DiscardHandler), cfg)
	require.NoError(t, err)
	return d
}

func pending(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pgtest.DB(t).Raw("SELECT count(*) FROM outbox WHERE published_at IS NULL AND dead_at IS NULL").Scan(&n).Error)
	return n
}

func TestWriteIsBoundToTheTransaction(t *testing.T) {
	w, _, tm := setup(t)

	_, err := w.Write(context.Background(), "x", event{})
	require.ErrorIs(t, err, outbox.ErrNoTransaction, "fuera de una transacción no se escribe")

	boom := errors.New("el caso de uso falló después de escribir")
	err = tm.WithinTransaction(context.Background(), func(ctx context.Context) error {
		_, err := w.Write(ctx, "example:status_changed", event{ExampleUUID: "a"})
		require.NoError(t, err)
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Zero(t, pending(t), "rollback: el mensaje se descarta junto con el cambio")

	write(context.Background(), t, w, tm, 1)
	assert.EqualValues(t, 1, pending(t))
}

func TestDispatchPublishesOnceAndPropagatesTrace(t *testing.T) {
	w, c, tm := setup(t)
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })

	reqCtx, span := otel.Tracer("test").Start(context.Background(), "petición")
	write(reqCtx, t, w, tm, 3)
	span.End()

	rec := &recorder{}
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{})
	n, err := d.DispatchOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, 3, rec.count())
	assert.JSONEq(t, `{"example_uuid":"`+jsonField(t, rec.got[0].Payload)+`"}`, string(rec.got[0].Payload))
	for _, tr := range rec.traces {
		assert.Equal(t, span.SpanContext().TraceID(), tr, "la publicación sigue la traza de la petición que escribió el mensaje")
	}

	n, err = d.DispatchOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "lo publicado no se vuelve a tomar")
	assert.Zero(t, pending(t))
}

func TestFailedPublishRetriesWithBackoffUntilDead(t *testing.T) {
	w, c, tm := setup(t)
	write(context.Background(), t, w, tm, 1)
	rec := &recorder{fail: errors.New("redis no disponible")}
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{MaxAttempts: 3})

	n, _ := d.DispatchOnce(context.Background())
	assert.Equal(t, 1, n)
	n, _ = d.DispatchOnce(context.Background())
	assert.Zero(t, n, "tras un fallo espera su backoff (1s)")

	c.Add(1500 * time.Millisecond)
	n, _ = d.DispatchOnce(context.Background())
	assert.Equal(t, 1, n, "segundo intento")
	c.Add(3 * time.Second)
	n, _ = d.DispatchOnce(context.Background())
	assert.Equal(t, 1, n, "tercer intento: agota MaxAttempts")

	c.Add(time.Hour)
	n, _ = d.DispatchOnce(context.Background())
	assert.Zero(t, n, "un mensaje muerto no se reintenta")
	var row struct {
		Attempts  int
		LastError string
		Dead      bool
	}
	require.NoError(t, pgtest.DB(t).Raw("SELECT attempts, last_error, dead_at IS NOT NULL AS dead FROM outbox").Scan(&row).Error)
	assert.Equal(t, 3, row.Attempts)
	assert.Equal(t, "redis no disponible", row.LastError)
	assert.True(t, row.Dead)
}

func TestConcurrentDispatchersNeverPublishTwice(t *testing.T) {
	w, c, tm := setup(t)
	write(context.Background(), t, w, tm, 60)
	rec := &recorder{}

	var wg sync.WaitGroup
	for range 4 {
		d := newDispatcher(t, rec, c, outbox.DispatcherConfig{Batch: 7})
		wg.Go(func() {
			for {
				n, err := d.DispatchOnce(context.Background())
				if err != nil || n == 0 {
					return
				}
			}
		})
	}
	wg.Wait()

	seen := map[uuid.UUID]int{}
	for _, m := range rec.got {
		seen[m.ID]++
	}
	assert.Len(t, seen, 60, "todos publicados")
	for id, times := range seen {
		assert.Equal(t, 1, times, "mensaje %s publicado %d veces", id, times)
	}
}

func TestExpiredLeaseIsRetaken(t *testing.T) {
	w, c, tm := setup(t)
	write(context.Background(), t, w, tm, 1)
	// Simula un despachador que tomó el mensaje y cayó antes de marcarlo.
	require.NoError(t, pgtest.DB(t).Exec("UPDATE outbox SET available_at = ?, attempts = 1", c.Now().Add(30*time.Second)).Error)
	rec := &recorder{}
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{})

	n, _ := d.DispatchOnce(context.Background())
	assert.Zero(t, n, "dentro del lease nadie más lo toma")
	c.Add(31 * time.Second)
	n, _ = d.DispatchOnce(context.Background())
	assert.Equal(t, 1, n, "vencido el lease, otro despachador lo retoma")
}

// attemptsOf devuelve los intentos de cada mensaje pendiente.
func attemptsOf(t *testing.T) []int {
	t.Helper()
	var out []int
	require.NoError(t, pgtest.DB(t).Raw(`SELECT attempts FROM outbox WHERE published_at IS NULL ORDER BY id`).Scan(&out).Error)
	return out
}

// La ronda no empieza una publicación que podría terminar después del lease, y lo que
// no alcanzó a publicar queda disponible, sin gastar el intento.
func TestRoundStopsBeforeLeaseExpires(t *testing.T) {
	ctx := context.Background()
	w, c, tm := setup(t)
	write(ctx, t, w, tm, 5)
	// Lease 30 s y plazo 10 s: se puede empezar hasta el segundo 20. Cada publicación tarda 8 s.
	rec := &recorder{before: func() { c.Add(8 * time.Second) }}
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{Lease: 30 * time.Second, PublishTimeout: 10 * time.Second})

	n, err := d.DispatchOnce(ctx)

	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, 3, rec.count(), "empieza en los segundos 0, 8 y 16")
	assert.Equal(t, []int{0, 0}, attemptsOf(t), "lo devuelto no gasta intentos")

	rec.before = nil
	_, err = d.DispatchOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, rec.count(), "lo devuelto se toma enseguida, sin esperar el lease")
	assert.Zero(t, pending(t))
}

// Con publicaciones lentas y dos réplicas, ninguna retoma lo que la otra no publicó aún.
func TestSlowPublishingNeverPublishesTwice(t *testing.T) {
	ctx := context.Background()
	w, _, tm := setup(t)
	write(ctx, t, w, tm, 10)
	rec := &recorder{}
	cfg := outbox.DispatcherConfig{Batch: 10, Lease: 500 * time.Millisecond, PublishTimeout: 200 * time.Millisecond}
	slow := slowPublisher{rec, 100 * time.Millisecond}

	var wg sync.WaitGroup
	for range 2 {
		d := newDispatcher(t, slow, clockwork{}, cfg)
		wg.Go(func() {
			for pending(t) > 0 {
				_, err := d.DispatchOnce(ctx)
				assert.NoError(t, err)
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
	wg.Wait()

	seen := map[uuid.UUID]int{}
	for _, m := range rec.got {
		seen[m.ID]++
	}
	assert.Len(t, seen, 10)
	for id, times := range seen {
		assert.Equal(t, 1, times, "mensaje %s", id)
	}
	var attempts []int
	require.NoError(t, pgtest.DB(t).Raw(`SELECT attempts FROM outbox`).Scan(&attempts).Error)
	for _, a := range attempts {
		assert.Equal(t, 1, a, "publicar a la primera cuenta un solo intento")
	}
}

// slowPublisher tarda en cada publicación, sin bloquear al recorder.
type slowPublisher struct {
	*recorder
	delay time.Duration
}

func (s slowPublisher) Publish(ctx context.Context, m outbox.Message) error {
	time.Sleep(s.delay)
	return s.recorder.Publish(ctx, m)
}

// clockwork es el reloj real, para las pruebas que dependen de tiempos reales.
type clockwork struct{}

func (clockwork) Now() time.Time { return time.Now() }

// Al apagar termina lo que está publicando y devuelve el resto.
func TestShutdownStopsTheRound(t *testing.T) {
	bg := context.Background()
	w, c, tm := setup(t)
	write(bg, t, w, tm, 5)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	rec := &recorder{before: cancel}
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{})

	_, err := d.DispatchOnce(ctx)

	require.NoError(t, err)
	assert.Equal(t, 1, rec.count(), "lo empezado termina y se marca, aunque ctx se haya cancelado")
	assert.Equal(t, []int{0, 0, 0, 0}, attemptsOf(t))
}

// Una caída del destino, por larga que sea, no gasta intentos ni mata mensajes.
func TestOutageDoesNotSpendAttempts(t *testing.T) {
	ctx := context.Background()
	w, c, tm := setup(t)
	write(ctx, t, w, tm, 3)
	down := errors.New("redis caído")
	rec := &recorder{}
	rec.set(down, down)
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{MaxAttempts: 2})

	for range 10 {
		_, err := d.DispatchOnce(ctx)
		require.ErrorIs(t, err, outbox.ErrDestinationUnavailable)
		c.Add(time.Minute)
	}

	assert.Equal(t, 10, rec.callCount(), "cada ronda se corta en el primer fallo: no insiste con toda la tanda")
	assert.Equal(t, []int{0, 0, 0}, attemptsOf(t))
	var dead int64
	require.NoError(t, pgtest.DB(t).Raw(`SELECT count(*) FROM outbox WHERE dead_at IS NOT NULL`).Scan(&dead).Error)
	assert.Zero(t, dead)

	rec.set(nil, nil)
	_, err := d.DispatchOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, rec.count(), "al volver el destino, sale todo")
	assert.Zero(t, pending(t))
}

// Con el destino caído, Run espera UnavailableDelay, no el Poll.
func TestRunPausesWhileDestinationIsDown(t *testing.T) {
	bg := context.Background()
	w, c, tm := setup(t)
	write(bg, t, w, tm, 1)
	down := errors.New("redis caído")
	rec := &recorder{}
	rec.set(down, down)
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{Poll: time.Millisecond, UnavailableDelay: time.Hour})

	ctx, cancel := context.WithTimeout(bg, 200*time.Millisecond)
	defer cancel()
	require.NoError(t, d.Run(ctx))

	assert.Equal(t, 1, rec.callCount())
}

// La limpieza sigue el ritmo de escritura: lo que no cabe en una tanda se borra en las
// vueltas siguientes, sin esperar a la próxima hora.
func TestCleanupCatchesUpWithoutWaitingAnHour(t *testing.T) {
	_, c, _ := setup(t)
	old := c.Now().Add(-8 * 24 * time.Hour)
	require.NoError(t, pgtest.DB(t).Exec(`INSERT INTO outbox (uuid, topic, payload, created_at, available_at, published_at)
		SELECT gen_random_uuid(), 'viejo', '{}', ?, ?, ? FROM generate_series(1, 25000)`, old, old, old).Error)
	d := newDispatcher(t, &recorder{}, c, outbox.DispatcherConfig{Poll: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- d.Run(ctx) }()

	assert.Eventually(t, func() bool {
		var n int64
		require.NoError(t, pgtest.DB(t).Raw("SELECT count(*) FROM outbox").Scan(&n).Error)
		return n == 0
	}, 10*time.Second, 20*time.Millisecond, "el reloj no avanza: no hace falta que pase la hora")
	cancel()
	require.NoError(t, <-stopped)
}

func TestBacklogMetricsReadPendingOldestAndDead(t *testing.T) {
	setup(t)
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(prev); _ = provider.Shutdown(context.Background()) })
	require.NoError(t, outbox.RegisterBacklogMetrics(pgtest.DB(t), slog.New(slog.DiscardHandler)))

	insert := func(n int, extra string) {
		t.Helper()
		for range n {
			require.NoError(t, pgtest.DB(t).Exec(`INSERT INTO outbox (uuid, topic, payload, created_at, available_at, published_at, dead_at)
				VALUES (?, 't', '{}', now() - interval '1 hour', now(), `+extra+`)`, uuid.New()).Error)
		}
	}
	insert(2, "NULL, NULL")  // pendientes
	insert(3, "NULL, now()") // muertos
	insert(4, "now(), NULL") // publicados: no cuentan

	var got metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &got))
	values := map[string]float64{}
	for _, sm := range got.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				values[m.Name] = float64(data.DataPoints[0].Value)
			case metricdata.Gauge[float64]:
				values[m.Name] = data.DataPoints[0].Value
			}
		}
	}
	assert.EqualValues(t, 2, values["outbox.pending"])
	assert.EqualValues(t, 3, values["outbox.dead"])
	assert.InDelta(t, 3600, values["outbox.oldest_pending_age"], 60)
}

func jsonField(t *testing.T, raw []byte) string {
	t.Helper()
	var e event
	require.NoError(t, json.Unmarshal(raw, &e))
	return e.ExampleUUID
}

// El bucle Run publica lo pendiente, limpia lo publicado antiguo, late y se detiene al
// cancelar el context.
func TestRunPublishesCleansUpAndStops(t *testing.T) {
	w, c, tm := setup(t)
	write(context.Background(), t, w, tm, 2)
	// Un publicado de hace 8 días (se borra) y uno de hace 1 día (se conserva).
	for _, age := range []time.Duration{8 * 24 * time.Hour, 24 * time.Hour} {
		require.NoError(t, pgtest.DB(t).Exec(`INSERT INTO outbox (uuid, topic, payload, created_at, available_at, published_at)
			VALUES (?, 'viejo', '{}', ?, ?, ?)`, uuid.New(), c.Now(), c.Now(), c.Now().Add(-age)).Error)
	}
	rec := &recorder{}
	var beats atomic.Int64
	d := newDispatcher(t, rec, c, outbox.DispatcherConfig{Poll: 10 * time.Millisecond, Heartbeat: func() { beats.Add(1) }})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- d.Run(ctx) }()

	require.Eventually(t, func() bool { return rec.count() == 2 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		var n int64
		require.NoError(t, pgtest.DB(t).Raw("SELECT count(*) FROM outbox WHERE topic = 'viejo'").Scan(&n).Error)
		return n == 1
	}, 5*time.Second, 10*time.Millisecond, "los publicados de más de 7 días se borran; los recientes, no")
	before := beats.Load()
	require.Eventually(t, func() bool { return beats.Load() > before }, time.Second, 5*time.Millisecond, "sigue latiendo sin trabajo")

	cancel()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run no se detuvo al cancelar el context")
	}
	assert.Zero(t, pending(t))
}
