package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type txKey struct{}

// DB devuelve la conexión que debe usar un adapter: la transacción del ctx si
// existe, o el pool base. SIEMPRE con WithContext(ctx), para que la cancelación
// y los timeouts de la petición lleguen al SQL.
//
// Los adapters nunca usan su *gorm.DB directamente: siempre DB(ctx, a.db).
func DB(ctx context.Context, base *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx.WithContext(ctx)
	}
	return base.WithContext(ctx)
}

// InTransaction indica si ctx lleva una transacción abierta por WithinTransaction.
func InTransaction(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(*gorm.DB)
	return ok
}

// GormTransactionManager implementa sharedport.TransactionManager con GORM.
type GormTransactionManager struct {
	db          *gorm.DB
	maxAttempts int
	baseDelay   time.Duration
}

// NewGormTransactionManager crea el gestor con 3 intentos y backoff de 50 ms, 100 ms.
func NewGormTransactionManager(db *gorm.DB) *GormTransactionManager {
	return &GormTransactionManager{db: db, maxAttempts: 3, baseDelay: 50 * time.Millisecond}
}

// WithinTransaction ejecuta fn en una transacción.
//
//   - Si ctx ya lleva una transacción, fn se une a ella (no se anidan transacciones).
//   - Solo se reintenta ante errores transitorios (40001 serialización, 40P01 deadlock).
//     Un *result.Failure o cualquier otro error nunca se reintenta.
//   - La espera entre intentos respeta la cancelación de ctx.
//   - Un panic dentro de fn hace rollback y se propaga (lo atrapa el recovery HTTP).
func (m *GormTransactionManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return fn(ctx)
	}
	return retryTransient(ctx, m.maxAttempts, m.baseDelay, func() error {
		return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return fn(context.WithValue(ctx, txKey{}, tx))
		})
	})
}

// retryTransient ejecuta op hasta maxAttempts veces mientras falle con un error transitorio.
func retryTransient(ctx context.Context, maxAttempts int, baseDelay time.Duration, op func() error) error {
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil || !IsTransient(err) || attempt >= maxAttempts {
			return err
		}
		delay := baseDelay << (attempt - 1)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("transacción cancelada tras %d intento(s): %w", attempt, ctx.Err())
		case <-timer.C:
		}
	}
}
