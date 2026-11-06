package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	sharedport "myapp.v1.0.0/internal/shared/application/port"
)

const (
	// DefaultTTL es cuánto se conserva una respuesta para repetirla.
	DefaultTTL = 24 * time.Hour
	// DefaultLock es la duración mínima de la reserva de una petición en curso (LockFor).
	DefaultLock = time.Minute
	// lockMargin es lo que la reserva dura más que el plazo de la petición, para que le
	// dé tiempo de guardar su respuesta.
	lockMargin = 30 * time.Second
	// cleanupBatch es cuántas claves vencidas se borran de una vez.
	cleanupBatch = 10000
)

// LockFor es la duración de la reserva para un plazo de petición dado: siempre lo supera.
// Si durara menos, un reintento se ejecutaría en paralelo con la petición original.
func LockFor(requestTimeout time.Duration) time.Duration {
	return max(DefaultLock, requestTimeout+lockMargin)
}

// PostgresStore implementa Store. No usa la transacción del caso de uso: la reserva
// debe verse desde otras peticiones antes de que este termine.
type PostgresStore struct {
	db    *gorm.DB
	clock sharedport.Clock
	ttl   time.Duration
	lock  time.Duration
}

// NewPostgresStore crea el almacén.
func NewPostgresStore(db *gorm.DB, clock sharedport.Clock, ttl, lock time.Duration) *PostgresStore {
	return &PostgresStore{db: db, clock: clock, ttl: ttl, lock: lock}
}

var _ Store = (*PostgresStore)(nil)

// Begin implementa Store con una sola sentencia atómica: de varias peticiones
// simultáneas con la misma clave, solo una inserta o recupera la fila.
func (s *PostgresStore) Begin(ctx context.Context, scope, key, fingerprint string, owner uuid.UUID) (Outcome, Record, error) {
	now := s.clock.Now()
	res := s.db.WithContext(ctx).Exec(`
		INSERT INTO idempotency_keys (scope, key, fingerprint, owner, locked_until, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (scope, key) DO UPDATE SET
			fingerprint = EXCLUDED.fingerprint, owner = EXCLUDED.owner, status = NULL, headers = NULL, body = NULL,
			locked_until = EXCLUDED.locked_until, expires_at = EXCLUDED.expires_at, created_at = EXCLUDED.created_at
		WHERE idempotency_keys.expires_at <= ?
		   OR (idempotency_keys.status IS NULL AND idempotency_keys.locked_until <= ?)`,
		scope, key, fingerprint, owner, now.Add(s.lock), now.Add(s.ttl), now, now, now)
	if res.Error != nil {
		return 0, Record{}, fmt.Errorf("idempotencia: reservar: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return Started, Record{}, nil
	}

	var row struct {
		Fingerprint string
		Status      *int
		Headers     []byte
		Body        []byte
	}
	read := s.db.WithContext(ctx).Raw(`SELECT fingerprint, status, headers, body FROM idempotency_keys WHERE scope = ? AND key = ?`, scope, key).Scan(&row)
	if read.Error != nil {
		return 0, Record{}, fmt.Errorf("idempotencia: leer: %w", read.Error)
	}
	switch {
	case read.RowsAffected == 0:
		// Otra petición la abandonó entre las dos sentencias: el cliente reintenta.
		return InProgress, Record{}, nil
	case row.Fingerprint != fingerprint:
		return Mismatch, Record{}, nil
	case row.Status == nil:
		return InProgress, Record{}, nil
	}
	rec := Record{Status: *row.Status, Body: row.Body}
	if len(row.Headers) > 0 {
		if err := json.Unmarshal(row.Headers, &rec.Headers); err != nil {
			return 0, Record{}, fmt.Errorf("idempotencia: cabeceras guardadas inválidas: %w", err)
		}
	}
	return Replay, rec, nil
}

// Complete implementa Store.
func (s *PostgresStore) Complete(ctx context.Context, scope, key string, owner uuid.UUID, rec Record) error {
	if rec.Status == 0 {
		return errors.New("idempotencia: respuesta sin estado")
	}
	headers, err := json.Marshal(rec.Headers)
	if err != nil {
		return fmt.Errorf("idempotencia: serializar cabeceras: %w", err)
	}
	res := s.db.WithContext(ctx).Exec(`UPDATE idempotency_keys SET status = ?, headers = ?, body = ?
		WHERE scope = ? AND key = ? AND owner = ? AND status IS NULL`,
		rec.Status, string(headers), rec.Body, scope, key, owner)
	if res.Error != nil {
		return fmt.Errorf("idempotencia: guardar respuesta: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrReservationLost
	}
	return nil
}

// Abandon implementa Store.
func (s *PostgresStore) Abandon(ctx context.Context, scope, key string, owner uuid.UUID) error {
	if err := s.db.WithContext(ctx).Exec(`DELETE FROM idempotency_keys WHERE scope = ? AND key = ? AND owner = ? AND status IS NULL`, scope, key, owner).Error; err != nil {
		return fmt.Errorf("idempotencia: liberar: %w", err)
	}
	return nil
}

// Cleanup borra las claves vencidas, en tandas para no bloquear, y devuelve cuántas borró.
// Varias réplicas pueden hacerlo a la vez.
func (s *PostgresStore) Cleanup(ctx context.Context) (int64, error) {
	var total int64
	for {
		res := s.db.WithContext(ctx).Exec(`
			DELETE FROM idempotency_keys WHERE (scope, key) IN (
				SELECT scope, key FROM idempotency_keys WHERE expires_at <= ?
				LIMIT ? FOR UPDATE SKIP LOCKED)`, s.clock.Now(), cleanupBatch)
		if res.Error != nil {
			return total, fmt.Errorf("idempotencia: limpiar: %w", res.Error)
		}
		total += res.RowsAffected
		if res.RowsAffected < cleanupBatch {
			return total, nil
		}
	}
}
