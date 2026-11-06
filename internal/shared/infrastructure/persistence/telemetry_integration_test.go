//go:build integration

package persistence_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"myapp.v1.0.0/internal/shared/infrastructure/persistence"
	"myapp.v1.0.0/internal/shared/infrastructure/persistence/pgtest"
)

func TestQueriesAreTracedWithoutParameterValues(t *testing.T) {
	setupProbe(t)
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	db, err := persistence.Open(context.Background(), pgtest.Config(), slog.New(slog.DiscardHandler), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = persistence.Close(db) })

	ctx, parent := otel.Tracer("test").Start(context.Background(), "petición")
	require.NoError(t, db.WithContext(ctx).Exec("INSERT INTO tx_probe (v) VALUES (?)", "secreto@example.com").Error)
	var n int64
	require.NoError(t, db.WithContext(ctx).Table("tx_probe").Where("v = ?", "secreto@example.com").Count(&n).Error)
	err = db.WithContext(ctx).Exec("INSERT INTO tx_probe (v) VALUES (NULL)").Error // NOT NULL
	require.Error(t, err)
	parent.End()

	// Sin span padre no se crean trazas sueltas por cada SQL.
	require.NoError(t, db.Exec("SELECT 1").Error)

	var dbSpans []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() != "petición" {
			dbSpans = append(dbSpans, s)
		}
	}
	require.Len(t, dbSpans, 3)
	for _, s := range dbSpans {
		assert.Equal(t, parent.SpanContext().TraceID(), s.SpanContext().TraceID(), "hijo de la petición")
		for _, a := range s.Attributes() {
			assert.NotContains(t, a.Value.String(), "secreto@example.com", "un valor de parámetro llegó al span (%s)", a.Key)
		}
	}
	assert.Equal(t, "INSERT", dbSpans[0].Name())
	assert.Equal(t, "SELECT tx_probe", dbSpans[1].Name())
	assert.Equal(t, codes.Error, dbSpans[2].Status().Code)
	assert.Contains(t, attr(dbSpans[1], "db.query.text"), "$1")
	assert.Equal(t, "23502", attr(dbSpans[2], "db.response.status_code"))
}

// CountUpTo arma una subconsulta en modo DryRun, que pasa por los callbacks sin ejecutar
// nada. No debe aparecer como un SQL más, ni cerrar el span de la petición.
func TestCountUpToProducesOneSpanAndKeepsTheParentOpen(t *testing.T) {
	setupProbe(t)
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	db, err := persistence.Open(context.Background(), pgtest.Config(), slog.New(slog.DiscardHandler), false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = persistence.Close(db) })

	ctx, parent := otel.Tracer("test").Start(context.Background(), "petición")
	_, err = persistence.CountUpTo(db.WithContext(ctx).Table("tx_probe"), 10)
	require.NoError(t, err)

	assert.True(t, parent.IsRecording(), "el span de la petición sigue abierto")
	parent.End()
	var queries []string
	for _, s := range rec.Ended() {
		if s.Name() != "petición" {
			queries = append(queries, attr(s, "db.query.text"))
		}
	}
	require.Len(t, queries, 1, "un solo SQL ejecutado: %v", queries)
	assert.Contains(t, queries[0], "count(")
}

func attr(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.String()
		}
	}
	return ""
}
