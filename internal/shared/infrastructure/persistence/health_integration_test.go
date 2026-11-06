//go:build integration

package persistence_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

// Con el pool de la aplicación agotado, el health check sigue respondiendo.
func TestHealthCheckerDoesNotCompeteForTheAppPool(t *testing.T) {
	cfg := pgtest.Config()
	cfg.MaxOpenConns, cfg.MaxIdleConns = 1, 1
	app, err := persistence.Open(context.Background(), cfg, slog.New(slog.DiscardHandler), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = persistence.Close(app) })
	checker, err := persistence.NewHealthChecker(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = checker.Close() })

	// Ocupa la única conexión del pool con una transacción abierta.
	tx := app.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	assert.Error(t, persistence.Ping(app)(ctx), "el pool está lleno: el ping por el pool espera y vence")
	assert.NoError(t, checker.Ping(context.Background()), "la conexión propia de las sondas responde")
}
