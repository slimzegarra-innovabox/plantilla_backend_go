//go:build integration

// Package migrations verifica que cada migración es reversible: up → down → up.
package migrations_test

import (
	"errors"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func TestMigrationsAreReversible(t *testing.T) {
	m, err := pgtest.NewMigrate(pgtest.Config())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })

	// pgtest.Main ya aplicó todo: bajar hasta cero y volver a subir.
	require.NoError(t, m.Down(), "down de todas las migraciones")
	_, _, err = m.Version()
	require.ErrorIs(t, err, migrate.ErrNilVersion)

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("up tras down: %v", err)
	}
	_, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
}
