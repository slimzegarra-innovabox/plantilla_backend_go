//go:build integration

package asynq_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
	sharedasynq "myapp.v1.0.0/internal/shared/infrastructure/external/asynq"
	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

// Requiere un Redis real: TEST_REDIS_ADDR=localhost:6379 (p. ej. el de develop.yml).
func redisConfig(t *testing.T) config.RedisConfig {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR no definida: se omite la integración con Redis")
	}
	return config.RedisConfig{Enabled: true, Addr: addr, DB: 15}
}

func TestPublishAndProcessTask(t *testing.T) {
	redis := redisConfig(t)
	log := slog.New(slog.DiscardHandler)
	rec := tracetest.NewSpanRecorder()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })

	pub := sharedasynq.NewPublisher(redis)
	t.Cleanup(func() { _ = pub.Close() })
	require.NoError(t, pub.Ping(context.Background()))

	type payload struct {
		ExampleUUID string `json:"example_uuid"`
	}
	got := make(chan string, 1)
	handlerTrace := make(chan trace.TraceID, 1)
	mux := sharedasynq.NewServeMux(log)
	mux.HandleFunc("test:echo", func(ctx context.Context, task *asynq.Task) error {
		var p payload
		if err := sharedasynq.DecodePayload(task, &p); err != nil {
			return err
		}
		assert.NotNil(t, logging.FromContext(ctx), "el handler recibe el logger de la tarea")
		handlerTrace <- trace.SpanContextFromContext(ctx).TraceID()
		got <- p.ExampleUUID
		return nil
	})
	srv := sharedasynq.NewServer(redis, config.QueueConfig{Concurrency: 2, ShutdownTimeout: 5 * time.Second}, log, nil)
	require.NoError(t, srv.Start(mux))
	t.Cleanup(srv.Shutdown)

	reqCtx, request := otel.Tracer("test").Start(context.Background(), "petición")
	id, err := pub.Publish(reqCtx, "test:echo", payload{ExampleUUID: "0192-abc"}, asynq.Retention(time.Minute))
	request.End()

	require.NoError(t, err)
	assert.NotEmpty(t, id)
	select {
	case v := <-got:
		assert.Equal(t, "0192-abc", v)
		assert.Equal(t, request.SpanContext().TraceID(), <-handlerTrace, "la tarea sigue la traza de la petición")
	case <-time.After(10 * time.Second):
		t.Fatal("la tarea no se procesó")
	}
}
