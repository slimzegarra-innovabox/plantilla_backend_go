package idempotencytest_test

import (
	"testing"

	"myapp.v1.0.0/internal/shared/infrastructure/idempotency"
	"myapp.v1.0.0/internal/shared/infrastructure/idempotency/idempotencytest"
)

func TestMemoryContract(t *testing.T) {
	idempotencytest.RunContract(t, func(*testing.T) idempotency.Store { return idempotencytest.NewMemory() })
}
