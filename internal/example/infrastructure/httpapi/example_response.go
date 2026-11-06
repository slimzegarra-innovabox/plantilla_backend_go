package httpapi

import (
	"time"

	"github.com/google/uuid"

	"myapp.v1.0.0/internal/example/application/dto"
)

// ExampleResponse es la representación pública de un example. Contrato JSON en
// snake_case, declarado explícitamente en cada campo.
type ExampleResponse struct {
	UUID      uuid.UUID `json:"uuid"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	CreatedBy uuid.UUID `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toExampleResponse(d dto.ExampleData) ExampleResponse {
	return ExampleResponse{
		UUID: d.UUID,
		Name: d.Name,
		// Contrato público del estado: el código del enum (pending | active | inactive),
		// documentado en OpenAPI. Cambiarlo es un cambio incompatible de la API.
		Status:    string(d.Status),
		Version:   d.Version,
		CreatedBy: d.CreatedBy,
		CreatedAt: d.CreatedAt.UTC(),
		UpdatedAt: d.UpdatedAt.UTC(),
	}
}
