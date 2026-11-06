// Package dto contiene las entradas (Command/Query) y salidas (Data) de los casos
// de uso de example.
package dto

import (
	"time"

	"github.com/google/uuid"

	"myapp.v1.0.0/internal/example/application/model"
	shareddto "myapp.v1.0.0/internal/shared/application/dto"
)

// ExampleActor es quien ejecuta una escritura. Lo arma el adaptador de entrada desde el
// Principal autenticado, nunca desde el cuerpo: application no conoce los scopes.
type ExampleActor struct {
	UserUUID uuid.UUID
	IsAdmin  bool
}

// CreateExampleCommand crea un example. CreatedBy viene del Principal, nunca del body.
type CreateExampleCommand struct {
	Name      string
	CreatedBy uuid.UUID
}

// UpdateExampleCommand renombra un example. Version es la que el cliente leyó:
// si otro usuario lo modificó después, el caso de uso responde un conflicto.
type UpdateExampleCommand struct {
	Actor   ExampleActor
	UUID    uuid.UUID
	Name    string
	Version int
}

// ExampleStatusAction es la intención de negocio sobre el estado.
type ExampleStatusAction int

const (
	ActionActivate ExampleStatusAction = iota + 1
	ActionDeactivate
)

// ChangeExampleStatusCommand aplica una intención de negocio sobre el estado.
type ChangeExampleStatusCommand struct {
	Actor  ExampleActor
	UUID   uuid.UUID
	Action ExampleStatusAction
}

// GetExampleQuery obtiene un example por uuid.
type GetExampleQuery struct {
	UUID uuid.UUID
}

// ListExamplesQuery lista examples. Status vacío: todos.
type ListExamplesQuery struct {
	Status string
	// Search filtra por nombre (contiene, sin distinguir mayúsculas). Vacío no filtra.
	Search string
	Sort   shareddto.Sort
	Page   shareddto.PageQuery
}

// ExampleSortFields son los campos lógicos por los que se puede ordenar el listado.
// Es parte del contrato público (query param sort).
var ExampleSortFields = []string{"name", "createdAt", "updatedAt"}

// DefaultExampleSort es el orden del listado cuando el cliente no envía sort.
var DefaultExampleSort = shareddto.Sort{{Field: "createdAt", Desc: true}}

// DeleteExampleCommand elimina un example.
type DeleteExampleCommand struct {
	Actor ExampleActor
	UUID  uuid.UUID
}

// ExampleData es la salida de los casos de uso de example.
type ExampleData struct {
	UUID      uuid.UUID
	Name      string
	Status    model.ExampleStatus
	Version   int
	CreatedBy uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ExampleStatusChanged es el evento de que un example cambió de estado. Lo consumen
// otros procesos (worker): se publica por el outbox después del commit.
type ExampleStatusChanged struct {
	ExampleUUID uuid.UUID
	Status      string
	Version     int
	OccurredAt  time.Time
}
