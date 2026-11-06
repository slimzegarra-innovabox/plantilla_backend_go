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

// UpdateExampleUseCase renombra un example con concurrencia optimista.
//
// No necesita TransactionManager: es una única escritura sobre un único registro,
// y la condición de versión del UPDATE ya la hace atómica frente a otros escritores.
type UpdateExampleUseCase struct {
	repo  port.ExampleRepository
	clock sharedport.Clock
}

// NewUpdateExampleUseCase crea el caso de uso.
func NewUpdateExampleUseCase(repo port.ExampleRepository, clock sharedport.Clock) *UpdateExampleUseCase {
	return &UpdateExampleUseCase{repo: repo, clock: clock}
}

// Execute renombra el example.
// Fallos: EXAMPLE_NAME_* (400), EXAMPLE_NOT_FOUND (404), EXAMPLE_VERSION_CONFLICT (409),
// EXAMPLE_NAME_TAKEN (409).
func (uc *UpdateExampleUseCase) Execute(ctx context.Context, cmd dto.UpdateExampleCommand) (result.Result[dto.ExampleData], error) {
	name, err := model.NewExampleName(cmd.Name)
	if err != nil {
		return result.FromError[dto.ExampleData](err)
	}

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
	// El cliente editó una versión vieja: se rechaza antes de escribir.
	if example.Version() != cmd.Version {
		return result.FromError[dto.ExampleData](apperr.ErrExampleVersionConflict)
	}

	example.Rename(name, uc.clock.Now())

	// Si otro escritor se adelanta entre la lectura y esta escritura, el adapter
	// devuelve ErrExampleVersionConflict.
	updated, err := uc.repo.Update(ctx, example)
	if err != nil {
		return result.FromError[dto.ExampleData](fmt.Errorf("actualizar example %s: %w", cmd.UUID, err))
	}
	return result.Ok(mapper.ToExampleData(updated)), nil
}
