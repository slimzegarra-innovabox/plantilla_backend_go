package persistence

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/application/result"
)

func pgErr(code, constraint string) error {
	return fmt.Errorf("insertar: %w", &pgconn.PgError{Code: code, ConstraintName: constraint})
}

func TestViolationClassification(t *testing.T) {
	unique := pgErr("23505", "uq_examples_name")
	fk := pgErr("23503", "fk_orders_customer")
	notNull := pgErr("23502", "")

	assert.True(t, IsUniqueViolation(unique, "uq_examples_name"))
	assert.True(t, IsUniqueViolation(unique, ""))
	assert.False(t, IsUniqueViolation(unique, "uq_other"))
	assert.False(t, IsUniqueViolation(fk, ""))

	assert.True(t, IsForeignKeyViolation(fk, "fk_orders_customer"))
	assert.False(t, IsForeignKeyViolation(unique, ""))

	assert.True(t, IsDataConflict(unique))
	assert.True(t, IsDataConflict(fk))
	assert.False(t, IsDataConflict(notNull), "NOT NULL es un bug, no un conflicto")
	assert.False(t, IsDataConflict(errors.New("23505 en el texto no cuenta")))
}

func TestIsTransient(t *testing.T) {
	assert.True(t, IsTransient(pgErr("40001", "")))
	assert.True(t, IsTransient(pgErr("40P01", "")))
	assert.False(t, IsTransient(pgErr("23505", "")))
	assert.False(t, IsTransient(errors.New("timeout")))
	assert.False(t, IsTransient(result.NewFailure(result.Conflict, "X", "x")))
}

func TestRetryTransientRetriesOnlyTransientErrors(t *testing.T) {
	tests := map[string]struct {
		err       error
		wantCalls int
	}{
		"éxito":            {err: nil, wantCalls: 1},
		"serialización":    {err: pgErr("40001", ""), wantCalls: 3},
		"deadlock":         {err: pgErr("40P01", ""), wantCalls: 3},
		"unicidad":         {err: pgErr("23505", ""), wantCalls: 1},
		"fallo de negocio": {err: result.NewFailure(result.Conflict, "X", "x"), wantCalls: 1},
		"error genérico":   {err: errors.New("boom"), wantCalls: 1},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := retryTransient(context.Background(), 3, time.Microsecond, func() error {
				calls++
				return tt.err
			})
			assert.Equal(t, tt.wantCalls, calls)
			assert.Equal(t, tt.err, err)
		})
	}
}

func TestRetryTransientStopsAfterSuccess(t *testing.T) {
	calls := 0
	err := retryTransient(context.Background(), 3, time.Microsecond, func() error {
		calls++
		if calls == 1 {
			return pgErr("40001", "")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestRetryTransientRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	err := retryTransient(ctx, 5, time.Hour, func() error {
		calls++
		cancel()
		return pgErr("40001", "")
	})

	assert.Equal(t, 1, calls, "no espera una hora: la cancelación corta el backoff")
	assert.ErrorIs(t, err, context.Canceled)
}
