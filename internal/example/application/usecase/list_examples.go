package usecase

import (
	"context"
	"fmt"
	"strings"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/mapper"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	"myapp.v1.0.0/internal/shared/application/result"
)

// ListExamplesUseCase lista examples paginados, con filtro opcional por estado.
type ListExamplesUseCase struct {
	repo port.ExampleRepository
}

// NewListExamplesUseCase crea el caso de uso.
func NewListExamplesUseCase(repo port.ExampleRepository) *ListExamplesUseCase {
	return &ListExamplesUseCase{repo: repo}
}

// Execute lista la página pedida. Fallos: EXAMPLE_STATUS_INVALID (400).
func (uc *ListExamplesUseCase) Execute(ctx context.Context, q dto.ListExamplesQuery) (result.Result[shareddto.PageResult[dto.ExampleData]], error) {
	var filter port.ExampleFilter
	if q.Status != "" {
		status, err := model.ParseExampleStatus(q.Status)
		if err != nil {
			// El código viene del usuario: es un fallo de validación anticipado, no técnico.
			return result.FromError[shareddto.PageResult[dto.ExampleData]](apperr.ErrExampleStatusInvalid)
		}
		filter.Status = status
	}

	filter.Search = strings.TrimSpace(q.Search)
	sort := q.Sort
	if len(sort) == 0 {
		sort = dto.DefaultExampleSort
	}

	page, err := uc.repo.List(ctx, filter, sort, q.Page)
	if err != nil {
		return result.FromError[shareddto.PageResult[dto.ExampleData]](fmt.Errorf("listar examples: %w", err))
	}
	return result.Ok(shareddto.MapPage(page, mapper.ToExampleData)), nil
}
