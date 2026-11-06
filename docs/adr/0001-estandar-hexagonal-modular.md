# ADR 0001 — Estándar de arquitectura hexagonal modular

- **Estado:** aceptado
- **Fecha:** 2026-11-04
- **Reemplaza a:** el estándar anterior de `CLAUDE.md` (capas Handler → UseCase → Service → Provider → Repository)

## Contexto

La plantilla debe servir de base a cualquier backend Go con arquitectura hexagonal, DDD y Clean Code. Un análisis externo (`plantilla_backend_csharp/docs/reference-go/04-EVALUACION-CRITICA.md`) y la revisión del código confirmaron que el estándar anterior acumulaba capas sin aislar mejor el núcleo, y que en dos puntos rompía la regla central del hexágono:

| Problema | Evidencia | Efecto |
|---|---|---|
| Capa *Provider* que duplica el repositorio 1:1 (A-05) | `providerexampleconcretetype/example.go` | Dos interfaces y dos structs por cada acceso a datos, sin ningún punto de variación |
| UseCase que solo delega en un Service de «dominio» que orquesta transacciones (A-04) | `usecasecreateexample/usecase.go`, `createexampleservice/service.go` | La orquestación vive en el dominio; el caso de uso no aporta nada |
| Interfaces del núcleo con `*gorm.DB` y *claims* JWT (A-06) | `apiinterface/*.go`, `apidto/tokenservice/claim.go` | **El núcleo conoce el ORM**: rompe el hexágono |
| Contratos HTTP dentro de `application` (A-07) | `contracthttpcreateexample/` | **El núcleo conoce HTTP**: rompe el hexágono |
| Un mapper con interfaz por dirección y por capa (A-10) | `examplehandlermapper`, `examplerepositorymapper` | Interfaces que nadie sustituye |
| Un paquete por acción con nombres que repiten el módulo (A-09) | `src/app/example/**` (6 niveles) | No es Go idiomático; mucho código de relleno por endpoint |
| Dos tablas distintas de categoría → HTTP (A-13) | `notification.go` frente a `middleware/manageerror.go` | Un mismo error responde con códigos distintos |
| «No encontrado» devuelto como entidad vacía (P-04); mapper que ignora errores de VO (P-05) | `getexamplebyuuid.go`, `examplerepositorymapper/repository.go` | Datos inválidos o inexistentes pasan como válidos |
| Reintento de **cualquier** error, sin cancelación; SQL sin `WithContext` (P-02, P-03) | `transactiontxt/*.go`, `examplerepository/*.go` | Se reintentan fallos de negocio; la cancelación no llega a la base de datos |
| Reglas de dependencia solo escritas | `CLAUDE.md` | Nada impide violarlas |

En paralelo, el equipo ya trabaja con un estándar probado en producción para el BFF Java de la intranet (`enazul-intranet-bck-bff/docs/arquitectura-hexagonal-prompt.md`), con reglas que nacieron de incidentes reales: Entity local por módulo, un único dueño de escritura por tabla, sufijos de puertos, `ResultDto` con notificaciones, paginación centralizada y una tabla explícita de errores técnicos.

## Decisión

1. **Se adopta como base el estándar del BFF Java, traducido a Go:** módulos de negocio aislados con dos capas (`application`, `infrastructure`), `application` pura, puertos con sufijos de rol, Entity local y un único dueño de escritura, `Result` con notificaciones, contrato JSON `snake_case` y paginación desde 1.
2. **Se elimina** la capa Provider, el Service intermedio, las interfaces por caso de uso y las interfaces de mapper. El caso de uso orquesta y depende directamente de los puertos.
3. **Estructura Go:** `internal/<modulo>/{application,infrastructure}/<carpeta plana>`, con un paquete por carpeta, archivos en `snake_case` y un `module.go` como raíz de composición del módulo.

### Traducciones de Java a Go

| Java (BFF) | Go (plantilla) | Motivo |
|---|---|---|
| `Application.java` | `cmd/api/main.go` | Raíz de composición idiomática |
| `UseCaseConfig` (`@Bean`) | `internal/<m>/module.go` → `Register(router, deps)` | Composición explícita, sin contenedor |
| `@Transactional` en el adapter | Puerto `TransactionManager.WithinTransaction(ctx, fn)` | Go no tiene AOP; la transacción viaja en el `context` |
| Excepción técnica + `GlobalExceptionHandler` | `Execute` devuelve `(Result[T], error)`; el middleware convierte `error` en 500 | Go no tiene excepciones: el segundo valor de retorno cumple ese papel |
| `application/exception` | `application/apperr` con `*result.Failure` | En Go «exception» es vocabulario ajeno; `Failure` implementa `error` |
| `ResultDto`, `NotificationDto`, `PageQuery` | `result.Result`, `result.Notification`, `shareddto.PageQuery` | En Go, el nombre del paquete ya da el contexto del sufijo `Dto` |
| `infrastructure/http` | `infrastructure/httpapi` | Un paquete `http` ocultaría `net/http` |
| `/actuator/health` | `/health/live` + `/health/ready` | No hay Actuator; ver la mejora M6 |
| SQLSTATE por la cadena de causas | `errors.As(err, *pgconn.PgError)` | Mismo criterio: nunca se analiza el texto |
| Optional / `null` | `(*T, bool, error)` | `nil, nil` es ambiguo y lo marca el linter `nilnil` |

### Desviaciones intencionadas respecto del estándar Java

| # | Java | Go | Por qué |
|---|---|---|---|
| X1 | `!isSuccess()` → siempre 400 | HTTP según la **categoría** (400/401/403/404/409/422) | Con un 400 único, un «no encontrado» se ve como error del cliente, y caché, reintentos y monitoreo no pueden distinguirlo. La decisión sigue siendo exclusiva de infraestructura, así que el principio se mantiene |
| X2 | `application` = solo el JDK | Biblioteca estándar + `github.com/google/uuid` | Un UUID es un tipo de valor, no una tecnología que cambie sin que cambie el negocio. Como `string`, cada capa tendría que revalidarlo |
| X3 | Aislamiento por convención (sin ArchUnit) | Prueba de arquitectura (`test/architecture`) + `depguard` | En Java, ArchUnit añade peso al build. En Go es una prueba de unas 40 líneas con `go list`, dentro de `go test`. El propio documento Java la recomienda «si el equipo crece»; en una plantilla no cuesta nada |
| X4 | `uuid` + `timestamp` por notificación | `request_id` por respuesta | Un único id por petición correlaciona cabecera, respuesta y logs. Un id por notificación no aporta más al soporte |

## Mejoras incorporadas (más allá de ambos estándares)

| # | Mejora | Problema que resuelve |
|---|---|---|
| M1 | Prueba de arquitectura + `depguard` | Las reglas «no negociables» se verifican en el build |
| M2 | Generador `make module` | El estándar se cumple por defecto al crear un módulo |
| M3 | `make check` (fmt, vet, lint, pruebas, arquitectura, `govulncheck`) | Una única puerta de calidad, local y en CI |
| M4 | Pruebas de persistencia con PostgreSQL real (`pgtest`: service container en CI o contenedor desechable en local), migraciones `up → down → up` y **suites de contrato** que corren igual contra el doble en memoria y contra el adapter | `sqlmock` comprueba el texto del SQL, no el comportamiento (T-03), y un doble escrito a mano puede desviarse del adapter real sin que nadie lo note. Se descartó testcontainers-go: junto con golang-migrate obliga a Go 1.25 |
| M5 | Concurrencia optimista (`version`) | Dos ediciones simultáneas: la segunda recibe 409 en lugar de pisar a la primera |
| M6 | `/health/live` y `/health/ready` separados, sin detalles | *Liveness* no reinicia en bucle si cae la base de datos; *readiness* saca la instancia del balanceador (S-10) |
| M7 | `TransactionManager` que solo reintenta `40001`/`40P01` y respeta la cancelación | P-02, P-03 |
| M8 | Configuración tipada y validada al arrancar, sin sufijos por entorno | Defaults incoherentes entre binarios (`production` frente a `quality`, `sslmode`) |
| M9 | `request_id` en el `context`, los logs y la respuesta | Correlación de extremo a extremo |
| M10 | JWT estricto, scopes por ruta, cabeceras de seguridad, límite de cuerpo, rate limit, proxies de confianza, CORS por configuración | S-03, S-05, S-06, S-11 |
| M11 | Intenciones de negocio como sub-recursos (`POST /{uuid}/activate`) y REST versionado (`/api/v1`) con 201 + `Location` | A-17; las transiciones de estado son explícitas y auditables |
| M12 | Docker sin root, imágenes fijadas, sin `libvips` (se elimina `imageproc`, que no se usaba) | S-12 y una dependencia de sistema menos |

## Consecuencias

**Positivas**
- Menos archivos por acción nueva en un módulo existente: de ~11 archivos y ~6 paquetes nuevos (request, response, interfaces de port, usecase, service, DTO, handler, mapper de handler, método de repositorio, wire) a ~3 archivos y ningún paquete nuevo (acción de `httpapi`, caso de uso y `Command`), más un método en el puerto y en el adapter.
- El núcleo no conoce GORM, gin ni JWT, y una prueba lo garantiza.
- El contrato de error es el mismo que el del BFF Java: el frontend lo maneja igual en ambos backends.

**Negativas y costos aceptados**
- Es una reescritura casi completa de la plantilla. Los proyectos ya creados a partir del estándar anterior no se migran automáticamente: deben adoptar este estándar al refactorizar cada módulo.
- Hay mapeo duplicado entre Entity locales de distintos módulos sobre la misma tabla. Es acotado y mecánico (ver la justificación en el estándar Java).
- El `Result` más el `error` en la firma de cada caso de uso es más verboso que devolver solo `error`. Se acepta para mantener una semántica idéntica a la del BFF (notificaciones múltiples y avisos en éxitos).

## Alternativas consideradas

- **Mantener el estándar anterior y corregir solo los bugs.** Se descartó: no resuelve las dos violaciones del hexágono (A-06, A-07) ni el exceso de capas.
- **`domain/` + `app/` + `adapter/` con DDD táctico completo** (aggregates, domain events). Se descartó por ahora: para el tamaño de los módulos esperados añade ceremonia sin beneficio, y lo alejaría del estándar del equipo. Los tipos con invariantes (`ExampleName`) y las máquinas de estado se mantienen dentro de `model`.
- **ProblemDetails (RFC 9457) en lugar del sobre `{data, notifications}`.** Se descartó para que el contrato sea el mismo que el del BFF Java y que la plantilla Go actual.
