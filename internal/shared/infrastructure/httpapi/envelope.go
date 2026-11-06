// Package httpapi es la infraestructura HTTP común: el sobre de respuesta, la
// única tabla categoría → estado HTTP, los middlewares y los helpers de binding.
//
// Los módulos lo importan como sharedhttp.
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	shareddto "myapp.v1.0.0/internal/shared/application/dto"
	"myapp.v1.0.0/internal/shared/application/result"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

// Envelope es el sobre de TODAS las respuestas JSON de la API (§9.1 del estándar).
type Envelope struct {
	Data          any                `json:"data"`
	Page          *PageMeta          `json:"page"`
	Notifications []NotificationJSON `json:"notifications"`
	RequestID     string             `json:"request_id"`
}

// PageMeta son los metadatos de un listado paginado. page empieza en 1.
type PageMeta struct {
	Page          int   `json:"page"`
	Size          int   `json:"size"`
	TotalElements int64 `json:"total_elements"`
	TotalPages    int   `json:"total_pages"`
}

// NotificationJSON es la representación pública de una notificación.
type NotificationJSON struct {
	Category string `json:"category"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Field    string `json:"field,omitempty"`
}

// WriteData responde un éxito con el dato y las notificaciones de aviso, si las hay.
func WriteData(c *gin.Context, status int, data any, notes []result.Notification) {
	write(c, status, Envelope{Data: data, Notifications: toJSON(notes)})
}

// WritePage responde un listado paginado.
func WritePage[T any](c *gin.Context, page shareddto.PageResult[T], notes []result.Notification) {
	write(c, http.StatusOK, Envelope{
		Data: page.Items,
		Page: &PageMeta{
			Page:          page.Page,
			Size:          page.Size,
			TotalElements: page.TotalElements,
			TotalPages:    page.TotalPages,
		},
		Notifications: toJSON(notes),
	})
}

// WriteFailure responde un fallo de negocio. El estado HTTP sale de la categoría
// de la primera notificación de severidad ERROR.
func WriteFailure(c *gin.Context, notes []result.Notification) {
	write(c, StatusForNotifications(notes), Envelope{Notifications: toJSON(notes)})
	c.Abort()
}

// Handle resuelve el par (Result, error) que devuelve un caso de uso:
//
//   - error técnico   → lo registra con c.Error (ErrorHandler responde 500) y devuelve false;
//   - fallo de negocio → responde con WriteFailure y devuelve false;
//   - éxito           → no escribe nada y devuelve true: el handler arma su respuesta.
//
// Uso: if !sharedhttp.Handle(c, res, err) { return }
func Handle[T any](c *gin.Context, res result.Result[T], err error) bool {
	if err != nil {
		_ = c.Error(err)
		return false
	}
	if !res.IsSuccess() {
		WriteFailure(c, res.Notifications())
		return false
	}
	return true
}

// writeNotification responde un único error generado por la infraestructura.
func writeNotification(c *gin.Context, status int, n result.Notification) {
	write(c, status, Envelope{Notifications: toJSON([]result.Notification{n})})
	c.Abort()
}

func write(c *gin.Context, status int, env Envelope) {
	env.RequestID = logging.RequestID(c.Request.Context())
	if env.Notifications == nil {
		env.Notifications = []NotificationJSON{}
	}
	c.JSON(status, env)
}

func toJSON(notes []result.Notification) []NotificationJSON {
	out := make([]NotificationJSON, len(notes))
	for i, n := range notes {
		out[i] = NotificationJSON{
			Category: string(n.Category),
			Code:     n.Code,
			Message:  n.Message,
			Severity: string(n.Severity),
			Field:    n.Field,
		}
	}
	return out
}
