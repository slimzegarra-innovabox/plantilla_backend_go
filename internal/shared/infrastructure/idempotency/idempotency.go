// Package idempotency guarda las respuestas de las peticiones con Idempotency-Key
// (ADR 0007). Lo usa el middleware sharedhttp.Idempotent.
package idempotency

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrReservationLost indica que la reserva ya no es de quien la pidió: venció y otra
// petición la tomó. Lo que esta quisiera guardar se descarta, para no pisar a la otra.
var ErrReservationLost = errors.New("idempotencia: la reserva pertenece a otra petición")

// Outcome es el resultado de intentar reservar una clave.
type Outcome int

const (
	// Started: la clave quedó reservada; esta petición debe ejecutarse.
	Started Outcome = iota + 1
	// Replay: la clave ya se completó con el mismo cuerpo; se repite su respuesta.
	Replay
	// InProgress: otra petición con la misma clave sigue ejecutándose.
	InProgress
	// Mismatch: la clave ya se usó con otro cuerpo.
	Mismatch
)

// Record es la respuesta guardada.
type Record struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// Store guarda el estado de las claves.
type Store interface {
	// Begin reserva la clave de forma atómica, o informa por qué no se pudo. Una
	// reserva cuyo plazo venció (el proceso cayó) o una clave vencida se reutilizan.
	// El Record solo tiene contenido con Replay. owner identifica a esta petición: solo
	// ella puede completar o liberar la reserva.
	Begin(ctx context.Context, scope, key, fingerprint string, owner uuid.UUID) (Outcome, Record, error)
	// Complete guarda la respuesta de una petición reservada. Si la reserva ya no es de
	// owner, devuelve ErrReservationLost y no guarda nada.
	Complete(ctx context.Context, scope, key string, owner uuid.UUID, rec Record) error
	// Abandon libera la clave: la petición falló sin un resultado que repetir (5xx,
	// panic) y un reintento debe ejecutarse de verdad. Si la reserva ya no es de owner,
	// no hace nada.
	Abandon(ctx context.Context, scope, key string, owner uuid.UUID) error
}
