package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/hibiken/asynq"

	sharedasynq "myapp.v1.0.0/internal/shared/infrastructure/external/asynq"
)

// dedupWindow es cuánto conserva Redis una tarea completada: mientras tanto, un
// republicado del mismo mensaje se descarta (ErrTaskIDConflict). Pasado ese plazo
// el consumidor debe ser idempotente por sí mismo (entrega al menos una vez).
const dedupWindow = time.Hour

// AsynqPublisher publica los mensajes del outbox como tareas de Asynq: topic = tipo de
// tarea, payload tal cual y TaskID = id del mensaje.
func AsynqPublisher(p *sharedasynq.Publisher) Publisher { return asynqPublisher{p} }

type asynqPublisher struct{ p *sharedasynq.Publisher }

func (a asynqPublisher) Publish(ctx context.Context, m Message) error {
	_, err := a.p.Publish(ctx, m.Topic, m.Payload, asynq.TaskID(m.ID.String()), asynq.Retention(dedupWindow))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil // ya se publicó (un republicado tras un fallo al marcar): idempotente
	}
	return err
}

func (a asynqPublisher) Ping(ctx context.Context) error { return a.p.Ping(ctx) }
