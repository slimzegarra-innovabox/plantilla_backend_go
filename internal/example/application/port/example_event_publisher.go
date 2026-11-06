package port

import (
	"context"

	"myapp.v1.0.0/internal/example/application/dto"
)

// ExampleEventPublisher publica los eventos de example para que otros procesos
// reaccionen (enviar un correo, actualizar una proyección…).
//
// Se llama DENTRO de la transacción del caso de uso: la implementación escribe en el
// outbox, así el evento se confirma o se descarta junto con el cambio de negocio, y
// se entrega después, al menos una vez. Sin fallos anticipados: todo error es técnico.
type ExampleEventPublisher interface {
	StatusChanged(ctx context.Context, event dto.ExampleStatusChanged) error
}
