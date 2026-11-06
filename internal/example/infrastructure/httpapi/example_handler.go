// Package httpapi es el adaptador HTTP del módulo example: traduce peticiones a
// Command/Query, llama a los casos de uso y traduce el resultado al contrato.
// Solo conoce casos de uso y DTOs (regla D5).
package httpapi

import (
	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/usecase"
	sharedhttp "myapp.v1.0.0/internal/shared/infrastructure/httpapi"
)

// Scopes del módulo, declarados en las rutas.
const (
	ScopeRead  = "example:read"
	ScopeWrite = "example:write"
	// ScopeAdmin permite modificar examples de otros usuarios.
	ScopeAdmin = "example:admin"
)

// basePath es el prefijo de las rutas del módulo dentro de /api/v1.
const basePath = "/examples"

// ExampleHandler agrupa las acciones HTTP sobre examples.
type ExampleHandler struct {
	create       *usecase.CreateExampleUseCase
	get          *usecase.GetExampleUseCase
	list         *usecase.ListExamplesUseCase
	update       *usecase.UpdateExampleUseCase
	changeStatus *usecase.ChangeExampleStatusUseCase
	remove       *usecase.DeleteExampleUseCase
}

// NewExampleHandler recibe los casos de uso concretos ya armados por module.go.
func NewExampleHandler(
	create *usecase.CreateExampleUseCase,
	get *usecase.GetExampleUseCase,
	list *usecase.ListExamplesUseCase,
	update *usecase.UpdateExampleUseCase,
	changeStatus *usecase.ChangeExampleStatusUseCase,
	remove *usecase.DeleteExampleUseCase,
) *ExampleHandler {
	return &ExampleHandler{create: create, get: get, list: list, update: update, changeStatus: changeStatus, remove: remove}
}

// actor arma quién ejecuta la escritura desde el token, nunca desde el cuerpo.
func actor(c *gin.Context) dto.ExampleActor {
	p := sharedhttp.MustPrincipal(c)
	return dto.ExampleActor{UserUUID: p.UserUUID, IsAdmin: p.HasScope(ScopeAdmin)}
}
