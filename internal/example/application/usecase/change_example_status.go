package usecase

import (
	"context"
	"fmt"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/mapper"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port"
	sharedport "myapp.v1.0.0/internal/shared/application/port"
	"myapp.v1.0.0/internal/shared/application/result"
)

// ChangeExampleStatusUseCase aplica una intención de negocio (activar, desactivar).
//
// El cliente no envía versión: la intención no pisa datos editados por otro, y la
// condición de versión del UPDATE protege la carrera entre lectura y escritura.
//
// Hace dos escrituras (el UPDATE y el evento en el outbox), así que van en una
// transacción (§6.2): el evento existe si y solo si el cambio se guardó.
type ChangeExampleStatusUseCase struct {
	repo   port.ExampleRepository
	events port.ExampleEventPublisher
	tx     sharedport.TransactionManager
	clock  sharedport.Clock
}

// NewChangeExampleStatusUseCase crea el caso de uso.
func NewChangeExampleStatusUseCase(repo port.ExampleRepository, events port.ExampleEventPublisher,
	tx sharedport.TransactionManager, clock sharedport.Clock,
) *ChangeExampleStatusUseCase {
	return &ChangeExampleStatusUseCase{repo: repo, events: events, tx: tx, clock: clock}
}

// Execute aplica la acción.
// Fallos: EXAMPLE_NOT_FOUND (404), EXAMPLE_FORBIDDEN (403), EXAMPLE_INVALID_TRANSITION (422), EXAMPLE_VERSION_CONFLICT (409).
func (uc *ChangeExampleStatusUseCase) Execute(ctx context.Context, cmd dto.ChangeExampleStatusCommand) (result.Result[dto.ExampleData], error) {
	example, found, err := uc.repo.FindByUUID(ctx, cmd.UUID)
	if err != nil {
		return result.FromError[dto.ExampleData](fmt.Errorf("buscar example %s: %w", cmd.UUID, err))
	}
	if !found {
		return result.FromError[dto.ExampleData](apperr.ErrExampleNotFound)
	}

	if err := example.CanBeManagedBy(cmd.Actor.UserUUID, cmd.Actor.IsAdmin); err != nil {
		return result.FromError[dto.ExampleData](err)
	}

	now := uc.clock.Now()
	switch cmd.Action {
	case dto.ActionActivate:
		err = example.Activate(now)
	case dto.ActionDeactivate:
		err = example.Deactivate(now)
	default:
		// Error de programación del adaptador de entrada, no del usuario.
		return result.Result[dto.ExampleData]{}, fmt.Errorf("acción de estado desconocida: %d", cmd.Action)
	}
	if err != nil {
		return result.FromError[dto.ExampleData](err)
	}

	var updated *model.Example
	err = uc.tx.WithinTransaction(ctx, func(ctx context.Context) error {
		var err error
		if updated, err = uc.repo.Update(ctx, example); err != nil {
			return fmt.Errorf("cambiar estado de example %s: %w", cmd.UUID, err)
		}
		if err := uc.events.StatusChanged(ctx, mapper.ToExampleStatusChanged(updated)); err != nil {
			return fmt.Errorf("publicar cambio de estado de example %s: %w", cmd.UUID, err)
		}
		return nil
	})
	if err != nil {
		// Un conflicto de versión (Failure) llega como fallo de negocio; el resto, técnico.
		return result.FromError[dto.ExampleData](err)
	}
	return result.Ok(mapper.ToExampleData(updated)), nil
}
