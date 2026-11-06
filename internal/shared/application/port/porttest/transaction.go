// Package porttest contiene dobles de los puertos comunes de shared.
package porttest

import "context"

// TransactionManager ejecuta fn directamente, sin base de datos. Cuenta las llamadas,
// para comprobar que un caso de uso agrupa sus escrituras en una transacción.
//
// No simula el rollback: eso lo prueba la integración con PostgreSQL.
type TransactionManager struct{ Calls int }

// WithinTransaction ejecuta fn con el mismo context.
func (m *TransactionManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	m.Calls++
	return fn(ctx)
}
