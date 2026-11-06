// Package idempotencytest contiene el doble en memoria de idempotency.Store y la suite
// de contrato que cumple junto con el almacén de PostgreSQL. Solo lo usan las pruebas.
package idempotencytest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"myapp.v1.0.0/internal/shared/infrastructure/idempotency"
)

// Memory implementa idempotency.Store en memoria, sin vencimientos: las pruebas HTTP
// no los necesitan (los cubre la suite de contrato sobre PostgreSQL).
type Memory struct {
	mu   sync.Mutex
	rows map[string]*row
}

type row struct {
	fingerprint string
	owner       uuid.UUID
	done        bool
	rec         idempotency.Record
}

// NewMemory crea el doble.
func NewMemory() *Memory { return &Memory{rows: map[string]*row{}} }

// Begin implementa idempotency.Store.
func (m *Memory) Begin(_ context.Context, scope, key, fingerprint string, owner uuid.UUID) (idempotency.Outcome, idempotency.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[scope+"|"+key]
	switch {
	case !ok:
		m.rows[scope+"|"+key] = &row{fingerprint: fingerprint, owner: owner}
		return idempotency.Started, idempotency.Record{}, nil
	case r.fingerprint != fingerprint:
		return idempotency.Mismatch, idempotency.Record{}, nil
	case !r.done:
		return idempotency.InProgress, idempotency.Record{}, nil
	}
	return idempotency.Replay, r.rec, nil
}

// Complete implementa idempotency.Store.
func (m *Memory) Complete(_ context.Context, scope, key string, owner uuid.UUID, rec idempotency.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[scope+"|"+key]
	if !ok || r.done || r.owner != owner {
		return idempotency.ErrReservationLost
	}
	r.done, r.rec = true, rec
	return nil
}

// Abandon implementa idempotency.Store.
func (m *Memory) Abandon(_ context.Context, scope, key string, owner uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[scope+"|"+key]; ok && !r.done && r.owner == owner {
		delete(m.rows, scope+"|"+key)
	}
	return nil
}

// owner es el dueño de las reservas que hace expect en la suite de contrato.
var owner = uuid.New()

// RunContract comprueba la semántica común de un Store.
func RunContract(t *testing.T, newStore func(t *testing.T) idempotency.Store) {
	t.Helper()
	ctx := context.Background()
	rec := idempotency.Record{Status: 201, Headers: map[string]string{"Location": "/x/1"}, Body: []byte(`{"data":1}`)}

	t.Run("la primera reserva, la segunda ve en curso y después repite", func(t *testing.T) {
		s := newStore(t)
		expect(ctx, t, s, "u", "k", "f", idempotency.Started)
		expect(ctx, t, s, "u", "k", "f", idempotency.InProgress)
		if err := s.Complete(ctx, "u", "k", owner, rec); err != nil {
			t.Fatal(err)
		}
		outcome, got, err := s.Begin(ctx, "u", "k", "f", uuid.New())
		if err != nil || outcome != idempotency.Replay {
			t.Fatalf("Begin = %v, %v; se esperaba Replay", outcome, err)
		}
		if got.Status != 201 || got.Headers["Location"] != "/x/1" || string(got.Body) != `{"data":1}` {
			t.Fatalf("respuesta repetida distinta: %+v", got)
		}
	})

	t.Run("otro cuerpo con la misma clave es Mismatch, en curso y completada", func(t *testing.T) {
		s := newStore(t)
		expect(ctx, t, s, "u", "k", "f", idempotency.Started)
		expect(ctx, t, s, "u", "k", "otro", idempotency.Mismatch)
		if err := s.Complete(ctx, "u", "k", owner, rec); err != nil {
			t.Fatal(err)
		}
		expect(ctx, t, s, "u", "k", "otro", idempotency.Mismatch)
	})

	t.Run("el alcance separa las claves", func(t *testing.T) {
		s := newStore(t)
		expect(ctx, t, s, "u1", "k", "f", idempotency.Started)
		expect(ctx, t, s, "u2", "k", "f", idempotency.Started)
	})

	t.Run("Abandon libera la clave, pero no borra una completada", func(t *testing.T) {
		s := newStore(t)
		expect(ctx, t, s, "u", "k", "f", idempotency.Started)
		if err := s.Abandon(ctx, "u", "k", owner); err != nil {
			t.Fatal(err)
		}
		expect(ctx, t, s, "u", "k", "f", idempotency.Started)
		if err := s.Complete(ctx, "u", "k", owner, rec); err != nil {
			t.Fatal(err)
		}
		if err := s.Abandon(ctx, "u", "k", owner); err != nil {
			t.Fatal(err)
		}
		expect(ctx, t, s, "u", "k", "f", idempotency.Replay)
	})

	t.Run("solo la petición dueña completa o libera su reserva", func(t *testing.T) {
		s := newStore(t)
		expect(ctx, t, s, "u", "k", "f", idempotency.Started)
		other := uuid.New()
		if err := s.Complete(ctx, "u", "k", other, rec); !errors.Is(err, idempotency.ErrReservationLost) {
			t.Fatalf("Complete de otra petición = %v; se esperaba ErrReservationLost", err)
		}
		if err := s.Abandon(ctx, "u", "k", other); err != nil {
			t.Fatal(err)
		}
		expect(ctx, t, s, "u", "k", "f", idempotency.InProgress)
		if err := s.Complete(ctx, "u", "k", owner, rec); err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, "u", "k", owner, rec); !errors.Is(err, idempotency.ErrReservationLost) {
			t.Fatalf("segundo Complete = %v; se esperaba ErrReservationLost", err)
		}
	})

	t.Run("de peticiones simultáneas, solo una queda reservada", func(t *testing.T) {
		s := newStore(t)
		var mu sync.Mutex
		started := 0
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				outcome, _, err := s.Begin(ctx, "u", "k", "f", uuid.New())
				if err != nil {
					t.Error(err)
					return
				}
				if outcome == idempotency.Started {
					mu.Lock()
					started++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if started != 1 {
			t.Fatalf("reservaron %d peticiones; se esperaba 1", started)
		}
	})
}

func expect(ctx context.Context, t *testing.T, s idempotency.Store, scope, key, fp string, want idempotency.Outcome) {
	t.Helper()
	got, _, err := s.Begin(ctx, scope, key, fp, owner)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Begin(%s,%s,%s) = %v; se esperaba %v", scope, key, fp, got, want)
	}
}
