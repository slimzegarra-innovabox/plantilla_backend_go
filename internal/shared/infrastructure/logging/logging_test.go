package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"myapp.v1.0.0/internal/shared/infrastructure/logging"
)

func TestRequestIDTravelsInContextAndLogger(t *testing.T) {
	var buf bytes.Buffer
	base := logging.New(&buf, slog.LevelInfo, false)

	ctx := logging.WithLogger(context.Background(), base)
	ctx = logging.WithRequestID(ctx, "req-1")
	ctx = logging.WithAttrs(ctx, "user_uuid", "u-1")
	logging.FromContext(ctx).Info("hola")

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "req-1", line["request_id"])
	assert.Equal(t, "u-1", line["user_uuid"])
	assert.Equal(t, "req-1", logging.RequestID(ctx))
}

func TestFromContextFallsBackToDefault(t *testing.T) {
	assert.Same(t, slog.Default(), logging.FromContext(context.Background()))
	assert.Empty(t, logging.RequestID(context.Background()))
}

func TestLogsCarryTraceIDsWhenThereIsASpan(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo, false).With("request_id", "r1")
	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	log.InfoContext(ctx, "con span")
	log.InfoContext(context.Background(), "sin span")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], `"trace_id":"0af7651916cd43dd8448eb211c80319c"`)
	assert.Contains(t, lines[0], `"span_id":"b7ad6b7169203331"`)
	assert.Contains(t, lines[0], `"request_id":"r1"`, "los atributos de With se conservan")
	assert.NotContains(t, lines[1], "trace_id")
}
