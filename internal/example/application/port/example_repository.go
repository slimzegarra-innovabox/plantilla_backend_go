// Package port contiene las interfaces que los casos de uso de example necesitan
// para hablar con el exterior.
package port

import (
	"context"

	"github.com/google/uuid"

	"myapp.v1.0.0/internal/example/application/model"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
)

// ExampleFilter son los criterios del listado. Un campo vacío no filtra.
type ExampleFilter struct {
	Status model.ExampleStatus
	// Search: el nombre contiene el texto, sin distinguir mayúsculas. Los comodines
	// del texto (%, _) son literales.
	Search string
}

// ExampleRepository persiste examples. El módulo example es el dueño de escritura
// de la tabla examples.
//
// Los métodos de escritura devuelven el estado PERSISTIDO (id, versión nueva).
type ExampleRepository interface {
	// Create inserta un example nuevo.
	// Fallos anticipados: apperr.ErrExampleNameTaken.
	Create(ctx context.Context, example *model.Example) (*model.Example, error)

	// FindByUUID busca por uuid. Si no existe devuelve (nil, false, nil).
	FindByUUID(ctx context.Context, id uuid.UUID) (*model.Example, bool, error)

	// List devuelve una página. sort usa los campos de dto.ExampleSortFields; vacío
	// es creación descendente. Siempre termina en el id descendente (orden estable).
	List(ctx context.Context, filter ExampleFilter, sort shareddto.Sort, page shareddto.PageQuery) (shareddto.PageResult[*model.Example], error)

	// Update guarda nombre y estado solo si la versión persistida sigue siendo
	// example.Version(), e incrementa la versión.
	// Fallos anticipados: apperr.ErrExampleNotFound, apperr.ErrExampleVersionConflict,
	// apperr.ErrExampleNameTaken.
	Update(ctx context.Context, example *model.Example) (*model.Example, error)

	// Delete elimina el example solo si la versión persistida sigue siendo
	// example.Version() (nadie lo cambió desde que se leyó).
	// Fallos anticipados: apperr.ErrExampleNotFound, apperr.ErrExampleVersionConflict.
	Delete(ctx context.Context, example *model.Example) error
}
