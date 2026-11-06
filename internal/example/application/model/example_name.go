// Package model contiene los objetos de negocio del módulo example.
package model

import (
	"strings"
	"unicode/utf8"

	"myapp.v1.0.0/internal/example/application/apperr"
)

const (
	exampleNameMinLength = 2
	exampleNameMaxLength = 150
)

// ExampleName es el nombre de un example: entre 2 y 150 caracteres, sin espacios
// en los extremos. El valor cero no es un nombre válido.
type ExampleName struct {
	value string
}

// NewExampleName normaliza (recorta espacios) y valida el nombre.
func NewExampleName(raw string) (ExampleName, error) {
	v := strings.TrimSpace(raw)
	switch n := utf8.RuneCountInString(v); {
	case n == 0:
		return ExampleName{}, apperr.ErrExampleNameRequired
	case n < exampleNameMinLength:
		return ExampleName{}, apperr.ErrExampleNameTooShort
	case n > exampleNameMaxLength:
		return ExampleName{}, apperr.ErrExampleNameTooLong
	}
	return ExampleName{value: v}, nil
}

// String devuelve el nombre.
func (n ExampleName) String() string { return n.value }

// Equals compara dos nombres sin distinguir mayúsculas (igual que el índice único).
func (n ExampleName) Equals(other ExampleName) bool { return strings.EqualFold(n.value, other.value) }
