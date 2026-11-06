package persistence

import (
	"context"
	"database/sql"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

// Instrumentación OpenTelemetry propia (ADR 0005). No se usa gorm.io/plugin/opentelemetry
// porque importa los drivers de ClickHouse y MySQL.
//
// Cada operación de GORM es un span hijo del span del context (petición o tarea), con:
//   - db.query.text: el SQL parametrizado ($1, $2…). Nunca los valores: pueden ser
//     datos personales o secretos.
//   - db.operation.name, db.collection.name y db.response.returned_rows.
//
// «No encontrado» (gorm.ErrRecordNotFound) no marca el span como error: es un resultado.

const instrumentationName = "myapp.v1.0.0/internal/shared/infrastructure/persistence"

type otelPlugin struct{}

func (otelPlugin) Name() string { return "otel" }

func (otelPlugin) Initialize(db *gorm.DB) error {
	tracer := otel.Tracer(instrumentationName)
	cb := db.Callback()
	hooks := []struct {
		op     string
		before func(string, func(*gorm.DB)) error
		after  func(string, func(*gorm.DB)) error
	}{
		{"create", cb.Create().Before("gorm:create").Register, cb.Create().After("gorm:create").Register},
		{"query", cb.Query().Before("gorm:query").Register, cb.Query().After("gorm:query").Register},
		{"update", cb.Update().Before("gorm:update").Register, cb.Update().After("gorm:update").Register},
		{"delete", cb.Delete().Before("gorm:delete").Register, cb.Delete().After("gorm:delete").Register},
		{"row", cb.Row().Before("gorm:row").Register, cb.Row().After("gorm:row").Register},
		{"raw", cb.Raw().Before("gorm:raw").Register, cb.Raw().After("gorm:raw").Register},
	}
	for _, h := range hooks {
		if err := h.before("otel:before_"+h.op, startSpan(tracer)); err != nil {
			return err
		}
		if err := h.after("otel:after_"+h.op, endSpan); err != nil {
			return err
		}
	}
	return nil
}

func startSpan(tracer trace.Tracer) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if db.DryRun {
			return // GORM arma una subconsulta sin ejecutarla: no hay SQL que medir
		}
		ctx := db.Statement.Context
		if ctx == nil || !trace.SpanFromContext(ctx).SpanContext().IsValid() {
			return // sin traza en curso (p. ej. arranque): no se crean trazas sueltas por SQL
		}
		ctx, _ = tracer.Start(ctx, "db", trace.WithSpanKind(trace.SpanKindClient))
		db.Statement.Context = ctx
	}
}

func endSpan(db *gorm.DB) {
	if db.DryRun {
		return // startSpan no creó nada: el span del context es el de la petición
	}
	span := trace.SpanFromContext(db.Statement.Context)
	if !span.IsRecording() {
		return
	}
	defer span.End()

	query := db.Statement.SQL.String()
	op := strings.ToUpper(strings.SplitN(strings.TrimSpace(query), " ", 2)[0])
	table := db.Statement.Table
	name := op
	if table != "" {
		name += " " + table
	}
	span.SetName(name)
	span.SetAttributes(
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.operation.name", op),
		attribute.String("db.query.text", query),
		attribute.Int64("db.response.returned_rows", db.Statement.RowsAffected),
	)
	if table != "" {
		span.SetAttributes(attribute.String("db.collection.name", table))
	}
	if err := db.Error; err != nil && err != gorm.ErrRecordNotFound { //nolint:errorlint // gorm lo devuelve sin envolver
		span.RecordError(err)
		span.SetStatus(codes.Error, "error de base de datos")
		if code, ok := SQLState(err); ok {
			span.SetAttributes(attribute.String("db.response.status_code", code))
		}
	}
}

// registerPoolMetrics publica el estado del pool (CLAUDE.md §11): conexiones en uso,
// ociosas, esperas y su duración. Sirven para dimensionar DB_MAX_OPEN_CONNS.
func registerPoolMetrics(db *sql.DB, dbName string) error {
	meter := otel.Meter(instrumentationName)
	pool := metric.WithAttributes(attribute.String("db.client.connection.pool.name", dbName))
	used, err := meter.Int64ObservableUpDownCounter("db.client.connection.count", metric.WithUnit("{connection}"),
		metric.WithDescription("Conexiones del pool por estado (used | idle)."))
	if err != nil {
		return err
	}
	maxOpen, err := meter.Int64ObservableUpDownCounter("db.client.connection.max", metric.WithUnit("{connection}"))
	if err != nil {
		return err
	}
	waits, err := meter.Int64ObservableCounter("db.client.connection.wait_count", metric.WithUnit("{wait}"),
		metric.WithDescription("Veces que una consulta esperó una conexión libre."))
	if err != nil {
		return err
	}
	waitTime, err := meter.Float64ObservableCounter("db.client.connection.wait_time", metric.WithUnit("s"))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := db.Stats()
		o.ObserveInt64(used, int64(s.InUse), pool, metric.WithAttributes(attribute.String("db.client.connection.state", "used")))
		o.ObserveInt64(used, int64(s.Idle), pool, metric.WithAttributes(attribute.String("db.client.connection.state", "idle")))
		o.ObserveInt64(maxOpen, int64(s.MaxOpenConnections), pool)
		o.ObserveInt64(waits, s.WaitCount, pool)
		o.ObserveFloat64(waitTime, s.WaitDuration.Seconds(), pool)
		return nil
	}, used, maxOpen, waits, waitTime)
	return err
}
