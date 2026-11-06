// Package logging configura slog y transporta el logger de la petición en el context.
//
// Reglas:
//   - application no registra logs; un error técnico se registra una sola vez, en el borde.
//   - Claves en snake_case.
//   - Nunca se registran cuerpos de petición, tokens, contraseñas, cadenas de conexión
//     ni URLs firmadas.
//   - Un log emitido con un context que lleva un span (…Context(ctx, …)) incluye
//     trace_id y span_id: desde el log se llega a la traza en X-Ray / CloudWatch.
package logging

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// New crea el logger del proceso: texto legible en desarrollo, JSON en el resto.
func New(w io.Writer, level slog.Level, text bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if text {
		return slog.New(traceHandler{slog.NewTextHandler(w, opts)})
	}
	return slog.New(traceHandler{slog.NewJSONHandler(w, opts)})
}

// traceHandler agrega trace_id y span_id cuando el context del registro lleva un
// span válido. Con OTEL_ENABLED=false solo lo hay si la petición trajo un traceparent:
// el log queda correlacionado con la traza del servicio que llamó.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

type ctxKey int

const (
	loggerKey ctxKey = iota
	requestIDKey
)

// WithRequestID guarda el request_id en el context y le agrega el atributo al logger.
func WithRequestID(ctx context.Context, id string) context.Context {
	ctx = context.WithValue(ctx, requestIDKey, id)
	return WithLogger(ctx, FromContext(ctx).With("request_id", id))
}

// RequestID devuelve el request_id del context, o "" si no hay.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithLogger guarda un logger en el context.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// WithAttrs agrega atributos al logger del context (p. ej. user_uuid tras autenticar).
func WithAttrs(ctx context.Context, args ...any) context.Context {
	return WithLogger(ctx, FromContext(ctx).With(args...))
}

// FromContext devuelve el logger del context, o slog.Default() si no hay.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
