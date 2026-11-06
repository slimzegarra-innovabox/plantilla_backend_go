package httpapi

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// EngineOptions configura el router y sus middlewares globales.
type EngineOptions struct {
	Logger         *slog.Logger
	HSTS           bool
	TrustedProxies []string
	CORSOrigins    []string
	BodyLimitBytes int64
	RequestTimeout time.Duration
	// RateLimitRPS == 0 desactiva el rate limit.
	RateLimitRPS   float64
	RateLimitBurst int
	// ServiceName activa la instrumentación OpenTelemetry del borde HTTP (trazas y
	// métricas http.server.*). Vacío la omite. Con OTEL_ENABLED=false los proveedores
	// globales son no-op y no cuesta nada.
	ServiceName string
}

// NewEngine crea el router con los middlewares globales en el orden fijo del estándar:
//
//	[OTel] → RequestID → AccessLog → Recovery → SecurityHeaders → CORS → BodyLimit →
//	RateLimit → RequestTimeout → ErrorHandler → (ruta)
//
// AccessLog envuelve a Recovery para registrar el 500 de un panic; ErrorHandler
// es el más interno para ver los errores que registran los handlers.
func NewEngine(opts EngineOptions) (*gin.Engine, error) {
	registerValidators()

	e := gin.New()
	// Solo se confía en X-Forwarded-For de estos proxies. Vacío: no se confía en ninguno.
	if err := e.SetTrustedProxies(opts.TrustedProxies); err != nil {
		return nil, fmt.Errorf("HTTP_TRUSTED_PROXIES inválido: %w", err)
	}
	e.HandleMethodNotAllowed = true
	e.RedirectTrailingSlash = false
	e.RedirectFixedPath = false

	if opts.ServiceName != "" {
		// Primero: el span existe antes que el logger de la petición, así el log de
		// acceso y los errores llevan trace_id. Las sondas de salud no se trazan.
		e.Use(otelgin.Middleware(opts.ServiceName, otelgin.WithGinFilter(func(c *gin.Context) bool {
			return !strings.HasPrefix(c.Request.URL.Path, "/health/")
		})))
	}
	e.Use(
		RequestID(opts.Logger),
		AccessLog(),
		Recovery(),
		SecurityHeaders(opts.HSTS),
		CORS(opts.CORSOrigins),
		BodyLimit(opts.BodyLimitBytes),
	)
	if opts.RateLimitRPS > 0 {
		e.Use(RateLimit(opts.RateLimitRPS, opts.RateLimitBurst))
	}
	if opts.RequestTimeout > 0 {
		e.Use(RequestTimeout(opts.RequestTimeout))
	}
	e.Use(ErrorHandler())

	e.NoRoute(noRoute)
	e.NoMethod(noMethod)
	return e, nil
}
