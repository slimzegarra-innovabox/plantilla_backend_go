package dto

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxSortFields es el máximo de criterios de un ordenamiento.
const MaxSortFields = 3

// SortField es un criterio de orden sobre un campo LÓGICO del contrato (p. ej.
// "createdAt"), nunca una columna: cada adapter de persistencia traduce los campos
// que permite a columnas, así el cliente no puede ordenar por algo arbitrario.
type SortField struct {
	Field string
	Desc  bool
}

// Sort es un ordenamiento: criterios en orden de prioridad.
type Sort []SortField

// ErrInvalidSort indica un ordenamiento mal formado o con un campo no permitido.
var ErrInvalidSort = errors.New("ordenamiento inválido")

// ParseSort lee "name,-createdAt" (un "-" delante es descendente) y valida cada campo
// contra allowed. Vacío devuelve def. Rechaza campos repetidos y más de MaxSortFields.
func ParseSort(raw string, allowed []string, def Sort) (Sort, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > MaxSortFields {
		return nil, fmt.Errorf("%w: como máximo %d campos", ErrInvalidSort, MaxSortFields)
	}
	out := make(Sort, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		f := SortField{Field: strings.TrimPrefix(p, "-"), Desc: strings.HasPrefix(p, "-")}
		switch {
		case f.Field == "":
			return nil, fmt.Errorf("%w: campo vacío", ErrInvalidSort)
		case !slices.Contains(allowed, f.Field):
			return nil, fmt.Errorf("%w: %q no se puede ordenar (permitidos: %s)", ErrInvalidSort, f.Field, strings.Join(allowed, ", "))
		case seen[f.Field]:
			return nil, fmt.Errorf("%w: %q repetido", ErrInvalidSort, f.Field)
		}
		seen[f.Field] = true
		out = append(out, f)
	}
	return out, nil
}
