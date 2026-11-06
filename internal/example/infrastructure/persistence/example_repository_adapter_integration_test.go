//go:build integration

package persistence_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/port"
	"myapp.v1.0.0/internal/example/application/port/porttest"
	"myapp.v1.0.0/internal/example/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/application/result"
	sharedpersistence "myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func newAdapter(t *testing.T) *persistence.ExampleRepositoryAdapter {
	t.Helper()
	pgtest.Truncate(t, "examples")
	return persistence.NewExampleRepositoryAdapter(pgtest.DB(t))
}

// El adapter real cumple el mismo contrato que el doble en memoria.
func TestExampleRepositoryAdapterContract(t *testing.T) {
	porttest.RunExampleRepositoryContract(t, func(t *testing.T) port.ExampleRepository {
		return newAdapter(t)
	})
}

// Un dato corrupto en la base de datos es un error técnico, no un fallo de negocio.
func TestCorruptRowIsTechnicalError(t *testing.T) {
	repo := newAdapter(t)
	id := uuid.New()
	// 'a' cumple los CHECK de la tabla pero no las reglas de ExampleName.
	require.NoError(t, pgtest.DB(t).Exec(
		"INSERT INTO examples (uuid, name, created_by) VALUES (?, 'a', ?)", id, uuid.New()).Error)

	e, found, err := repo.FindByUUID(context.Background(), id)

	require.Error(t, err)
	assert.False(t, found)
	assert.Nil(t, e)
	_, isFailure := result.AsFailure(err)
	assert.False(t, isFailure)
}

// Los DEFAULT de la migración coinciden con el model (una sola fuente de verdad).
func TestMigrationDefaultsMatchModel(t *testing.T) {
	repo := newAdapter(t)
	id := uuid.New()
	require.NoError(t, pgtest.DB(t).Exec(
		"INSERT INTO examples (uuid, name, created_by) VALUES (?, 'Por defecto', ?)", id, uuid.New()).Error)

	e, found, err := repo.FindByUUID(context.Background(), id)

	require.NoError(t, err)
	require.True(t, found)
	fresh := porttest.NewExample(t, "Nuevo", time.Now())
	assert.Equal(t, fresh.Status(), e.Status(), "estado inicial")
	assert.Equal(t, fresh.Version(), e.Version(), "versión inicial")
}

// El adapter escribe dentro de la transacción del context: un rollback lo deshace.
func TestAdapterJoinsTransactionFromContext(t *testing.T) {
	repo := newAdapter(t)
	tx := sharedpersistence.NewGormTransactionManager(pgtest.DB(t))
	e := porttest.NewExample(t, "Transaccional", time.Now())

	err := tx.WithinTransaction(context.Background(), func(ctx context.Context) error {
		_, err := repo.Create(ctx, e)
		require.NoError(t, err)
		return apperr.ErrExampleNameTaken // cualquier error provoca rollback
	})

	require.ErrorIs(t, err, apperr.ErrExampleNameTaken)
	_, found, err := repo.FindByUUID(context.Background(), e.UUID())
	require.NoError(t, err)
	assert.False(t, found, "el INSERT se revirtió con la transacción")
}

// Una consulta respeta la cancelación del context.
func TestAdapterRespectsContextCancellation(t *testing.T) {
	repo := newAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := repo.FindByUUID(ctx, uuid.New())

	require.ErrorIs(t, err, context.Canceled)
}
