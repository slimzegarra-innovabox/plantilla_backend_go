# ADR 0005 — Observabilidad con OpenTelemetry, exportada a CloudWatch con ADOT

- **Estado:** aceptado
- **Fecha:** 2026-11-05

## Contexto

La plantilla solo tenía logs JSON con `request_id`. Con eso se investiga un error concreto, pero no se puede responder «¿subió la latencia p99 desde el despliegue de ayer?» ni «¿qué parte de esta petición es lenta: la base, S3 o la tarea encolada?». El equipo usa AWS y **CloudWatch** como plataforma de observabilidad.

## Decisión

1. **OpenTelemetry**, el estándar abierto, exportado por **OTLP/HTTP** a un colector. En AWS es el **ADOT Collector** (sidecar en ECS), que envía:
   - las trazas a X-Ray (en CloudWatch: Traces / Transaction Search);
   - las métricas a CloudWatch en formato EMF.

   La configuración está en `deploy/otel/adot-collector.yaml`. La aplicación no sabe que el destino es AWS: cambiar a otro proveedor es cambiar la configuración del colector.
2. **IDs de traza con el formato de X-Ray** (`xray.NewIDGenerator`, compatibles con W3C), para que X-Ray los acepte y CloudWatch los correlacione sin conversión. La propagación es **W3C** (`traceparent`, `baggage`).
3. **Qué se instrumenta**, siempre en los bordes y adapters (`application` no conoce OpenTelemetry):
   - HTTP entrante con `otelgin`: un span por petición, con métricas `http.server.*` por ruta y `request.id` como atributo. `/health/*` no se traza.
   - SQL con un **plugin propio de GORM**: un span por sentencia con el SQL **parametrizado**, sin valores, y métricas del pool. No se usa `gorm.io/plugin/opentelemetry` porque importa los drivers de ClickHouse y MySQL.
   - Llamadas a AWS (S3, SQS) con la instrumentación **nativa** del SDK (`smithyoteltracing`/`smithyotelmetrics` en las opciones de cada cliente; `otelaws` está obsoleto), y a Brevo con `otelhttp`.
   - **Asynq:** el contexto viaja en las **cabeceras de la tarea** (asynq v0.26), no en el payload, y la tarea queda en la misma traza que la petición que la encoló. Métrica `messaging.process.duration`.
   - **SQS:** el contexto se extrae de los atributos `traceparent`/`tracestate` del mensaje.
   - Métricas de runtime de Go.
4. **Logs correlacionados:** todo log emitido con un `context` que lleva span incluye `trace_id` y `span_id`. Con OTel desactivado, si la petición trae un `traceparent`, también se incluyen: el log queda correlacionado con el servicio que llamó.
5. **Interruptor `OTEL_ENABLED`**, como las demás integraciones opcionales. Apagado, los proveedores son no-op y no se exporta nada. El destino, el muestreo y los atributos del recurso usan las **variables estándar** del SDK (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_TRACES_SAMPLER`, `OTEL_RESOURCE_ATTRIBUTES`), que no se duplican en la configuración propia.
6. **Datos sensibles:** nunca van a un span:
   - valores de parámetros SQL;
   - cuerpos de peticiones o mensajes;
   - cabeceras (la API key de Brevo viaja en una);
   - la query string;
   - URLs firmadas: presignar no hace llamadas de red.

## Consecuencias

- Las dependencias crecen: SDK de OTel, `otelgin` (que además exigió subir gin a 1.12), los adaptadores OTel de smithy-go, `otelhttp` y asynq 0.26. Todo queda dentro de govulncheck.
- La correlación con X-Ray depende del formato de ID. Si en el futuro se usa otro backend, el generador se puede quitar sin otros cambios.
- En develop, `develop.yml` trae Jaeger (UI en `localhost:16686`) y apaga las métricas (`OTEL_METRICS_EXPORTER=none`), porque Jaeger solo recibe trazas.
- El envío real a X-Ray/CloudWatch requiere el IAM Role con permisos de X-Ray y CloudWatch Logs. Con IMDSv2 en EC2, el *hop limit* debe ser 2 para que el colector en contenedor alcance las credenciales.
