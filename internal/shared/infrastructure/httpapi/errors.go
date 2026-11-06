package httpapi

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"myapp.v1.0.0/internal/shared/application/result"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
)

// Notificaciones técnicas que genera la infraestructura (§8.4). Nunca llevan
// detalles internos: el detalle va al log, correlacionado por request_id.
var (
	notifUnexpected = result.Error(result.Technical, "UNEXPECTED_ERROR",
		"Ocurrió un error inesperado. Intente nuevamente más tarde.")
	notifTimeout = result.Error(result.Technical, "SERVICE_TIMEOUT",
		"El servicio tardó demasiado en responder. Intente nuevamente.")
	notifDataConflict = result.Error(result.Conflict, "DATA_CONFLICT",
		"Los datos entran en conflicto con información ya registrada.")
	notifRouteNotFound = result.Error(result.NotFound, "ROUTE_NOT_FOUND",
		"La ruta solicitada no existe.")
	notifMethodNotAllowed = result.Error(result.Validation, "METHOD_NOT_ALLOWED",
		"El método HTTP no está permitido para esta ruta.")
)

// statusClientClosedRequest es la convención (nginx) para «el cliente canceló».
const statusClientClosedRequest = 499

// ErrorHandler convierte en respuesta el error técnico que un handler registró
// con c.Error(err). Es el único lugar donde se registra un error técnico de una
// petición HTTP: application nunca registra logs.
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if len(c.Errors) == 0 || c.Writer.Written() {
			return
		}
		err := c.Errors.Last().Err
		ctx := c.Request.Context()
		log := logging.FromContext(ctx)

		switch {
		case isFailure(err):
			// Un handler no debería llegar aquí con un Failure (debe usar WriteFailure),
			// pero si ocurre se responde como lo que es: un fallo de negocio.
			f, _ := result.AsFailure(err)
			WriteFailure(c, []result.Notification{f.Notification()})
		case errors.Is(err, context.Canceled):
			log.InfoContext(ctx, "petición cancelada por el cliente", "error", err)
			c.AbortWithStatus(statusClientClosedRequest)
		case errors.Is(err, context.DeadlineExceeded), persistence.IsTimeout(err):
			log.ErrorContext(ctx, "petición excedió el plazo", "error", err)
			writeNotification(c, http.StatusServiceUnavailable, notifTimeout)
		case persistence.IsDataConflict(err):
			// Red de seguridad: cada caso es una validación que falta en un caso de uso o adapter.
			log.ErrorContext(ctx, "violación de constraint no traducida por el adapter", "error", err)
			writeNotification(c, http.StatusConflict, notifDataConflict)
		default:
			log.ErrorContext(ctx, "error técnico no controlado", "error", err)
			writeNotification(c, http.StatusInternalServerError, notifUnexpected)
		}
	}
}

func isFailure(err error) bool {
	_, ok := result.AsFailure(err)
	return ok
}

// Recovery convierte un panic en 500, registrado con stack trace.
// Re-lanza http.ErrAbortHandler, que net/http usa para cortar la conexión a propósito.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // comparación por identidad, documentada en net/http
				panic(rec)
			}
			ctx := c.Request.Context()
			logging.FromContext(ctx).ErrorContext(ctx, "panic recuperado", "panic", rec, "stack", string(debug.Stack()))
			if !c.Writer.Written() {
				writeNotification(c, http.StatusInternalServerError, notifUnexpected)
			}
			c.Abort()
		}()
		c.Next()
	}
}

func noRoute(c *gin.Context) { writeNotification(c, http.StatusNotFound, notifRouteNotFound) }

func noMethod(c *gin.Context) {
	writeNotification(c, http.StatusMethodNotAllowed, notifMethodNotAllowed)
}
