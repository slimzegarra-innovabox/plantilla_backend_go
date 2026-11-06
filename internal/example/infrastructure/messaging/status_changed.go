// Package messaging contiene los mensajes asíncronos del módulo example: su contrato
// (topic y payload), el adapter que los publica por el outbox y los handlers que los
// procesan en cmd/worker. Productor y consumidor comparten la misma definición.
package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/port"
	sharedasynq "myapp.v1.0.0/internal/shared/infrastructure/external/asynq"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
	"myapp.v1.0.0/internal/shared/infrastructure/outbox"
)

// TopicStatusChanged es el tipo de tarea del evento. Es un contrato: no se renombra
// (los mensajes ya encolados lo usan).
const TopicStatusChanged = "example:status_changed"

// statusChangedPayload es el contrato JSON del evento (snake_case, explícito).
type statusChangedPayload struct {
	ExampleUUID uuid.UUID `json:"example_uuid"`
	Status      string    `json:"status"`
	Version     int       `json:"version"`
	OccurredAt  time.Time `json:"occurred_at"`
}

//--------------------------------------------------------------
// SALIDA: implementa port.ExampleEventPublisher con el outbox
//--------------------------------------------------------------

// NewExampleEventPublisher elige el adapter según la configuración: con el outbox
// desactivado (REDIS_ENABLED=false, w == nil), los eventos se descartan. En este módulo
// son opcionales: el cambio de estado es válido aunque nadie lo escuche.
func NewExampleEventPublisher(w *outbox.Writer) port.ExampleEventPublisher {
	if w == nil {
		return discardExampleEvents{}
	}
	return NewOutboxExampleEventPublisher(w)
}

type discardExampleEvents struct{}

func (discardExampleEvents) StatusChanged(context.Context, dto.ExampleStatusChanged) error {
	return nil
}

// OutboxExampleEventPublisher escribe los eventos en el outbox, dentro de la
// transacción del caso de uso. cmd/worker los publica después en Asynq.
type OutboxExampleEventPublisher struct{ outbox *outbox.Writer }

// NewOutboxExampleEventPublisher crea el adapter.
func NewOutboxExampleEventPublisher(w *outbox.Writer) *OutboxExampleEventPublisher {
	return &OutboxExampleEventPublisher{outbox: w}
}

// StatusChanged implementa port.ExampleEventPublisher.
func (p *OutboxExampleEventPublisher) StatusChanged(ctx context.Context, e dto.ExampleStatusChanged) error {
	_, err := p.outbox.Write(ctx, TopicStatusChanged, statusChangedPayload{
		ExampleUUID: e.ExampleUUID, Status: e.Status, Version: e.Version, OccurredAt: e.OccurredAt.UTC(),
	})
	return err
}

//--------------------------------------------------------------
// ENTRADA: handler de la tarea en cmd/worker
//--------------------------------------------------------------

// handleStatusChanged procesa el evento. Es el punto donde un proyecto reacciona
// (enviar un correo, actualizar una proyección, notificar a otro sistema) llamando a
// un caso de uso. Debe ser idempotente: la entrega es al menos una vez, y la versión
// del evento permite descartar uno que llega después de otro más nuevo.
func handleStatusChanged(ctx context.Context, t *asynq.Task) error {
	var p statusChangedPayload
	if err := sharedasynq.DecodePayload(t, &p); err != nil {
		return err // payload inválido: no se reintenta
	}
	if p.ExampleUUID == uuid.Nil {
		return fmt.Errorf("evento sin example_uuid: %w", sharedasynq.SkipRetry)
	}
	logging.FromContext(ctx).InfoContext(ctx, "example cambió de estado",
		"example_uuid", p.ExampleUUID, "status", p.Status, "version", p.Version)
	return nil
}

// RegisterHandlers registra los handlers de las tareas del módulo.
func RegisterHandlers(mux *asynq.ServeMux) {
	mux.HandleFunc(TopicStatusChanged, handleStatusChanged)
}
