// Package apperr es el catálogo de fallos de negocio anticipados del módulo example.
//
// El code es el contrato con el cliente: estable, UPPER_SNAKE_CASE y con prefijo
// EXAMPLE_. Cada fallo tiene al menos una prueba que lo provoca.
package apperr

import "myapp.v1.0.0/internal/shared/application/result"

var (
	// ErrExampleNameRequired: el nombre está vacío (o solo tiene espacios).
	ErrExampleNameRequired = result.NewFailure(result.Validation,
		"EXAMPLE_NAME_REQUIRED", "El nombre es obligatorio.")
	// ErrExampleNameTooShort: el nombre tiene menos de 2 caracteres.
	ErrExampleNameTooShort = result.NewFailure(result.Validation,
		"EXAMPLE_NAME_TOO_SHORT", "El nombre debe tener al menos 2 caracteres.")
	// ErrExampleNameTooLong: el nombre tiene más de 150 caracteres.
	ErrExampleNameTooLong = result.NewFailure(result.Validation,
		"EXAMPLE_NAME_TOO_LONG", "El nombre debe tener como máximo 150 caracteres.")
	// ErrExampleStatusInvalid: el estado pedido como filtro no existe.
	ErrExampleStatusInvalid = result.NewFailure(result.Validation,
		"EXAMPLE_STATUS_INVALID", "El estado debe ser pending, active o inactive.")

	// ErrExampleNotFound: no existe un example con ese uuid.
	ErrExampleNotFound = result.NewFailure(result.NotFound,
		"EXAMPLE_NOT_FOUND", "El ejemplo no existe.")

	// ErrExampleForbidden: el example es de otro usuario y quien actúa no es administrador.
	ErrExampleForbidden = result.NewFailure(result.Forbidden,
		"EXAMPLE_FORBIDDEN", "Solo quien creó el ejemplo o un administrador puede modificarlo.")

	// ErrExampleNameTaken: ya existe un example con ese nombre (sin distinguir mayúsculas).
	ErrExampleNameTaken = result.NewFailure(result.Conflict,
		"EXAMPLE_NAME_TAKEN", "Ya existe un ejemplo con ese nombre.")
	// ErrExampleVersionConflict: otro usuario modificó el example desde que se leyó.
	ErrExampleVersionConflict = result.NewFailure(result.Conflict,
		"EXAMPLE_VERSION_CONFLICT", "El ejemplo fue modificado por otro usuario. Recargue e intente nuevamente.")

	// ErrExampleInvalidTransition: la máquina de estados no permite el cambio pedido.
	ErrExampleInvalidTransition = result.NewFailure(result.BusinessRule,
		"EXAMPLE_INVALID_TRANSITION", "El ejemplo no puede pasar a ese estado desde su estado actual.")

	// ErrExampleDeleteActive: un example activo no se elimina; primero se desactiva.
	ErrExampleDeleteActive = result.NewFailure(result.BusinessRule,
		"EXAMPLE_DELETE_ACTIVE", "No se puede eliminar un ejemplo activo. Desactívelo primero.")
)
