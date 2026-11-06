// Package telemetry configura OpenTelemetry (trazas y métricas) para cmd/api y cmd/worker.
//
// Diseño (ADR 0005):
//   - Exporta por OTLP/HTTP a un colector: en AWS, el ADOT Collector (o el agente de
//     CloudWatch), que envía las trazas a X-Ray y las métricas a CloudWatch. El destino
//     se configura con las variables estándar OTEL_EXPORTER_OTLP_*, no con código.
//   - Los IDs de traza se generan con el formato de X-Ray (válidos también en W3C), para
//     que X-Ray y CloudWatch los acepten y correlacionen sin conversión.
//   - Propagación W3C (traceparent, baggage) en HTTP, Asynq (cabeceras de la tarea) y SQS
//     (atributos del mensaje).
//   - Con OTEL_ENABLED=false los proveedores son no-op: la instrumentación queda en su
//     lugar sin coste y sin exportar nada.
//   - application no conoce OpenTelemetry: se instrumentan los bordes y los adapters.
//
// El muestreo se controla con OTEL_TRACES_SAMPLER / OTEL_TRACES_SAMPLER_ARG
// (por defecto parentbased_always_on; en producción, p. ej. parentbased_traceidratio y 0.1).
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
)

// ScopeName es el nombre de la instrumentación propia del proyecto (spans de Asynq, SQS…).
const ScopeName = "myapp.v1.0.0/internal/shared/infrastructure/telemetry"

// Shutdown vacía y cierra los exportadores. Se llama al apagar, con un plazo.
type Shutdown func(context.Context) error

// Setup registra los proveedores globales de OpenTelemetry. El propagador W3C se
// registra siempre, aunque esté desactivado: así un traceparent entrante se reenvía
// a las llamadas salientes y la traza de otro servicio no se corta en este.
func Setup(ctx context.Context, cfg config.OTelConfig, env, version string) (Shutdown, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	// resource.Default() incluye OTEL_RESOURCE_ATTRIBUTES (p. ej. los del ECS task).
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(version),
		semconv.DeploymentEnvironmentNameKey.String(env),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}

	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("telemetry: exportador de trazas: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithIDGenerator(xray.NewIDGenerator()),
	)

	otel.SetTracerProvider(tp)
	if !cfg.Metrics {
		return tp.Shutdown, nil
	}

	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("telemetry: exportador de métricas: %w", err), tp.Shutdown(ctx))
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	// Memoria, GC y goroutines del proceso.
	if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(15 * time.Second)); err != nil {
		return nil, errors.Join(fmt.Errorf("telemetry: métricas de runtime: %w", err), tp.Shutdown(ctx), mp.Shutdown(ctx))
	}

	return func(ctx context.Context) error {
		// Trazas primero: son las que más se pierden si el proceso termina sin vaciar.
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}
