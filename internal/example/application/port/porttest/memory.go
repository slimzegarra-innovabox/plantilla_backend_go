// Package porttest contiene dobles de prueba de los puertos de example y la suite
// de contrato que todo ExampleRepository debe cumplir.
//
// La misma suite corre contra el doble en memoria (pruebas unitarias) y contra el
// adapter de PostgreSQL (pruebas de integración): así el doble no puede desviarse
// del comportamiento real sin que una prueba falle.
//
// Solo depende de la biblioteca estándar (regla D1).
package porttest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"myapp.v1.0.0/internal/example/application/apperr"
	"myapp.v1.0.0/internal/example/application/model"
	"myapp.v1.0.0/internal/example/application/port"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
)

// InMemoryExampleRepository implementa port.ExampleRepository en memoria, con la
// misma semántica que el adapter real (nombre único sin distinguir mayúsculas,
// concurrencia optimista, orden del listado).
type InMemoryExampleRepository struct {
	mu     sync.Mutex
	rows   map[uuid.UUID]*model.Example
	nextID int64

	// Err, si no es nil, lo devuelven todas las operaciones: simula un fallo técnico.
	Err error
	// Calls cuenta las llamadas por método, para verificar la orquestación.
	Calls map[string]int
}

var _ port.ExampleRepository = (*InMemoryExampleRepository)(nil)

// NewInMemoryExampleRepository crea un repositorio vacío.
func NewInMemoryExampleRepository() *InMemoryExampleRepository {
	return &InMemoryExampleRepository{rows: map[uuid.UUID]*model.Example{}, Calls: map[string]int{}}
}

// Create implementa port.ExampleRepository.
func (r *InMemoryExampleRepository) Create(_ context.Context, e *model.Example) (*model.Example, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls["Create"]++
	if r.Err != nil {
		return nil, r.Err
	}
	if r.nameTaken(e.Name(), uuid.Nil) {
		return nil, apperr.ErrExampleNameTaken
	}
	r.nextID++
	stored := model.RestoreExample(r.nextID, e.UUID(), e.Name(), e.Status(), 1, e.CreatedBy(), e.CreatedAt(), e.UpdatedAt())
	r.rows[e.UUID()] = stored
	return clone(stored), nil
}

// FindByUUID implementa port.ExampleRepository.
func (r *InMemoryExampleRepository) FindByUUID(_ context.Context, id uuid.UUID) (*model.Example, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls["FindByUUID"]++
	if r.Err != nil {
		return nil, false, r.Err
	}
	e, ok := r.rows[id]
	if !ok {
		return nil, false, nil
	}
	return clone(e), true, nil
}

// List implementa port.ExampleRepository con la misma semántica que el adapter:
// búsqueda literal sin distinguir mayúsculas, orden por campos lógicos y desempate
// por id descendente.
func (r *InMemoryExampleRepository) List(_ context.Context, f port.ExampleFilter, sort shareddto.Sort, q shareddto.PageQuery) (shareddto.PageResult[*model.Example], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls["List"]++
	if r.Err != nil {
		return shareddto.PageResult[*model.Example]{}, r.Err
	}
	if len(sort) == 0 {
		sort = shareddto.Sort{{Field: "createdAt", Desc: true}}
	}
	search := strings.ToLower(f.Search)
	var all []*model.Example
	for _, e := range r.rows {
		if f.Status != "" && e.Status() != f.Status {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(e.Name().String()), search) {
			continue
		}
		all = append(all, e)
	}
	for _, s := range sort {
		if !slices.Contains([]string{"name", "createdAt", "updatedAt"}, s.Field) {
			return shareddto.PageResult[*model.Example]{}, fmt.Errorf("campo de orden no soportado: %s", s.Field)
		}
	}
	slices.SortFunc(all, func(a, b *model.Example) int {
		for _, s := range sort {
			var c int
			switch s.Field {
			case "name":
				c = strings.Compare(strings.ToLower(a.Name().String()), strings.ToLower(b.Name().String()))
			case "createdAt":
				c = a.CreatedAt().Compare(b.CreatedAt())
			case "updatedAt":
				c = a.UpdatedAt().Compare(b.UpdatedAt())
			}
			if s.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return cmp.Compare(b.ID(), a.ID())
	})
	total := min(int64(len(all)), shareddto.MaxCount)
	from := min(q.Offset(), len(all))
	to := min(from+q.Limit(), len(all))
	items := make([]*model.Example, 0, to-from)
	for _, e := range all[from:to] {
		items = append(items, clone(e))
	}
	return shareddto.NewPageResult(q, items, total), nil
}

// Delete implementa port.ExampleRepository.
func (r *InMemoryExampleRepository) Delete(_ context.Context, e *model.Example) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls["Delete"]++
	if r.Err != nil {
		return r.Err
	}
	current, ok := r.rows[e.UUID()]
	switch {
	case !ok:
		return apperr.ErrExampleNotFound
	case current.Version() != e.Version():
		return apperr.ErrExampleVersionConflict
	}
	delete(r.rows, e.UUID())
	return nil
}

// Update implementa port.ExampleRepository.
func (r *InMemoryExampleRepository) Update(_ context.Context, e *model.Example) (*model.Example, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls["Update"]++
	if r.Err != nil {
		return nil, r.Err
	}
	current, ok := r.rows[e.UUID()]
	if !ok {
		return nil, apperr.ErrExampleNotFound
	}
	if current.Version() != e.Version() {
		return nil, apperr.ErrExampleVersionConflict
	}
	if r.nameTaken(e.Name(), e.UUID()) {
		return nil, apperr.ErrExampleNameTaken
	}
	stored := model.RestoreExample(current.ID(), e.UUID(), e.Name(), e.Status(), current.Version()+1,
		current.CreatedBy(), current.CreatedAt(), e.UpdatedAt())
	r.rows[e.UUID()] = stored
	return clone(stored), nil
}

// Seed inserta un example tal cual (con su estado), para preparar escenarios.
func (r *InMemoryExampleRepository) Seed(e *model.Example) *model.Example {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	stored := model.RestoreExample(r.nextID, e.UUID(), e.Name(), e.Status(), e.Version(), e.CreatedBy(), e.CreatedAt(), e.UpdatedAt())
	r.rows[e.UUID()] = stored
	return clone(stored)
}

func (r *InMemoryExampleRepository) nameTaken(name model.ExampleName, except uuid.UUID) bool {
	for id, e := range r.rows {
		if id != except && e.Name().Equals(name) {
			return true
		}
	}
	return false
}

// clone evita que el llamador modifique el estado guardado por referencia.
func clone(e *model.Example) *model.Example {
	return model.RestoreExample(e.ID(), e.UUID(), e.Name(), e.Status(), e.Version(), e.CreatedBy(), e.CreatedAt(), e.UpdatedAt())
}
