package httpapi

import (
	"net/http"

	"myapp.v1.0.0/internal/shared/application/result"
)

// StatusFor es la ÚNICA tabla categoría → estado HTTP del proyecto (§8.3).
// Una categoría nueva se agrega aquí y en su prueba en el mismo cambio.
func StatusFor(category result.Category) int {
	switch category {
	case result.Validation:
		return http.StatusBadRequest
	case result.Unauthorized:
		return http.StatusUnauthorized
	case result.Forbidden:
		return http.StatusForbidden
	case result.NotFound:
		return http.StatusNotFound
	case result.Conflict:
		return http.StatusConflict
	case result.BusinessRule:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// StatusForNotifications decide el estado de un resultado fallido: manda la
// primera notificación de severidad ERROR. Sin ninguna, es un error de programación → 500.
func StatusForNotifications(notes []result.Notification) int {
	for _, n := range notes {
		if n.Severity == result.SeverityError {
			return StatusFor(n.Category)
		}
	}
	return http.StatusInternalServerError
}
