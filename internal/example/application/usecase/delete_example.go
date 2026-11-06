package usecase

import (
	"context"
	"fmt"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/port"
	"myapp.v1.0.0/internal/shared/application/result"
)

// DeleteExampleUseCase elimina un example que no está activo.
//
// Como en el cambio de estado, el cliente no envía versión: la regla se valida sobre
// lo leído y el DELETE lleva la versión leída, así un example que otro activó entre
// la lectura y el borrado da EXAMPLE_VERSION_CONFLICT en lugar de borrarse activo.
type DeleteExampleUseCase struct {
	repo port.ExampleRepository
}

// NewDeleteExampleUseCase crea el caso de uso.
func NewDeleteExampleUseCase(repo port.ExampleRepository) *DeleteExampleUseCase {
	return &DeleteExampleUseCase{repo: repo}
}

// Execute elimina el example.
// Fallos: EXAMPLE_NOT_FOUND (404), EXAMPLE_FORBIDDEN (403), EXAMPLE_DELETE_ACTIVE (422), EXAMPLE_VERSION_CONFLICT (409).
func (uc *DeleteExampleUseCase) Execute(ctx context.Context, cmd dto.DeleteExampleCommand) (result.Result[struct{}], error) {
	example, found, err := uc.repo.FindByUUID(ctx, cmd.UUID)
	if err != nil {
		return result.FromError[struct{}](fmt.Errorf("buscar example %s: %w", cmd.UUID, err))
	}
	if !found {
		return result.FromError[struct{}](apperr.ErrExampleNotFound)
	}
	if err := example.CanBeManagedBy(cmd.Actor.UserUUID, cmd.Actor.IsAdmin); err != nil {
		return result.FromError[struct{}](err)
	}
	if err := example.CanDelete(); err != nil {
		return result.FromError[struct{}](err)
	}
	if err := uc.repo.Delete(ctx, example); err != nil {
		return result.FromError[struct{}](fmt.Errorf("eliminar example %s: %w", cmd.UUID, err))
	}
	return result.Ok(struct{}{}), nil
}
