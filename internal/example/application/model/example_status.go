package model

import (
	"fmt"
	"slices"

	"myapp.v1.0.0/internal/example/application/apperr"
)

// ExampleStatus es el estado del ciclo de vida de un example.
//
//	pending ──► active ──► inactive
//	              ▲            │
//	              └────────────┘ (reactivar)
//
// El valor string es también el código persistido y el valor del contrato JSON;
// cambiarlo requiere una migración y una versión nueva de la API.
type ExampleStatus string

const (
	ExampleStatusPending  ExampleStatus = "pending"
	ExampleStatusActive   ExampleStatus = "active"
	ExampleStatusInactive ExampleStatus = "inactive"
)

var exampleTransitions = map[ExampleStatus][]ExampleStatus{
	ExampleStatusPending:  {ExampleStatusActive},
	ExampleStatusActive:   {ExampleStatusInactive},
	ExampleStatusInactive: {ExampleStatusActive},
}

// ParseExampleStatus convierte un código en estado de forma ESTRICTA: un código
// desconocido es un error, nunca un valor por defecto.
//
// Devuelve un error técnico (no un Failure): quien lo llama decide si el código
// venía del usuario (y lo traduce a apperr.ErrExampleStatusInvalid) o de la base
// de datos (dato corrupto → 500).
func ParseExampleStatus(code string) (ExampleStatus, error) {
	s := ExampleStatus(code)
	if _, ok := exampleTransitions[s]; !ok {
		return "", fmt.Errorf("estado de example desconocido: %q", code)
	}
	return s, nil
}

// CanTransitionTo indica si la máquina de estados permite pasar a next.
func (s ExampleStatus) CanTransitionTo(next ExampleStatus) bool {
	return slices.Contains(exampleTransitions[s], next)
}

func (s ExampleStatus) transitionTo(next ExampleStatus) error {
	if !s.CanTransitionTo(next) {
		return apperr.ErrExampleInvalidTransition
	}
	return nil
}
