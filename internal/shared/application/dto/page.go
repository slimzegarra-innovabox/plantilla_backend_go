// Package dto contiene los DTOs genéricos que cualquier módulo usa sin saber
// nada de negocio: paginación e identidad autenticada.
package dto

import "github.com/google/uuid"

const (
	// DefaultPageSize es el tamaño de página cuando el cliente no envía size.
	DefaultPageSize = 20
	// MaxPageSize es el tamaño máximo permitido.
	MaxPageSize = 100
	// MaxOffset es el desplazamiento máximo: más allá, un OFFSET recorre el índice
	// entero en cada petición. Un recorrido profundo se hace con paginación por cursor.
	MaxOffset = 100_000
	// MaxCount es el tope de total_elements. Contar más allá no sirve, porque la página no
	// puede llegar, y cuesta caro: un COUNT(*) recorre la tabla entera en cada listado.
	// Con un millón de filas, eso agota el pool de conexiones a 300 rps y arrastra a todas
	// las demás operaciones (docs/load-baseline.md).
	MaxCount = MaxOffset + MaxPageSize
)

// MaxPage devuelve la última página que se puede pedir con ese size.
func MaxPage(size int) int {
	if size < 1 {
		size = 1
	}
	return MaxOffset/size + 1
}

// PageQuery es la página pedida, numerada desde 1 como en el contrato HTTP.
//
// Es el ÚNICO lugar que conoce la conversión entre el número de página del
// contrato y el desplazamiento SQL. Nadie suma ni resta 1 a mano.
type PageQuery struct {
	page int
	size int
}

// NewPageQuery normaliza la página pedida: page < 1 se toma como 1, y size == 0
// (no enviado) se toma como DefaultPageSize. Un size fuera de [1, MaxPageSize]
// debe rechazarse ANTES (validación de entrada); aquí se acota por seguridad.
func NewPageQuery(page, size int) PageQuery {
	if page < 1 {
		page = 1
	}
	switch {
	case size == 0:
		size = DefaultPageSize
	case size < 1:
		size = 1
	case size > MaxPageSize:
		size = MaxPageSize
	}
	// Por seguridad (la validación de entrada ya lo rechaza): sin desbordar el Offset.
	page = min(page, MaxPage(size))
	return PageQuery{page: page, size: size}
}

// Page devuelve el número de página (desde 1).
func (q PageQuery) Page() int { return q.page }

// Size devuelve el tamaño de página.
func (q PageQuery) Size() int { return q.size }

// Offset devuelve el desplazamiento SQL (desde 0).
func (q PageQuery) Offset() int { return (q.page - 1) * q.size }

// Limit devuelve el LIMIT SQL.
func (q PageQuery) Limit() int { return q.size }

// PageResult es una página de resultados lista para el contrato. TotalElements tiene el
// tope MaxCount, y TotalPages nunca supera la última página que se puede pedir.
type PageResult[T any] struct {
	Items         []T
	Page          int
	Size          int
	TotalElements int64
	TotalPages    int
}

// NewPageResult arma el resultado con la misma numeración que la consulta.
func NewPageResult[T any](q PageQuery, items []T, total int64) PageResult[T] {
	if items == nil {
		items = []T{}
	}
	pages := 0
	if total > 0 {
		pages = min(int((total+int64(q.size)-1)/int64(q.size)), MaxPage(q.size))
	}
	return PageResult[T]{Items: items, Page: q.page, Size: q.size, TotalElements: total, TotalPages: pages}
}

// MapPage convierte los elementos de una página conservando sus metadatos.
func MapPage[T, U any](p PageResult[T], fn func(T) U) PageResult[U] {
	items := make([]U, len(p.Items))
	for i, it := range p.Items {
		items[i] = fn(it)
	}
	return PageResult[U]{Items: items, Page: p.Page, Size: p.Size, TotalElements: p.TotalElements, TotalPages: p.TotalPages}
}

// Principal es la identidad autenticada que hace la petición.
// Proviene del token verificado, nunca del cuerpo del request.
type Principal struct {
	UserUUID uuid.UUID
	// TenantUUID es opcional (uuid.Nil si el sistema no es multiempresa).
	TenantUUID uuid.UUID
	Scopes     []string
}

// HasScope indica si el principal tiene el scope indicado.
func (p Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}
