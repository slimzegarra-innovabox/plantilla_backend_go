//go:build integration

package idempotency_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/idempotency"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency/idempotencytest"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

// owner es el dueño de las reservas de las pruebas que no lo distinguen.
var owner = uuid.New()

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

func newStore(t *testing.T) (*idempotency.PostgresStore, *clock) {
	t.Helper()
	pgtest.Truncate(t, "idempotency_keys")
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	return idempotency.NewPostgresStore(pgtest.DB(t), c, 24*time.Hour, time.Minute), c
}

func TestPostgresContract(t *testing.T) {
	idempotencytest.RunContract(t, func(t *testing.T) idempotency.Store {
		s, _ := newStore(t)
		return s
	})
}

func TestStaleReservationIsReclaimed(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	outcome, _, err := s.Begin(ctx, "u", "k", "f", owner)
	require.NoError(t, err)
	require.Equal(t, idempotency.Started, outcome)

	outcome, _, _ = s.Begin(ctx, "u", "k", "f", owner)
	assert.Equal(t, idempotency.InProgress, outcome, "dentro del plazo sigue reservada")

	c.Add(2 * time.Minute) // el proceso cayó sin completar ni abandonar
	outcome, _, err = s.Begin(ctx, "u", "k", "f", owner)
	require.NoError(t, err)
	assert.Equal(t, idempotency.Started, outcome, "vencida la reserva, un reintento se ejecuta")
}

func TestExpiredKeyIsReusedAndCleanedUp(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	_, _, err := s.Begin(ctx, "u", "k", "f", owner)
	require.NoError(t, err)
	require.NoError(t, s.Complete(ctx, "u", "k", owner, idempotency.Record{Status: 201, Body: []byte("{}")}))

	c.Add(25 * time.Hour)
	outcome, _, err := s.Begin(ctx, "u", "k", "otro-cuerpo", owner)
	require.NoError(t, err)
	assert.Equal(t, idempotency.Started, outcome, "una clave vencida se reutiliza, aunque cambie el cuerpo")

	_, _, err = s.Begin(ctx, "u2", "k2", "f", owner)
	require.NoError(t, err)
	c.Add(25 * time.Hour)
	n, err := s.Cleanup(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
}

// Cleanup vacía todo lo vencido en una llamada, aunque no quepa en una tanda.
func TestCleanupRemovesMoreThanOneBatch(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	require.NoError(t, pgtest.DB(t).Exec(`INSERT INTO idempotency_keys (scope, key, fingerprint, locked_until, expires_at, created_at)
		SELECT 'u', 'k' || g, 'f', ?, ?, ? FROM generate_series(1, 10005) g`, c.Now(), c.Now().Add(-time.Hour), c.Now()).Error)
	_, _, err := s.Begin(ctx, "u", "vigente", "f", owner)
	require.NoError(t, err)

	n, err := s.Cleanup(ctx)

	require.NoError(t, err)
	assert.EqualValues(t, 10005, n)
	var left int64
	require.NoError(t, pgtest.DB(t).Raw(`SELECT count(*) FROM idempotency_keys`).Scan(&left).Error)
	assert.EqualValues(t, 1, left, "lo vigente se conserva")
}

// Si la reserva vence y otra petición la toma, la original no debe pisarla: ni guardar su
// respuesta bajo la huella de la otra, ni liberar una reserva que ya no es suya.
func TestExpiredOwnerCannotTouchTheNewReservation(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	first, second := uuid.New(), uuid.New()
	_, _, err := s.Begin(ctx, "u", "k", "f", first)
	require.NoError(t, err)
	c.Add(2 * time.Minute)
	outcome, _, err := s.Begin(ctx, "u", "k", "otro-cuerpo", second)
	require.NoError(t, err)
	require.Equal(t, idempotency.Started, outcome)

	require.NoError(t, s.Abandon(ctx, "u", "k", first))
	outcome, _, err = s.Begin(ctx, "u", "k", "otro-cuerpo", uuid.New())
	require.NoError(t, err)
	assert.Equal(t, idempotency.InProgress, outcome, "la reserva de la segunda sigue en pie")

	err = s.Complete(ctx, "u", "k", first, idempotency.Record{Status: 201, Body: []byte(`{"de":"primera"}`)})
	require.ErrorIs(t, err, idempotency.ErrReservationLost)

	require.NoError(t, s.Complete(ctx, "u", "k", second, idempotency.Record{Status: 201, Body: []byte(`{"de":"segunda"}`)}))
	outcome, rec, err := s.Begin(ctx, "u", "k", "otro-cuerpo", uuid.New())
	require.NoError(t, err)
	assert.Equal(t, idempotency.Replay, outcome)
	assert.JSONEq(t, `{"de":"segunda"}`, string(rec.Body))
}

func TestLockAlwaysOutlivesTheRequest(t *testing.T) {
	assert.Equal(t, idempotency.DefaultLock, idempotency.LockFor(25*time.Second))
	assert.Greater(t, idempotency.LockFor(90*time.Second), 90*time.Second)
}

func TestCompletedKeyIsNotReclaimedByExpiredLock(t *testing.T) {
	s, c := newStore(t)
	ctx := context.Background()
	_, _, err := s.Begin(ctx, "u", "k", "f", owner)
	require.NoError(t, err)
	require.NoError(t, s.Complete(ctx, "u", "k", owner, idempotency.Record{Status: 201, Headers: map[string]string{"Location": "/x"}, Body: []byte(`{"a":1}`)}))

	c.Add(time.Hour) // venció el lock, no el TTL
	outcome, rec, err := s.Begin(ctx, "u", "k", "f", owner)
	require.NoError(t, err)
	assert.Equal(t, idempotency.Replay, outcome)
	assert.Equal(t, "/x", rec.Headers["Location"])
}
