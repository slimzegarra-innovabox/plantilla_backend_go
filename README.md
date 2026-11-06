# Plantilla Go Backend

Plantilla para APIs REST en Go con **arquitectura hexagonal modular**. Trae un módulo de referencia completo, infraestructura compartida (seguridad, observabilidad, outbox, idempotencia), un generador de módulos y pruebas que hacen cumplir las reglas de arquitectura.

> **Estado:** la plantilla pasa todas sus pruebas, pero **todavía no ha corrido en producción**. El despliegue a AWS (ECS, ECR, OIDC) y el envío de métricas a CloudWatch solo se han probado con dobles y con la imagen real del colector, nunca contra una cuenta de AWS. Conviene empezar por un piloto en QA. Lo pendiente está en [`docs/roadmap.md`](docs/roadmap.md).

| Quiero… | Ir a |
|---|---|
| Levantarla y hacer una petición | [Arranque rápido](#arranque-rápido) |
| Empezar un proyecto nuevo | [Crear un proyecto](#crear-un-proyecto) |
| Agregar un módulo de negocio | [Crear un módulo](#crear-un-módulo) |
| Conocer las reglas que debe cumplir el código | [`CLAUDE.md`](CLAUDE.md), el estándar completo |
| Saber por qué se decidió algo | [`docs/adr/`](docs/adr) |
| Desplegar en AWS | [`docs/deploy.md`](docs/deploy.md) |
| Entender un error o un comportamiento raro | [Problemas frecuentes](#problemas-frecuentes) |

---

## Qué trae

| Área | Qué hay |
|---|---|
| **Arquitectura** | Módulos independientes con una capa `application` pura y una `infrastructure`. Las reglas de dependencia las verifica una prueba: si se rompen, el build falla ([ADR 0001](docs/adr/0001-estandar-hexagonal-modular.md)) |
| **HTTP** | Gin, sobre de respuesta único, paginación, ordenamiento y búsqueda, contrato en [`docs/openapi.yaml`](docs/openapi.yaml) validado contra las rutas y las respuestas reales ([ADR 0004](docs/adr/0004-contrato-openapi-y-generador.md)) |
| **Persistencia** | PostgreSQL con GORM y pgx, concurrencia optimista, transacciones por `context`, plazos de sesión, migraciones SQL versionadas |
| **Seguridad** | Verificación de JWT, scopes por ruta, autorización por recurso, rate limit, cabeceras de seguridad, CORS explícito, secretos que no se imprimen |
| **Fiabilidad** | Outbox transaccional ([ADR 0006](docs/adr/0006-outbox-transaccional.md)), `Idempotency-Key` en los POST ([ADR 0007](docs/adr/0007-idempotencia-en-post.md)), apagado ordenado con drenaje, sondas de salud |
| **Observabilidad** | OpenTelemetry → ADOT → X-Ray y CloudWatch, logs JSON con `request_id` y `trace_id` ([ADR 0005](docs/adr/0005-observabilidad-opentelemetry.md)) |
| **Opcionales** | S3, correo con Brevo, tareas con Asynq y Redis, consumidor de SQS: cada uno se activa con una variable ([ADR 0003](docs/adr/0003-integraciones-opcionales.md)) |
| **Entrega** | Un `Dockerfile` con imágenes distroless, CI en GitHub Actions, despliegue a ECS que construye una vez y promueve la misma imagen |

**Stack:** Go (mínimo `1.26.0`, toolchain `go1.27.3`, [ADR 0002](docs/adr/0002-versiones-soportadas-e-imagenes.md)), Gin, GORM, PostgreSQL, golang-migrate, `log/slog`, OpenTelemetry.

## Requisitos

- **Docker** con Compose v2: basta para desarrollar.
- **Go 1.26 o superior**, para correr sin Docker o usar `make check`. Go descarga solo el toolchain `go1.27.3`.
- **Para `make check`:** [golangci-lint v2](https://golangci-lint.run/welcome/install/) y `go install golang.org/x/vuln/cmd/govulncheck@latest`, compilados con Go 1.26 o superior.

---

## Arranque rápido

**1. Configura el entorno.** `.env` nunca se versiona.

```bash
cp .env.template .env
openssl rand -base64 48    # pega el resultado en JWT_SECRET
```

Cambia también `DB_PASSWORD` y `POSTGRES_PASSWORD`, con el mismo valor.

**2. Levanta los servicios y aplica las migraciones.** `make up` se queda en primer plano mostrando los logs. La primera vez tarda unos minutos.

```bash
make up                                               # en otra terminal, lo siguiente:
docker compose -f develop.yml run --rm app_migrate
make health                                           # {"status":"UP"} [200]
```

Hasta que corren las migraciones, el worker registra `relation "outbox" does not exist`. Es lo esperado y se resuelve solo.

| Servicio | URL (solo en `127.0.0.1`) |
|---|---|
| API (con recarga automática) | http://localhost:8080 |
| Jaeger (trazas) | http://localhost:16686 |
| RedisInsight | http://localhost:5540 |
| Asynqmon (tareas) | http://localhost:3000 |

**3. Genera un token de prueba.** Toda ruta de `/api/v1` exige un JWT. La plantilla solo los **verifica**: en un entorno real los emite el servicio de identidad. Solo para desarrollo local:

```bash
secret=$(grep '^JWT_SECRET=' .env | cut -d= -f2-)
b64() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
header=$(printf '{"alg":"HS256","typ":"JWT"}' | b64)
payload=$(printf '{"sub":"0192f0c0-0000-7000-8000-000000000001","iss":"https://auth.example.com","aud":"myapp-api","scope":"example:read example:write","exp":%s}' "$(($(date +%s) + 3600))" | b64)
signature=$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -hmac "$secret" -binary | b64)
TOKEN="$header.$payload.$signature"
```

`iss` y `aud` deben coincidir con `JWT_ISSUER` y `JWT_AUDIENCE` de tu `.env`.

**4. Prueba la API.**

```bash
# Crear (el reintento con la misma Idempotency-Key repite la respuesta, sin crear otro)
curl -i -X POST localhost:8080/api/v1/examples \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-1' -d '{"name":"Demo"}'

# Listar, con búsqueda
curl "localhost:8080/api/v1/examples?q=Demo" -H "Authorization: Bearer $TOKEN"

# Activar: usa el uuid de la respuesta anterior
curl -X POST localhost:8080/api/v1/examples/<uuid>/activate -H "Authorization: Bearer $TOKEN"
```

Al activar, la API escribe un evento en el outbox y el worker lo procesa. Lo verás en los logs como `example cambió de estado`, y en Jaeger como una sola traza desde la petición hasta el worker.

Para detener todo: `make down`.

**Sin Docker para la API** (con tu propio PostgreSQL y `DB_HOST=localhost` en `.env`):

```bash
make migrate-up && make run
```

---

## Cómo está organizado

```
cmd/
  api/            servidor HTTP: raíz de composición y registro de módulos
  worker/         segundo plano: tareas de Asynq, despachador del outbox y consumidor de SQS
  migrate/        CLI de migraciones
internal/
  shared/         lo común a todos los módulos (result, config, httpapi, persistence, outbox…)
  example/        módulo de referencia: muestra todos los patrones
    module.go     raíz de composición del módulo
    application/  negocio puro: model, usecase, port, dto, mapper, apperr
    infrastructure/
      persistence/  entidad GORM, mapper y repositorio
      httpapi/      handlers, Request/Response y rutas
      messaging/    eventos que se publican por el outbox y su handler en el worker
docs/             contrato OpenAPI, ADR, despliegue, operación del outbox, línea base de carga
scripts/          migraciones SQL, despliegue y comprobaciones del CI
deploy/otel/      configuración del colector ADOT
test/             arquitectura, migraciones, punta a punta y carga
tools/modulegen/  generador de módulos
```

Una petición recorre siempre el mismo camino:

```
HTTP → handler (httpapi) → caso de uso (usecase) → model + puertos (port) → adapters (persistence, external)
```

Las tres reglas que más conviene tener presentes:

1. **`application` es pura:** solo biblioteca estándar y `uuid`. Ni Gin, ni GORM, ni SDK de nube.
2. **Ningún módulo de negocio importa a otro.** Todos pueden usar `shared`, y `shared` no conoce a ninguno.
3. **Los fallos de negocio son datos (`result.Result`), los técnicos son `error`.** El código HTTP se decide en un solo lugar, según la categoría del fallo.

Las reglas completas, con ejemplos y una checklist, están en [`CLAUDE.md`](CLAUDE.md).

---

## Crear un proyecto

```bash
cp -r plantilla_backend_go/ mi-proyecto/ && cd mi-proyecto/
rm -rf .git && git init

# Renombra el module path en go.mod, imports, .golangci.yml y CLAUDE.md
grep -rl 'myapp.v1.0.0' --exclude-dir=.git . | xargs sed -i 's|myapp.v1.0.0|mimodulo.v1.0.0|g'
```

Después sigue el [arranque rápido](#arranque-rápido) y crea tus módulos. Cuando ya no necesites el módulo de referencia, quítalo con `make remove-example`.

## Crear un módulo

```bash
make module NAME=orders ENTITY=order LABEL=pedido        # CRUD mínimo: empieza por aquí
make module NAME=purchase_orders ENTITY=purchase_order   # nombres compuestos
make module NAME=orders ENTITY=order FULL=1              # copia completa de internal/example
```

| Parámetro | Para qué sirve | Por defecto |
|---|---|---|
| `NAME` | Módulo en `snake_case`: paquete (`purchaseorders`), scopes (`purchase_orders:write`), códigos de error (`PURCHASE_ORDERS_…`) | — |
| `ENTITY` | Entidad en singular: `PurchaseOrder`, `CreatePurchaseOrderUseCase`… | — |
| `PLURAL` | Tabla (`purchase_orders`) y ruta (`/api/v1/purchase-orders`) | `<ENTITY>s` |
| `LABEL` | Nombre en español para los mensajes al usuario | `<ENTITY>` |
| `FULL=1` | Agrega eventos por el outbox, máquina de estados, intenciones de negocio, ordenamiento y búsqueda | CRUD mínimo |

El módulo mínimo trae `Create`, `Get`, `List`, `Update` y `Delete`, con autorización por recurso, concurrencia optimista e `Idempotency-Key`.

El generador deja el módulo **compilando y con sus pruebas en verde**: crea su código, su migración, su registro, sus rutas en OpenAPI y su fila en la tabla de dueños. Rechaza los nombres que romperían la compilación, sin escribir nada. Después:

1. Adapta el model, la migración y el contrato a la entidad real.
2. Revisa los mensajes de `apperr` (género y redacción).
3. Corre `make check` y `make test-integration`.

**Quitar el módulo de referencia:** `make remove-example` borra su código, sus migraciones, sus registros y su contrato, y el proyecto sigue en verde. Después puedes seguir generando módulos mínimos, pero no `FULL=1`. Si sus migraciones ya se aplicaron en un entorno compartido, no las borres: crea una migración nueva que elimine la tabla.

---

## Contrato HTTP

Todas las respuestas JSON usan el mismo sobre:

```json
{
  "data": { "uuid": "0192…", "name": "Demo", "status": "pending", "version": 1 },
  "page": null,
  "notifications": [],
  "request_id": "6f1c…"
}
```

- **Errores:** `data: null` y notificaciones con `category`, `code`, `message` y `severity`. El `code` es estable y es lo que debe interpretar el cliente.
- **Rutas:** en plural bajo `/api/v1`, sin verbos. Las intenciones de negocio son `POST /{uuid}/<accion>`. Crear responde `201` con `Location`.
- **Listados:** `page`, `size` (máximo 100), `sort=name,-createdAt` y `q` (mínimo 3 caracteres).
- **Reintentos seguros:** con la misma `Idempotency-Key` y el mismo cuerpo, durante 24 h, un POST repite la respuesta sin ejecutar nada. No garantiza exactamente una vez: donde un duplicado sea inaceptable, como en pagos, agrega además un constraint único ([ADR 0007](docs/adr/0007-idempotencia-en-post.md)).
- **`docs/openapi.yaml` se escribe a mano** y se revisa en cada PR. Las pruebas fallan si una ruta no está documentada, si una documentada no existe o si una respuesta no cumple su esquema. La API no lo sirve: se lee con un visor de OpenAPI.

## Configuración

Todo viene de variables de entorno. Se valida al arrancar y, si algo falla, el proceso termina con la lista completa de problemas. `.env` solo se lee con `APP_ENV=develop`. La lista completa está en [`.env.template`](.env.template).

| Grupo | Variables principales |
|---|---|
| Proceso | `APP_ENV` (`develop`, `test`, `qa`, `production`), `LOG_LEVEL` |
| HTTP | `HTTP_ADDR`, `HTTP_CORS_ALLOWED_ORIGINS`, `HTTP_TRUSTED_PROXIES`, `HTTP_RATE_LIMIT_RPS`, `HTTP_REQUEST_TIMEOUT`, `HTTP_DRAIN_DELAY` |
| Base de datos | `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSLMODE`, `DB_STATEMENT_TIMEOUT`, `DB_LOCK_TIMEOUT` |
| JWT | `JWT_SECRET` (al menos 32 bytes), `JWT_ISSUER`, `JWT_AUDIENCE` |

Fuera de develop y test, el arranque rechaza los marcadores `change-me`, `DB_SSLMODE=disable` en producción y `AWS_PROFILE` (se usa el IAM Role). Además exige `HTTP_TRUSTED_PROXIES`: las subredes del balanceador, o `none` si no hay proxy delante.

### Integraciones opcionales

Cada una se activa con su interruptor y, solo entonces, sus variables son obligatorias. Un módulo que necesita una la exige con `module.MustHave` en su `Register`.

| Integración | Interruptor | Variables | En `module.Deps` |
|---|---|---|---|
| Redis, Asynq y outbox | `REDIS_ENABLED` | `REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_TLS` | `Tasks`, `Outbox` |
| S3 | `S3_ENABLED` | `AWS_REGION`, `S3_BUCKET`, `S3_UPLOAD_MAX_BYTES`, `S3_PRESIGN_TTL` | `Storage` |
| Brevo | `BREVO_ENABLED` | `BREVO_API_KEY`, `BREVO_SENDER_EMAIL` | `Mail` |
| SQS (solo worker) | `SQS_ENABLED` | `AWS_REGION`, `SQS_QUEUE_URL`, `SQS_VISIBILITY_TIMEOUT` | — |

**Sin Redis no hay outbox:** los eventos de los módulos se descartan, y la API lo avisa en el log al arrancar. `make up` siempre lo activa.

## Trabajo en segundo plano

`cmd/worker` corre las tareas de Asynq, el despachador del outbox y el consumidor de SQS. No recibe tráfico: su sonda de salud solo escucha en loopback.

Para avisar a otro proceso, un caso de uso escribe el evento en el outbox **dentro de la misma transacción** que el cambio. Así, no se pierde el aviso ni sale uno de un cambio que no se guardó. La entrega es **al menos una vez y sin orden garantizado**, por lo que los handlers deben ser idempotentes. La operación (alarmas, mensajes muertos, reencolado) está en [`docs/outbox.md`](docs/outbox.md).

## Observabilidad

Se activa con `OTEL_ENABLED=true` y exporta trazas y métricas por OTLP a un colector.

- **En local:** `make up` ya envía las trazas a Jaeger.
- **En AWS:** el colector ADOT va como sidecar de la task, con [`deploy/otel/adot-collector.yaml`](deploy/otel/adot-collector.yaml). Las trazas van a X-Ray y las métricas a CloudWatch (namespace `MyApp`). El IAM Role de la task necesita permisos de X-Ray y de CloudWatch Logs.
- **Con compose:** `docker compose -f production.yml --profile otel up -d`.
- **Salud:** `GET /health/live` indica que el proceso atiende, y `GET /health/ready`, que además llegan sus dependencias. El balanceador apunta a `/health/ready`.

Una petición se ve como una sola traza: HTTP → SQL → S3, SQS o Brevo → tarea en el worker. Cada log lleva su `request_id` y su `trace_id`. Nunca van a un log ni a un span los cuerpos, tokens, secretos ni valores de parámetros SQL.

---

## Pruebas y calidad

```bash
make test               # unitarias, HTTP, OpenAPI y arquitectura, sin Docker
make test-integration   # además, PostgreSQL y Redis reales, y los binarios de punta a punta
make test-docker        # la integración dentro de contenedores, sin Go local
make check              # formato, vet, lint, pruebas y vulnerabilidades: antes de abrir un PR
make lint-ci            # workflows y scripts de despliegue, incluido el colector ADOT real
make cover              # cobertura con integración; reporte en coverage.html
make load               # prueba de carga con k6; línea base en docs/load-baseline.md
```

- La integración usa Docker, o `TEST_DATABASE_URL` si existe. Las pruebas de Asynq se omiten sin `TEST_REDIS_ADDR=localhost:6379`.
- Las pruebas de integración corren con `-p 1`, porque comparten la base. Al lanzarlas a mano, usa también `-count=1`: `test/e2e` compila los binarios durante la prueba, y la caché de Go puede dar un resultado viejo.
- No se usan librerías de mocking. Los dobles de cada puerto viven en `port/porttest`, junto con una suite de contrato que corre contra el doble y contra el adapter real.

El CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) corre todo esto, comprueba que las migraciones integradas no cambiaron, prueba el generador y escanea las imágenes con Trivy.

## Migraciones

```bash
make migrate-create NAME=add_orders_status   # par up/down con el siguiente número
make migrate-up                              # aplica las pendientes
make migrate-down                            # revierte la última
make migrate-version                         # versión actual
```

- Una migración ya aplicada en un entorno compartido **nunca se edita**: se crea una nueva. El CI lo verifica.
- No corren al arrancar el servidor: son un paso explícito del despliegue, antes de la nueva versión.
- Son *forward-only* en los despliegues: la versión anterior debe tolerar el esquema nuevo. Un cambio incompatible se hace en dos despliegues.

## Despliegue

**En AWS (ECS).** Un push a `main` con el CI en verde publica las imágenes en ECR y despliega en QA. Producción se despliega a mano con una etiqueta ya probada en QA, sin reconstruir. Si ECS revierte la versión, el pipeline falla. Para volver a una versión anterior, se ejecuta el workflow con esa etiqueta y la casilla `rollback`. Mientras no estén configuradas las variables, el despliegue se omite. Infraestructura, roles OIDC y variables: [`docs/deploy.md`](docs/deploy.md).

**Con compose (EC2).**

```bash
docker compose -f production.yml run --rm app_migrate   # 1. migraciones
docker compose -f production.yml up -d --build          # 2. nueva versión de api y worker
```

| Archivo | Ambiente |
|---|---|
| `develop.yml` | Desarrollo con recarga automática |
| `test.yml` | Integración en contenedores |
| `quality.yml`, `production.yml` | Imágenes distroless, solo lectura; variables desde `ENV_FILE` (`.env.qa`, `.env.prod`, nunca versionados) |

Las imágenes no tienen shell: para diagnosticar se usan los logs y `/health/*`.

---

## Problemas frecuentes

| Qué ves | Causa y solución |
|---|---|
| `relation "outbox" does not exist` en el worker | Faltan las migraciones: `docker compose -f develop.yml run --rm app_migrate` |
| `401 AUTH_TOKEN_INVALID` | Token vencido, firmado con otro `JWT_SECRET`, o con `iss` o `aud` distintos de los de `.env`. El error no dice cuál, a propósito |
| `403 AUTH_FORBIDDEN` | Al token le falta el scope de la ruta |
| `403 <MODULO>_FORBIDDEN` | El scope está bien, pero el registro es de otro usuario: solo pueden modificarlo su creador o quien tenga `<modulo>:admin` |
| `409 <MODULO>_VERSION_CONFLICT` | Otro escritor cambió el registro: vuelve a leerlo y reintenta con la `version` nueva |
| `409 IDEMPOTENCY_IN_PROGRESS` | La petición original con esa clave sigue en curso: reintenta tras `Retry-After` |
| `422 IDEMPOTENCY_KEY_REUSED` | Esa clave ya se usó con otro cuerpo: usa una nueva |
| El proceso termina al arrancar y lista variables | La configuración no es válida. En QA y producción suele faltar `HTTP_TRUSTED_PROXIES` |
| `make test` falla en `test/architecture` | Se rompió una regla de dependencia: mira [CLAUDE.md §4](CLAUDE.md#4-reglas-de-dependencia-verificadas-por-testarchitecture). Se corrige el import, no la prueba |
| `make test` falla en `openapi_test` | Una ruta y `docs/openapi.yaml` no coinciden: se actualizan en el mismo cambio |
| `no migration found for version N` al desplegar | La imagen es anterior a la última migración. Si es una vuelta atrás, usa la casilla `rollback` |
| Mensajes acumulados en el outbox | El worker o Redis están caídos. Una caída no gasta intentos: al volver, sale todo ([`docs/outbox.md`](docs/outbox.md)) |

## Más documentación

| Documento | Contenido |
|---|---|
| [`CLAUDE.md`](CLAUDE.md) | El estándar: reglas, convenciones, ejemplos y checklist. Ante una duda, prevalece |
| [`docs/adr/`](docs/adr) | Por qué se decidió cada cosa |
| [`docs/openapi.yaml`](docs/openapi.yaml) | Contrato HTTP |
| [`docs/deploy.md`](docs/deploy.md) | Despliegue en AWS |
| [`docs/outbox.md`](docs/outbox.md) | Operación del outbox |
| [`docs/load-baseline.md`](docs/load-baseline.md) | Resultados de las pruebas de carga |
| [`docs/roadmap.md`](docs/roadmap.md) | Mejoras pendientes y hechas |

`make help` lista todos los comandos.
