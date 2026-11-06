// Package usecase contiene los casos de uso del módulo example: uno por archivo,
// cada uno con un único método público Execute.
package usecase

import (
	"context"
	"fmt"

	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/mapper"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port"
	sharedport "myapp.v1.0.0/internal/shared/application/port"
	"myapp.v1.0.0/internal/shared/application/result"
)

// CreateExampleUseCase crea un example en estado pending.
type CreateExampleUseCase struct {
	repo  port.ExampleRepository
	clock sharedport.Clock
}

// NewCreateExampleUseCase crea el caso de uso.
func NewCreateExampleUseCase(repo port.ExampleRepository, clock sharedport.Clock) *CreateExampleUseCase {
	return &CreateExampleUseCase{repo: repo, clock: clock}
}

// Execute crea el example.
// Fallos: EXAMPLE_NAME_REQUIRED | _TOO_SHORT | _TOO_LONG (400), EXAMPLE_NAME_TAKEN (409).
func (uc *CreateExampleUseCase) Execute(ctx context.Context, cmd dto.CreateExampleCommand) (result.Result[dto.ExampleData], error) {
	name, err := model.NewExampleName(cmd.Name)
	if err != nil {
		return result.FromError[dto.ExampleData](err)
	}
	example, err := model.NewExample(name, cmd.CreatedBy, uc.clock.Now())
	if err != nil {
		return result.FromError[dto.ExampleData](err)
	}
	created, err := uc.repo.Create(ctx, example)
	if err != nil {
		// Un nombre duplicado llega como apperr.ErrExampleNameTaken (409);
		// cualquier otro error es técnico y se propaga (500).
		return result.FromError[dto.ExampleData](fmt.Errorf("crear example: %w", err))
	}
	return result.Ok(mapper.ToExampleData(created)), nil
}
