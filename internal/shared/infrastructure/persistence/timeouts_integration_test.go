//go:build integration

package persistence_test

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

// Comprueba que pgx envía los plazos como parámetros de sesión y que PostgreSQL los aplica.
func TestSessionTimeoutsAreAppliedByPostgres(t *testing.T) {
	cfg := pgtest.Config()
	cfg.StatementTimeout = 200 * time.Millisecond
	cfg.LockTimeout = 150 * time.Millisecond
	cfg.IdleInTxTimeout = time.Minute
	ctx := context.Background()
	db, err := persistence.Open(ctx, cfg, slog.New(slog.DiscardHandler), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = persistence.Close(db) })

	var stmt, lock, idle string
	require.NoError(t, db.Raw("SELECT current_setting('statement_timeout'), current_setting('lock_timeout'), "+
		"current_setting('idle_in_transaction_session_timeout')").Row().Scan(&stmt, &lock, &idle))
	assert.Equal(t, "200ms", stmt)
	assert.Equal(t, "150ms", lock)
	assert.Equal(t, "1min", idle)

	// Una sentencia más larga que statement_timeout la aborta PostgreSQL (57014),
	// aunque el context no tenga plazo.
	err = db.WithContext(ctx).Exec("SELECT pg_sleep(1)").Error
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "57014", pgErr.Code)
}

// La conexión de migraciones no lleva statement_timeout, pero sí lock_timeout.
func TestMigrationDSNHasNoStatementTimeout(t *testing.T) {
	cfg := pgtest.Config()
	cfg.StatementTimeout = 200 * time.Millisecond
	cfg.LockTimeout = 150 * time.Millisecond

	sqlDB, err := sql.Open("pgx", cfg.MigrationDSN()) // mismo driver que cmd/migrate
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	var stmt, lock string
	require.NoError(t, sqlDB.QueryRowContext(context.Background(),
		"SELECT current_setting('statement_timeout'), current_setting('lock_timeout')").Scan(&stmt, &lock))
	assert.Equal(t, "0", stmt)
	assert.Equal(t, "150ms", lock)
}
