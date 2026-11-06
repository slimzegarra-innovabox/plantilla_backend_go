//go:build integration

package messaging_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/usecase"
	"myapp.v1.0.0/internal/example/infrastructure/messaging"
	"myapp.v1.0.0/internal/example/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/outbox"
	sharedpersistence "myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// failingAfterWrite escribe el evento y luego falla: simula un error posterior dentro
// de la transacción, que debe revertir el evento Y el cambio de estado.
type failingAfterWrite struct {
	inner *messaging.OutboxExampleEventPublisher
}

func (f failingAfterWrite) StatusChanged(ctx context.Context, e dto.ExampleStatusChanged) error {
	if err := f.inner.StatusChanged(ctx, e); err != nil {
		return err
	}
	return errors.New("fallo posterior a escribir el evento")
}

func setup(t *testing.T) (*persistence.ExampleRepositoryAdapter, *usecase.CreateExampleUseCase, *outbox.Writer, *sharedpersistence.GormTransactionManager) {
	t.Helper()
	pgtest.Truncate(t, "examples", "outbox")
	db := pgtest.DB(t)
	repo := persistence.NewExampleRepositoryAdapter(db)
	return repo, usecase.NewCreateExampleUseCase(repo, fixedClock{t0}), outbox.NewWriter(db, fixedClock{t0}), sharedpersistence.NewGormTransactionManager(db)
}

func TestActivateWritesTheEventInTheSameTransaction(t *testing.T) {
	repo, create, w, tm := setup(t)
	created, err := create.Execute(context.Background(), dto.CreateExampleCommand{Name: "Demo", CreatedBy: uuid.New()})
	require.NoError(t, err)
	uc := usecase.NewChangeExampleStatusUseCase(repo, messaging.NewOutboxExampleEventPublisher(w), tm, fixedClock{t0.Add(time.Minute)})

	res, err := uc.Execute(context.Background(), dto.ChangeExampleStatusCommand{Actor: dto.ExampleActor{IsAdmin: true}, UUID: created.Data().UUID, Action: dto.ActionActivate})

	require.NoError(t, err)
	require.True(t, res.IsSuccess())
	var row struct {
		Topic   string
		Payload string
	}
	require.NoError(t, pgtest.DB(t).Raw("SELECT topic, payload::text AS payload FROM outbox").Scan(&row).Error)
	assert.Equal(t, messaging.TopicStatusChanged, row.Topic)
	assert.JSONEq(t, `{"example_uuid":"`+created.Data().UUID.String()+`","status":"active","version":2,"occurred_at":"2026-11-05T12:01:00Z"}`, row.Payload)
}

func TestEventFailureRollsBackTheStatusChange(t *testing.T) {
	repo, create, w, tm := setup(t)
	created, err := create.Execute(context.Background(), dto.CreateExampleCommand{Name: "Demo", CreatedBy: uuid.New()})
	require.NoError(t, err)
	uc := usecase.NewChangeExampleStatusUseCase(repo, failingAfterWrite{messaging.NewOutboxExampleEventPublisher(w)}, tm, fixedClock{t0})

	_, err = uc.Execute(context.Background(), dto.ChangeExampleStatusCommand{Actor: dto.ExampleActor{IsAdmin: true}, UUID: created.Data().UUID, Action: dto.ActionActivate})

	require.Error(t, err)
	got, found, err := repo.FindByUUID(context.Background(), created.Data().UUID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "pending", string(got.Status()), "el cambio de estado se revirtió")
	assert.Equal(t, 1, got.Version())
	var n int64
	require.NoError(t, pgtest.DB(t).Raw("SELECT count(*) FROM outbox").Scan(&n).Error)
	assert.Zero(t, n, "el evento se revirtió junto con el cambio")
}
