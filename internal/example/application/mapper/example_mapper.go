// Package mapper convierte entre model y dto del módulo example. Funciones puras.
package mapper

import (
	"myapp.v1.0.0/internal/example/application/dto"
	"myapp.v1.0.0/internal/example/application/model"
)

// ToExampleData convierte el model en la salida de los casos de uso.
func ToExampleData(e *model.Example) dto.ExampleData {
	return dto.ExampleData{
		UUID:      e.UUID(),
		Name:      e.Name().String(),
		Status:    e.Status(),
		Version:   e.Version(),
		CreatedBy: e.CreatedBy(),
		CreatedAt: e.CreatedAt(),
		UpdatedAt: e.UpdatedAt(),
	}
}

// ToExampleStatusChanged arma el evento de cambio de estado desde el estado persistido.
func ToExampleStatusChanged(e *model.Example) dto.ExampleStatusChanged {
	return dto.ExampleStatusChanged{
		ExampleUUID: e.UUID(),
		Status:      string(e.Status()),
		Version:     e.Version(),
		OccurredAt:  e.UpdatedAt(),
	}
}
