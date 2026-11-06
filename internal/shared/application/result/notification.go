// Package result define cómo un caso de uso comunica su resultado: el dato en
// caso de éxito o las notificaciones de negocio en caso de fallo.
//
// No contiene vocabulario HTTP. La traducción de Category a un código de estado
// vive exclusivamente en infraestructura (sharedhttp).
package result

// Category clasifica una notificación por su significado de negocio.
type Category string

const (
	// Validation: la entrada no cumple el formato o una invariante.
	Validation Category = "VALIDATION"
	// Unauthorized: no hay una identidad válida.
	Unauthorized Category = "UNAUTHORIZED"
	// Forbidden: la identidad no tiene permiso para la acción.
	Forbidden Category = "FORBIDDEN"
	// NotFound: el recurso referenciado no existe.
	NotFound Category = "NOT_FOUND"
	// Conflict: el estado actual impide la acción (duplicado, versión desfasada).
	Conflict Category = "CONFLICT"
	// BusinessRule: la entrada es válida pero una regla de negocio la rechaza.
	BusinessRule Category = "BUSINESS_RULE"
	// Technical: fallo no anticipado. Solo lo usa el borde de infraestructura.
	Technical Category = "TECHNICAL"
)

// Severity indica la gravedad de una notificación.
type Severity string

const (
	SeverityError   Severity = "ERROR"
	SeverityWarning Severity = "WARNING"
	SeverityInfo    Severity = "INFO"
)

// Notification es un mensaje de negocio con un código estable.
//
// Code es el contrato que consume el cliente: UPPER_SNAKE_CASE, en inglés y
// con el prefijo del módulo (EXAMPLE_NAME_TOO_SHORT). No se renombra.
// Message es el texto para el usuario y puede cambiar.
type Notification struct {
	Category Category
	Code     string
	Message  string
	Severity Severity
	// Field es opcional: el campo de entrada (snake_case) que origina la notificación.
	Field string
}

// Error crea una notificación de severidad ERROR.
func Error(category Category, code, message string) Notification {
	return Notification{Category: category, Code: code, Message: message, Severity: SeverityError}
}

// Warning crea una notificación de severidad WARNING, útil para acompañar un éxito.
func Warning(category Category, code, message string) Notification {
	return Notification{Category: category, Code: code, Message: message, Severity: SeverityWarning}
}

// Info crea una notificación de severidad INFO, útil para acompañar un éxito.
func Info(category Category, code, message string) Notification {
	return Notification{Category: category, Code: code, Message: message, Severity: SeverityInfo}
}

// WithField devuelve una copia de la notificación asociada a un campo de entrada.
func (n Notification) WithField(field string) Notification {
	n.Field = field
	return n
}
