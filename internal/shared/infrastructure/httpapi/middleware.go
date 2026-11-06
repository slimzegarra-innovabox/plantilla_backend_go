package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"myapp.v1.0.0/internal/shared/application/result"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

// HeaderRequestID es la cabecera de correlación.
const HeaderRequestID = "X-Request-ID"

// Solo se acepta un X-Request-ID del cliente si es corto y seguro: evita inyectar
// saltos de línea o texto arbitrario en los logs.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID asigna un id a la petición (o propaga uno válido del cliente/gateway),
// lo pone en el context y en el logger, y lo devuelve en la cabecera.
func RequestID(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		ctx := logging.WithLogger(c.Request.Context(), base)
		ctx = logging.WithRequestID(ctx, id)
		// El request_id también en el span: desde la respuesta del cliente se llega a la traza.
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("request.id", id))
		c.Request = c.Request.WithContext(ctx)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// AccessLog registra una línea por petición. No registra la query string ni el
// cuerpo (pueden contener datos personales); sí la ruta registrada (sin valores).
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "NO_ROUTE"
		}
		status := c.Writer.Status()
		ctx := c.Request.Context()
		args := []any{
			"method", c.Request.Method,
			"route", route,
			"status", status,
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
			"bytes_out", c.Writer.Size(),
		}
		log := logging.FromContext(ctx)
		switch {
		case status < 400 && strings.HasPrefix(route, "/health/"):
			// Las sondas del orquestador llegan cada pocos segundos: en INFO serían ruido.
			log.DebugContext(ctx, "http_request", args...)
		case status >= 500:
			log.ErrorContext(ctx, "http_request", args...)
		case status >= 400:
			log.WarnContext(ctx, "http_request", args...)
		default:
			log.InfoContext(ctx, "http_request", args...)
		}
	}
}

// SecurityHeaders agrega cabeceras defensivas para una API JSON.
// hsts solo fuera de desarrollo (en local no hay TLS).
func SecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// CORS permite solo los orígenes configurados. La API usa tokens Bearer, no cookies,
// así que nunca envía Access-Control-Allow-Credentials.
func CORS(allowed []string) gin.HandlerFunc {
	const (
		allowMethods  = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
		allowHeaders  = "Authorization, Content-Type, " + HeaderIdempotencyKey + ", " + HeaderRequestID
		exposeHeaders = "Location, Retry-After, " + HeaderIdempotencyReplayed + ", " + HeaderRequestID
	)
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		h := c.Writer.Header()
		h.Add("Vary", "Origin")
		if !slices.Contains(allowed, origin) {
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next() // sin cabeceras CORS: el navegador bloquea la respuesta
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", exposeHeaders)
		if c.Request.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", allowMethods)
			h.Set("Access-Control-Allow-Headers", allowHeaders)
			h.Set("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// BodyLimit limita el tamaño del cuerpo. Si Content-Length ya lo supera responde
// 413 de inmediato; si no, MaxBytesReader corta la lectura y BindJSON responde 413.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			writeNotification(c, http.StatusRequestEntityTooLarge, result.Error(result.Validation, codePayloadTooLarge,
				"El cuerpo de la petición supera el tamaño permitido."))
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// RequestTimeout pone un plazo al context de la petición: las consultas SQL y las
// llamadas externas se cancelan al vencer, y ErrorHandler responde 503.
func RequestTimeout(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
