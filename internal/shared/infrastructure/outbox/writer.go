package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"

	sharedport "myapp.v1.0.0/internal/shared/application/port"
	sharedpersistence "myapp.v1.0.0/internal/shared/infrastructure/persistence"
)

// ErrNoTransaction indica que se intentó escribir en el outbox fuera de una
// transacción: el mensaje no quedaría atado al cambio de negocio, que es justo lo que
// el outbox garantiza. Es un error de programación del adapter que lo llama.
var ErrNoTransaction = errors.New("outbox: Write exige la transacción del caso de uso (TransactionManager.WithinTransaction)")

// Writer escribe mensajes en el outbox.
type Writer struct {
	db    *gorm.DB
	clock sharedport.Clock
}

// NewWriter crea el escritor.
func NewWriter(db *gorm.DB, clock sharedport.Clock) *Writer {
	return &Writer{db: db, clock: clock}
}

// Write guarda el mensaje en la transacción de ctx y devuelve su id. payload debe ser
// un struct de infraestructura con tags json: es el contrato con el consumidor.
func (w *Writer) Write(ctx context.Context, topic string, payload any) (uuid.UUID, error) {
	if !sharedpersistence.InTransaction(ctx) {
		return uuid.Nil, ErrNoTransaction
	}
	if topic == "" {
		return uuid.Nil, errors.New("outbox: el topic es obligatorio")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("outbox: serializar %s: %w", topic, err)
	}
	headers := map[string]string{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
	rawHeaders, err := json.Marshal(headers)
	if err != nil {
		return uuid.Nil, fmt.Errorf("outbox: serializar cabeceras: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("outbox: generar id: %w", err)
	}
	now := w.clock.Now()
	row := entity{UUID: id, Topic: topic, Payload: raw, Headers: rawHeaders, CreatedAt: now, AvailableAt: now}
	if err := sharedpersistence.DB(ctx, w.db).Create(&row).Error; err != nil {
		return uuid.Nil, fmt.Errorf("outbox: guardar %s: %w", topic, err)
	}
	return id, nil
}
