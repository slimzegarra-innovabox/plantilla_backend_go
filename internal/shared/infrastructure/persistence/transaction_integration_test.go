//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"myapp.v1.0.0/internal/shared/application/result"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func setupProbe(t *testing.T) {
	t.Helper()
	db := pgtest.DB(t)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS tx_probe (v TEXT NOT NULL)").Error)
	pgtest.Truncate(t, "tx_probe")
}

func insert(ctx context.Context, t *testing.T, v string) {
	t.Helper()
	require.NoError(t, persistence.DB(ctx, pgtest.DB(t)).Exec("INSERT INTO tx_probe (v) VALUES (?)", v).Error)
}

func count(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pgtest.DB(t).Raw("SELECT count(*) FROM tx_probe").Scan(&n).Error)
	return n
}

func TestWithinTransactionCommits(t *testing.T) {
	setupProbe(t)
	tm := persistence.NewGormTransactionManager(pgtest.DB(t))

	err := tm.WithinTransaction(context.Background(), func(ctx context.Context) error {
		insert(ctx, t, "a")
		insert(ctx, t, "b")
		return nil
	})

	require.NoError(t, err)
	assert.EqualValues(t, 2, count(t))
}

func TestWithinTransactionRollsBackOnErrorAndFailure(t *testing.T) {
	for name, cause := range map[string]error{
		"error técnico":    errors.New("boom"),
		"fallo de negocio": result.NewFailure(result.Conflict, "X", "x"),
	} {
		t.Run(name, func(t *testing.T) {
			setupProbe(t)
			tm := persistence.NewGormTransactionManager(pgtest.DB(t))

			err := tm.WithinTransaction(context.Background(), func(ctx context.Context) error {
				insert(ctx, t, "a")
				return cause
			})

			require.ErrorIs(t, err, cause, "el error se devuelve tal cual")
			assert.Zero(t, count(t), "rollback")
		})
	}
}

func TestWithinTransactionRollsBackOnPanic(t *testing.T) {
	setupProbe(t)
	tm := persistence.NewGormTransactionManager(pgtest.DB(t))

	assert.Panics(t, func() {
		_ = tm.WithinTransaction(context.Background(), func(ctx context.Context) error {
			insert(ctx, t, "a")
			panic("bug")
		})
	})
	assert.Zero(t, count(t))
}

func TestNestedTransactionJoinsOuter(t *testing.T) {
	setupProbe(t)
	tm := persistence.NewGormTransactionManager(pgtest.DB(t))

	err := tm.WithinTransaction(context.Background(), func(ctx context.Context) error {
		insert(ctx, t, "outer")
		innerErr := tm.WithinTransaction(ctx, func(ctx context.Context) error {
			insert(ctx, t, "inner")
			return nil
		})
		require.NoError(t, innerErr)
		return errors.New("falla el exterior")
	})

	require.Error(t, err)
	assert.Zero(t, count(t), "la interna se unió a la externa: el rollback deshace ambas")
}

func TestOutsideTransactionWritesAreImmediate(t *testing.T) {
	setupProbe(t)
	insert(context.Background(), t, "a")
	assert.EqualValues(t, 1, count(t))
}

func TestOpenAndPing(t *testing.T) {
	db, err := persistence.Open(context.Background(), pgtest.Config(), slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = persistence.Close(db) })

	require.NoError(t, persistence.Ping(db)(context.Background()))
}

func TestOpenFailsWithWrongPassword(t *testing.T) {
	cfg := pgtest.Config()
	cfg.Password = "incorrecta"

	_, err := persistence.Open(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), false)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "incorrecta", "el error no incluye la contraseña")
}

func TestCountUpToIsCappedRespectsFiltersAndTheTransaction(t *testing.T) {
	setupProbe(t)
	db := pgtest.DB(t)
	for _, v := range []string{"a", "a", "a", "b"} {
		insert(context.Background(), t, v)
	}
	probe := func(ctx context.Context) *gorm.DB { return persistence.DB(ctx, db).Table("tx_probe") }
	ctx := context.Background()

	total, err := persistence.CountUpTo(probe(ctx), 100)
	require.NoError(t, err)
	assert.EqualValues(t, 4, total)

	total, err = persistence.CountUpTo(probe(ctx), 2)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "el tope acota el conteo")

	total, err = persistence.CountUpTo(probe(ctx).Where("v = ?", "b"), 100)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "respeta los filtros")

	tm := persistence.NewGormTransactionManager(db)
	require.NoError(t, tm.WithinTransaction(ctx, func(ctx context.Context) error {
		insert(ctx, t, "b")
		total, err := persistence.CountUpTo(probe(ctx), 100)
		require.NoError(t, err)
		assert.EqualValues(t, 5, total, "ve lo escrito en su transacción")
		return nil
	}))
}
