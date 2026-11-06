// Package awsconfig carga la configuración común de los clientes de AWS (S3, SQS).
//
// Las credenciales salen de la cadena estándar del SDK: en QA/producción, el IAM Role
// de la tarea o instancia; en develop, el perfil de ~/.aws indicado en AWS_PROFILE
// (config ya rechaza AWS_PROFILE fuera de develop). Nunca se registran.
package awsconfig

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconf "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go/metrics"
	"github.com/aws/smithy-go/metrics/smithyotelmetrics"
	"github.com/aws/smithy-go/tracing"
	"github.com/aws/smithy-go/tracing/smithyoteltracing"
	"go.opentelemetry.io/otel"

	"myapp.v1.0.0/internal/shared/infrastructure/config"
)

// Load devuelve la configuración del SDK para la región y el perfil indicados.
func Load(ctx context.Context, cfg config.AWSConfig) (aws.Config, error) {
	opts := []func(*awsconf.LoadOptions) error{awsconf.WithRegion(cfg.Region)}
	if cfg.Profile != "" {
		opts = append(opts, awsconf.WithSharedConfigProfile(cfg.Profile))
	}
	awsCfg, err := awsconf.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("cargar configuración de AWS: %w", err)
	}
	return awsCfg, nil
}

// TracerProvider y MeterProvider adaptan OpenTelemetry a la instrumentación nativa del
// SDK (smithy-go). Cada cliente los recibe en sus opciones:
//
//	s3.NewFromConfig(awsCfg, func(o *s3.Options) {
//		o.TracerProvider, o.MeterProvider = awsconfig.TracerProvider(), awsconfig.MeterProvider()
//	})
//
// Resultado: un span por llamada a la API (S3, SQS…), hijo de la petición o la tarea, y
// métricas del cliente. No incluyen cuerpos ni URLs firmadas (presignar no llama a la red).
// Se llaman después de telemetry.Setup, que registra los proveedores globales.
func TracerProvider() tracing.TracerProvider {
	return smithyoteltracing.Adapt(otel.GetTracerProvider())
}

// MeterProvider: ver TracerProvider.
func MeterProvider() metrics.MeterProvider { return smithyotelmetrics.Adapt(otel.GetMeterProvider()) }
