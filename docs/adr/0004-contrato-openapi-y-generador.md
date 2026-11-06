# ADR 0004 — Contrato OpenAPI escrito a mano y generador de módulos por copia

- **Estado:** aceptado
- **Fecha:** 2026-11-05

## Contexto

**Contrato.** La plantilla anterior generaba Swagger 2.0 con swag a partir de anotaciones en los handlers. Tenía tres problemas:

- los archivos generados (`docs/docs.go`, `swagger.json/yaml`) se versionaban y se desincronizaban si nadie corría `make swagger`;
- las anotaciones repetían en comentarios lo que el código ya declara, sin que nada comprobara que coincidían;
- el contrato se deducía del código, en lugar de diseñarse y revisarse como acuerdo con el frontend.

**Módulos nuevos.** El estándar pide copiar `internal/example` y renombrar a mano unos 30 archivos e identificadores, más la migración, el registro, el contrato y la tabla de dueños. Es lento, y cada descuido (un `example:write` sin renombrar, un `EXAMPLE_` en un código de error) pasa desapercibido.

## Decisión

1. **`docs/openapi.yaml` (OpenAPI 3.0) es el contrato, escrito a mano**, en el mismo cambio que la ruta.
2. **Una prueba lo ata al código** (`cmd/api/openapi_test.go`, dentro de `make test`):
   - el documento es válido, incluidas todas las `$ref` (`kin-openapi`, solo en pruebas: no llega a los binarios);
   - las rutas registradas en Gin bajo `/api/v1` y las documentadas son **el mismo conjunto**;
   - cada operación tiene `operationId` único y documenta `401` y `500`.
3. **El generador (`tools/modulegen`, `make module`) copia el módulo de referencia** en lugar de usar plantillas propias:
   - traduce nombres de archivo e identificadores;
   - crea la migración con el siguiente número;
   - registra el módulo y copia sus bloques de OpenAPI, delimitados por marcadores;
   - agrega la fila de dueños.

   El CI genera un módulo en cada cambio y corre sus pruebas.
4. **`make check`** (formato, vet, lint, pruebas, govulncheck) es la puerta de calidad local y equivale al job principal del CI.

## Consecuencias

- La prueba de paridad no detecta por sí sola un contrato desactualizado (un campo nuevo sin documentar). Eso lo cubre `openapitest`: las pruebas HTTP validan cada respuesta real contra esquemas estrictos (`additionalProperties: false`). Lo que queda para la revisión del PR es lo que las pruebas no ejercitan y el significado de los campos.
- **Actualización:** un módulo copiado heredaba eventos, máquina de estados y búsqueda, y para un CRUD simple se empezaba borrando. Ahora `make module` genera por defecto un módulo mínimo desde una plantilla embebida, que además funciona sin `internal/example`, y la copia pasó a `FULL=1`. El costo es mantener dos fuentes: el CI genera y prueba ambas. Los nombres admiten `snake_case` y toman la forma de cada contexto.
