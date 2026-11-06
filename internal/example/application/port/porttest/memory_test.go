package porttest_test

import (
	"testing"

	"myapp.v1.0.0/internal/example/application/port"
	"myapp.v1.0.0/internal/example/application/port/porttest"
)

// El doble en memoria cumple el mismo contrato que el adapter de PostgreSQL
// (ver infrastructure/persistence, etiqueta integration).
func TestInMemoryExampleRepositoryContract(t *testing.T) {
	porttest.RunExampleRepositoryContract(t, func(*testing.T) port.ExampleRepository {
		return porttest.NewInMemoryExampleRepository()
	})
}
