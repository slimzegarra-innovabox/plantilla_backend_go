package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	"myapp.v1.0.0/internal/shared/infrastructure/telemetry"
)

// restore devuelve los proveedores globales al terminar la prueba.
func restore(t *testing.T) {
	t.Helper()
	tp, mp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		otel.SetTextMapPropagator(prop)
	})
}

// otlpReceiver cuenta lo que llega por OTLP/HTTP.
func otlpReceiver(t *testing.T) (url string, hits func(path string) int) {
	t.Helper()
	var mu sync.Mutex
	got := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return got[path]
	}
}

func TestDisabledExportsNothingButPropagates(t *testing.T) {
	restore(t)
	url, hits := otlpReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)
	ctx := context.Background()

	shutdown, err := telemetry.Setup(ctx, config.OTelConfig{Enabled: false, ServiceName: "svc"}, "test", "1")
	require.NoError(t, err)
	_, span := otel.Tracer("t").Start(ctx, "x")
	span.End()
	require.NoError(t, shutdown(ctx))

	assert.Zero(t, hits("/v1/traces"))
	assert.Contains(t, otel.GetTextMapPropagator().Fields(), "traceparent", "un traceparent entrante se sigue propagando")
}

// Con OTel activo, los proveedores se arman (el recurso une sus esquemas sin conflicto),
// los IDs tienen el formato de X-Ray y Shutdown vacía lo pendiente.
func TestEnabledExportsTracesAndMetricsOnShutdown(t *testing.T) {
	restore(t)
	url, hits := otlpReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)
	ctx := context.Background()
	before := time.Now().Unix()

	shutdown, err := telemetry.Setup(ctx, config.OTelConfig{Enabled: true, ServiceName: "svc", Metrics: true}, "test", "1")
	require.NoError(t, err)

	_, span := otel.Tracer("t").Start(ctx, "x")
	traceID := span.SpanContext().TraceID()
	span.End()
	counter, err := otel.Meter("t").Int64Counter("c", metric.WithDescription("d"))
	require.NoError(t, err)
	counter.Add(ctx, 1)

	var epoch int64
	for _, b := range traceID[:4] {
		epoch = epoch<<8 | int64(b)
	}
	assert.InDelta(t, before, epoch, 5, "los primeros 4 bytes del trace_id son la marca de tiempo (formato X-Ray)")

	require.NoError(t, shutdown(ctx))
	assert.Positive(t, hits("/v1/traces"), "Shutdown vacía las trazas")
	assert.Positive(t, hits("/v1/metrics"), "Shutdown vacía las métricas")
}

func TestMetricsCanBeDisabled(t *testing.T) {
	restore(t)
	url, hits := otlpReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)
	ctx := context.Background()

	shutdown, err := telemetry.Setup(ctx, config.OTelConfig{Enabled: true, ServiceName: "svc", Metrics: false}, "test", "1")
	require.NoError(t, err)
	_, span := otel.Tracer("t").Start(ctx, "x")
	span.End()
	require.NoError(t, shutdown(ctx))

	assert.Positive(t, hits("/v1/traces"))
	assert.Zero(t, hits("/v1/metrics"))
}
