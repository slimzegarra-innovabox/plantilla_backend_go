# Plantilla Go Backend — Estándar de Arquitectura

Referencia oficial para todo desarrollo en este proyecto y en los proyectos creados a partir de él. Va dirigida tanto al equipo como a la IA: **ante cualquier duda, la regla explícita de este documento prevalece sobre las «buenas prácticas generales»** vistas en otros contextos.

El razonamiento detrás de cada decisión está en `docs/adr/` (0001: estándar hexagonal; 0002: versión de Go e imágenes; 0003: integraciones opcionales; 0004: contrato OpenAPI y generador de módulos; 0005: observabilidad; 0006: outbox transaccional; 0007: idempotencia en POST).

Las mejoras pendientes están en `docs/roadmap.md`. Una mejora que se completa se marca allí **en el mismo commit** que la implementa.

---

## 1. Reglas no negociables (resumen)

1. **Arquitectura hexagonal simplificada (Ports & Adapters)** en **módulos de negocio independientes**. Cada módulo tiene dos capas: `application` e `infrastructure`.
2. **`application` es pura:** solo la biblioteca estándar de Go, `github.com/google/uuid` y otros paquetes `application` permitidos (§4). Ni gin, ni gorm, ni jwt, ni AWS, ni tags `json`/`gorm`/`binding`.
3. **Ningún módulo de negocio importa a otro.** Todos pueden importar `shared`; `shared` no importa ningún módulo.
4. **Las reglas 2 y 3 las verifica una prueba** (`test/architecture`) y `depguard`. Si fallan, el build falla.
5. **Un caso de uso = una acción de negocio = un struct con un único método `Execute`.** No hay capa Provider, ni Service intermedio, ni interfaces por caso de uso.
6. **Los fallos de negocio son datos** (`result.Result[T]` con notificaciones). **Los fallos técnicos son `error`** y los convierte en 500 un único manejador del borde HTTP. Ningún caso de uso convierte un error técnico desconocido en un fallo de negocio.
7. **El código HTTP se decide solo en infraestructura**, a partir de la **categoría** de la notificación (§8.3).
8. **Los contratos HTTP nunca exponen ids numéricos:** siempre `uuid`.
9. **Toda operación de I/O recibe `context.Context`** y la consulta SQL lo usa (`WithContext`).
10. **Todo dato persistido que no se puede interpretar falla de forma explícita.** Nunca se devuelve una entidad vacía o con valores por defecto.
11. **Nada sensible en el repositorio ni en los logs:** secretos, tokens, contraseñas, cadenas de conexión, credenciales firmadas o cuerpos de petición.

---

## 2. Stack

| Elemento | Valor |
|---|---|
| Lenguaje | Go: mínimo `1.26.0`, toolchain `go1.27.3`. Solo versiones con soporte de seguridad (ADR 0002) |
| Module path | `myapp.v1.0.0` (se renombra al crear el proyecto) |
| HTTP | Gin (solo en `infrastructure`) |
| Persistencia | GORM + PostgreSQL (driver pgx) (solo en `infrastructure`) |
| Migraciones | golang-migrate, SQL versionado |
| Auth | JWT (`golang-jwt/jwt/v5`), solo verificación |
| Logging | `log/slog` (JSON; texto en `develop`), con `trace_id`/`span_id` |
| Observabilidad | OpenTelemetry (trazas y métricas) por OTLP → ADOT Collector → X-Ray / CloudWatch (ADR 0005) |
| Pruebas | `testing` + `testify` (`assert`/`require`); dobles escritos a mano; PostgreSQL real para integración (`pgtest`) |
| Contrato HTTP | `docs/openapi.yaml` (OpenAPI 3.0, escrito a mano, verificado contra las rutas reales) |
| Calidad | `make check`: gofmt, `go vet`, golangci-lint v2, pruebas, `govulncheck` |
| Opcionales | S3, correo (Brevo), Asynq + Redis, consumidor SQS (ADR 0003) |

---

## 3. Estructura del proyecto

```
cmd/
  api/main.go              → raíz de composición del servidor HTTP (equivale a Application.java)
  worker/main.go           → procesos en segundo plano: servidor de Asynq y consumidor de SQS
  worker/tasks.go          → registro de los handlers de tareas y de SQS de cada módulo
  migrate/main.go          → CLI de migraciones
internal/
  shared/
    application/
      result/              → Result[T], Notification, Failure, Category, Severity
      port/                → puertos comunes: TransactionManager, Clock
      dto/                 → PageQuery, PageResult[T], Principal
      → NO tiene usecase: shared no ejecuta acciones de negocio propias
    infrastructure/
      config/              → configuración tipada y validada al arrancar
      logging/             → slog, logger con request_id desde el context
      persistence/         → conexión, GormTransactionManager, DB(ctx), clasificación de errores de Postgres
        pgtest/            → base PostgreSQL real para pruebas de integración (etiqueta integration)
      httpapi/             → sobre de respuesta, categoría → HTTP, middlewares (request id, recovery, errores,
                             auth, scopes, cabeceras de seguridad, límite de cuerpo, rate limit), binding, paginación
        openapitest/       → valida en las pruebas HTTP cada respuesta contra docs/openapi.yaml
      security/            → verificación de JWT
      health/              → /health/live y /health/ready
      clock/               → reloj del sistema (implementa sharedport.Clock)
      telemetry/           → OpenTelemetry: proveedores, exportadores OTLP, propagación W3C
      outbox/              → transactional outbox: Writer (en la transacción) y Dispatcher (cmd/worker)
      idempotency/         → almacén de Idempotency-Key (PostgreSQL), con su doble y contrato en idempotencytest/
      external/            → clientes técnicos opcionales (no implementan puertos, ADR 0003):
                             awsconfig/, s3/, brevo/, asynq/, sqs/
      module/              → Deps: dependencias que cmd/api y cmd/worker entregan a cada módulo
  <modulo>/
    module.go              → raíz de composición del módulo: Register(router, deps)
    application/
      model/               → objetos de negocio con comportamiento e invariantes
      usecase/             → un archivo por caso de uso
      port/                → interfaces hacia el exterior (carpeta plana)
        porttest/          → dobles en memoria + suite de contrato de cada puerto (única subcarpeta permitida)
      dto/                 → Command, Query, Data, Summary
      mapper/              → model ↔ dto, conversión pura
      apperr/              → fallos de negocio con código del módulo
    infrastructure/
      persistence/         → XxxEntity (GORM), mapper Entity ↔ model, XxxRepositoryAdapter
      httpapi/             → handlers, Request/Response, rutas
      external/            → clientes de servicios externos que solo usa este módulo
      messaging/           → mensajes asíncronos: contrato (topic + payload), adapter que los publica
                             por el outbox y handlers que los procesan en cmd/worker
      file/                → lectores y escritores de archivos (csv/, txt/, json/…)
scripts/db/migrations/     → migraciones SQL
test/architecture/         → prueba de reglas de dependencia
test/migrations/           → reversibilidad de las migraciones
test/e2e/                  → binarios reales de la API y el worker contra PostgreSQL y Redis
test/load/                 → prueba de carga con k6 (make load)
tools/modulegen/           → generador de módulos (make module), con la plantilla mínima en template/
docs/openapi.yaml          → contrato HTTP (§9.4)
docs/deploy.md             → CD en AWS: infraestructura, OIDC, variables y operación
scripts/deploy/            → despliegue a ECS (con su prueba)
deploy/otel/               → configuración del ADOT Collector (CloudWatch)
docs/adr/                  → decisiones de arquitectura
```

**Carpetas siempre en singular**, en minúsculas y sin separadores. Una carpeta es un paquete Go y lleva el mismo nombre. Se crea una carpeta solo cuando hay algo que poner en ella: `external/`, `file/` y `apperr/` son opcionales.

**Subdivisión:** `port/`, `usecase/` y `persistence/` son planas. Si una supera ~15-20 archivos, se subdivide **por concepto** (`persistence/order/`, `persistence/invoice/`, con su Entity, mapper y adapter juntos), **nunca por tipo técnico** (`entity/`, `mapper/`, `adapter/`).

---

## 4. Reglas de dependencia (verificadas por `test/architecture`)

| # | Paquete | Puede importar | No puede importar |
|---|---|---|---|
| D1 | `internal/<m>/application/**` | biblioteca estándar, `github.com/google/uuid`, `internal/<m>/application/**`, `internal/shared/application/**` | cualquier otra cosa |
| D2 | `internal/shared/application/**` | biblioteca estándar, `github.com/google/uuid`, `internal/shared/application/**` | cualquier otra cosa |
| D3 | `internal/<m>/**` | `internal/<m>/**`, `internal/shared/**` | `internal/<n>/**` de otro módulo de negocio |
| D4 | `internal/shared/**` | `internal/shared/**` | cualquier módulo de negocio |
| D5 | `internal/<m>/infrastructure/httpapi` | `application/usecase`, `application/dto`, `shared/infrastructure/httpapi` | `infrastructure/persistence`, `infrastructure/external`, `application/port` |
| D6 | `internal/<m>/infrastructure/persistence` | `application/model`, `application/port`, `application/dto`, `application/apperr`, `shared/infrastructure/persistence` | `infrastructure/httpapi` |
| D7 | `cmd/**`, `internal/<m>` (raíz, `module.go`) | todo lo necesario para componer | — |

**Regla mental de D1/D2:** si una tecnología puede cambiar sin que cambie el negocio (framework, ORM, librería de tokens, SDK de nube), no pertenece a `application`. `uuid` se permite porque es un **tipo de valor** sin comportamiento técnico; llevarlo como `string` solo movería la validación a cada capa.

**Si una tarea parece requerir que un módulo use a otro:** lo correcto es exponerlo en `shared` (si cumple §5) o señalarlo al desarrollador. **Nunca** se crea el import cruzado. Los módulos tampoco se llaman por HTTP interno.

Agregar un paquete a la lista permitida de D1/D2 es una decisión de arquitectura: requiere un ADR nuevo y actualizar la prueba en el mismo cambio.

---

## 5. Aislamiento entre módulos y datos compartidos

### 5.1 `model` y `port` no se comparten

- Por defecto, `model` y `port` son **locales al módulo**. Dos módulos que leen la misma tabla por motivos distintos (p. ej. `security` autentica y `billing` solo resuelve quién registró una factura) tienen **cada uno su `port`, su `model` y su `Entity`**, solo con lo que necesitan.
- Un `model` o `port` sube a `shared/application` **solo** si ya existe duplicación real y verificada en 2+ módulos con el mismo concepto **y** el mismo propósito. Nunca de forma preventiva.

### 5.2 Entity local por módulo y un único dueño de escritura por tabla

1. **Cada módulo mapea su propia `XxxEntity`** en `infrastructure/persistence`, **solo con las columnas que usa**, aunque otro módulo mapee la misma tabla. Así se aplica el mínimo privilegio a nivel de datos: un módulo que solo resuelve un email no tiene el hash de la contraseña al alcance.
2. **Cada tabla tiene un único módulo dueño de la escritura** (INSERT/UPDATE/DELETE). Los demás solo leen. El dueño concentra las invariantes de la tabla, y la única forma de escribir pasa por sus casos de uso.
3. Una Entity de un módulo lector se documenta como `// Solo lectura: el dueño de escritura de <tabla> es el módulo <x>.` y su adapter no tiene métodos de escritura.
4. Si dos módulos necesitan **escribir** la misma tabla, primero se revisa si la responsabilidad está bien repartida (casi siempre la respuesta es mover la escritura al dueño). Una excepción por columna se documenta en la tabla de dueños, y se escribe solo con `UPDATE` puntuales de esas columnas.

### 5.3 Tabla de dueños

Se actualiza **en el mismo cambio** que agrega una tabla o un nuevo escritor.

| Tabla | Dueño de escritura | Módulos que solo leen |
|---|---|---|
| `examples` | `example` | — |
| `outbox` | `shared` (cada módulo escribe con `sharedoutbox.Writer` en su transacción; el despachador de `cmd/worker` marca los publicados) | — |
| `idempotency_keys` | `shared` (middleware `sharedhttp.Idempotent`) | — |

---

## 6. Capa `application`

### 6.1 `model`

- Objetos de negocio **con comportamiento**: `example.Activate(now)`, `user.CanLogin()`. No son bolsas de getters.
- Campos **privados**, getters públicos con el nombre del campo (`Name()`, no `GetName()`), sin tags.
- Dos formas de construir un objeto:
  - `NewXxx(...)` crea uno nuevo y **valida las invariantes**. Devuelve `(*Xxx, error)`, y el error es un `*result.Failure` de `apperr`.
  - `RestoreXxx(...)` lo rehidrata desde persistencia **sin revalidar las reglas de creación**. Solo lo usan los mappers de `persistence`.
- Los **conceptos con invariantes propias** se modelan como tipos (`ExampleName`, `Email`, `Money`), con un constructor que valida. No se exige la ceremonia completa de DDD (aggregates, domain events) mientras no haga falta.
- **Estados con significado de negocio → tipo con constantes y `ParseXxx(code)` estricto.** Un código desconocido devuelve error, nunca un valor por defecto. Las transiciones de estado son métodos que validan (`CanTransitionTo`). Nunca se compara `status == "active"` disperso en el código.
- El tiempo llega como parámetro (`now time.Time`) desde el caso de uso, que lo obtiene del puerto `Clock`. El model nunca llama a `time.Now()`.
- No se llama `entity`: ese nombre se reserva para `persistence`.

### 6.2 `usecase`

- Un archivo por caso de uso: `create_example.go`, con `CreateExampleUseCase`, `NewCreateExampleUseCase(...)` y un único método público `Execute`.
- Firma fija:

  ```go
  func (uc *CreateExampleUseCase) Execute(ctx context.Context, cmd dto.CreateExampleCommand) (result.Result[dto.ExampleData], error)
  ```

  - `error != nil` **solo** para fallos técnicos (base de datos caída, bug, dato corrupto).
  - Los fallos de negocio se devuelven como `result.Fail(...)` con `error == nil`.
- Las dependencias (puertos) se reciben por el constructor como interfaces. No hay estado mutable.
- **Orquesta:** valida la entrada que no cubre el model, carga lo necesario por los puertos, invoca el comportamiento del model, persiste y mapea la salida a `Data`.
- **Transacciones:** se usa `TransactionManager.WithinTransaction` cuando hay **más de una escritura** o una lectura-modificación-escritura que debe ser atómica. Una lectura simple o un único INSERT no la necesitan. **Dentro de la transacción no se hace I/O externo** (S3, correo, colas), porque el cierre se puede reintentar.
- **Avisar a otro proceso** (encolar una tarea, emitir un evento) se hace con un puerto del módulo (`ExampleEventPublisher`) implementado sobre el **outbox** (ADR 0006), llamado **dentro** de la misma transacción que el cambio. Nunca se publica directamente en una cola desde el caso de uso: o se pierde el aviso, o sale uno de un cambio que no se guardó. El evento se arma desde el estado **persistido** (con su versión nueva).
- No registra logs. El detalle técnico viaja en el `error` envuelto (`fmt.Errorf("crear example: %w", err)`), y lo registra una sola vez el borde HTTP.
- **Prohibido** convertir un error desconocido en fallo de negocio (`if err != nil { return result.Fail(...) }`). Se usa `result.FromError`, que solo convierte los `*result.Failure` y propaga todo lo demás como técnico (§8.2).
- No existe en `shared`.

### 6.3 `port`

- Interfaces que el caso de uso necesita para hablar con el exterior. Se definen **donde se consumen** (el módulo que las usa), nunca junto a la implementación.
- El nombre dice **qué** necesita el negocio, no **cómo** se resuelve: `CaptchaValidator`, no `GoogleRecaptchaPort`. Sin prefijo `I` ni sufijo `Interface`/`Port`.
- Sufijo según el tipo de colaborador:

  | Colaborador | Sufijo | Ejemplo | Implementación en |
  |---|---|---|---|
  | Base de datos | `Repository` | `ExampleRepository` | `infrastructure/persistence` |
  | Servicio externo, consulta o confirmación puntual sin estado propio | `Client` | `ObjectStorageClient` | `infrastructure/external` |
  | Sistema externo con un flujo cuyo estado el dominio **rastrea en sus tablas** | `Gateway` | `PaymentGateway` | `infrastructure/external` |
  | Envío de mensajes (correo, SMS, push) | `Sender` | `NotificationSender` | `infrastructure/external` |
  | Publicación de eventos o tareas (colas, buses) | `Publisher` | `TaskPublisher` | `infrastructure/external` |
  | Lectura o escritura de archivos | `Reader` / `Writer` | `PaymentFileReader` | `infrastructure/file` |

  **Client o Gateway:** ¿el dominio rastrea en sus propias tablas el estado de lo que pidió a ese sistema? Si es así, es `Gateway`. Si el sistema solo ejecuta o replica lo que el dominio ya decidió, es `Client`.
- **«No encontrado» se expresa explícitamente:** `FindByUUID(ctx, id) (*model.Example, bool, error)`. Nunca `nil, nil` ni una entidad vacía.
- **Los métodos de escritura devuelven el estado persistido:** `Create(ctx, e) (*model.Example, error)`, `Update(ctx, e) (*model.Example, error)`, con el id asignado y la versión nueva. El caso de uso responde con eso, no con el objeto que tenía en memoria.
- Cada método documenta en su comentario los **fallos anticipados** (`apperr`) que puede devolver.
- Los métodos reciben y devuelven tipos de `model`/`dto`, nunca tipos de GORM, gin o SDKs.
- Un listado paginado recibe `shareddto.PageQuery`, nunca `page, size int` sueltos.

### 6.4 `dto`

El sufijo indica el **rol**, no «esto es un DTO»:

| Rol | Sufijo | Ejemplo |
|---|---|---|
| Entrada de un caso de uso con efecto (crea, modifica, autentica) | `Command` | `CreateExampleCommand` |
| Entrada de un caso de uso que solo consulta | `Query` | `ListExamplesQuery` |
| Salida específica de un caso de uso | `Data` | `ExampleData` |
| Salida que proyecta varias fuentes sin ser una entidad completa | `Summary` / `View` | `ExampleSummary` |
| Hecho ocurrido que se comunica a otros procesos (evento) | participio, sin sufijo | `ExampleStatusChanged` |

- Structs con campos públicos, sin tags y sin métodos con lógica.
- Pueden ir en un solo archivo por entidad (`dto/example.go`) mientras sea legible; se separan por acción cuando crece.
- **Un dato que no se usa no se transporta.** Campos como autor (`CreatedBy`) o empresa (`TenantUUID`) solo existen si el caso de uso los usa. Siempre provienen del `Principal` autenticado, **nunca del cuerpo del request**.
- Los mecanismos genéricos (`Result`, `Notification`, `PageQuery`, `PageResult`, `Principal`) viven en `shared/application` **sin sufijo**: en Go, el nombre del paquete ya da el contexto (`result.Result`, `shareddto.PageQuery`).

### 6.5 `mapper`

- Funciones puras `model ↔ dto` del mismo módulo: `ToExampleData(e *model.Example) dto.ExampleData`.
- No llaman a puertos ni hacen I/O. Si se necesitan datos de catálogos, el caso de uso los carga y se los pasa.
- No llevan interfaz ni struct. Todos los mappers se escriben a mano, sin librerías de mapeo ni reflexión.

### 6.6 `apperr`

- Catálogo de **fallos de negocio anticipados** del módulo, como variables `*result.Failure`:

  ```go
  var ErrExampleNotFound = result.NewFailure(result.NotFound, "EXAMPLE_NOT_FOUND", "El ejemplo no existe.")
  ```
- Los usa el `model` (invariantes), el caso de uso y, cuando un adapter traduce un caso **previsible** (p. ej. un `23505` sobre `uq_examples_name`), también `persistence`.
- No se crean «por si acaso»: solo cuando un caso de uso necesita distinguir ese caso para dar un mensaje de negocio.

---

## 7. Capa `infrastructure`

### 7.1 `persistence`

- `XxxEntity`: struct GORM con tags y `TableName()`. Es **el único lugar del proyecto con sufijo `Entity`**. No tiene lógica.
- Mapper técnico `Entity ↔ model` en el mismo paquete: `toExampleModel(e ExampleEntity) (*model.Example, error)`. Usa `RestoreXxx` y los `ParseXxx` estrictos. **Si un valor persistido no es válido, devuelve un error técnico** (dato corrupto → 500 en el log). Nunca registra el error para luego seguir con un valor vacío.
- **Trampa:** los constructores del model (`NewExampleName`) devuelven un `*result.Failure`. Si el mapper lo **envuelve** con `%w`, `result.FromError` lo trata como fallo de negocio y responde **400 por un dato corrupto**. El mapper crea un error técnico nuevo que nombra la regla incumplida, sin envolver el `Failure`.
- `XxxRepositoryAdapter` implementa el puerto. Obtiene siempre la conexión con `sharedpersistence.DB(ctx, a.db)`, que usa la transacción del `context` si existe y **siempre aplica `WithContext(ctx)`**. **Nunca se usa `a.db` directamente.**
- «No encontrado»: `errors.Is(err, gorm.ErrRecordNotFound)` (con `First`/`Take`) o `RowsAffected == 0` (con `Scan`) → `nil, false, nil`.
- Las violaciones de constraint **previsibles** se traducen a un `apperr` comprobando el SQLSTATE y el nombre del constraint con `sharedpersistence.IsUniqueViolation(err, "uq_examples_name")`. Nunca se analiza el texto del mensaje.
- Un «borrar y recrear» filas hijas dentro de una transacción usa un único `DELETE ... WHERE` explícito antes de los `INSERT`.
- **Concurrencia optimista:** las tablas editables tienen `version INTEGER NOT NULL DEFAULT 1`. Un UPDATE lleva `WHERE uuid = ? AND version = ?`, `version = version + 1` y `RETURNING` (`clause.Returning{}`) para devolver el estado persistido. Si `RowsAffected == 0`, el adapter distingue «no existe» de «versión desfasada» y devuelve el `apperr` correspondiente (`…_NOT_FOUND` o `…_VERSION_CONFLICT`).
- La Entity desactiva los timestamps automáticos de GORM (`autoCreateTime:false`, `autoUpdateTime:false`): el tiempo lo decide el caso de uso con su `Clock`. `clock.System` trunca a microsegundos (la precisión de `TIMESTAMPTZ`), para que lo que devuelve una escritura sea idéntico a lo que se lee después.
- **Ids:** las tablas usan `id BIGSERIAL` (interno, para JOINs) y `uuid UUID UNIQUE` (externo). El `uuid` lo genera el model (`uuid.NewV7()`); el `DEFAULT` de la base de datos es solo una red de seguridad.
- `AutoMigrate` está prohibido: el esquema solo cambia por migraciones (§12).

### 7.2 `httpapi`

> En Java esta carpeta se llama `http`. En Go se llama `httpapi` para no ocultar `net/http`.

- Un struct de handler por entidad (`ExampleHandler`) que recibe los **casos de uso concretos** por constructor. Nunca conoce adapters ni puertos.
- Un archivo por acción, `<accion>_<entidad>.go`, con el método del handler y su `XxxRequest`. Las respuestas y su mapeo van en `<entidad>_response.go`. Las rutas van en `routes.go`.
- El handler solo hace esto:
  1. bind y validación de forma del request;
  2. arma el `Command`/`Query`, con el `Principal` cuando aplica;
  3. llama a `Execute`;
  4. `if !sharedhttp.Handle(c, res, err) { return }` (error técnico → `c.Error`, fallo → `WriteFailure`);
  5. responde con `sharedhttp.WriteData` / `WritePage`.
- Los tags de validación de forma (`binding:"required,max=150"`) solo van en el Request. Las invariantes de negocio se validan en el model.
- **Contrato JSON en `snake_case`**, declarado explícitamente con un tag `json:"..."` en **cada** campo de cada Request/Response. Los **query params** van en `camelCase` (`collectionTypeId`). La única excepción son `page` y `size`, que son de una sola palabra.
- **Enums en la respuesta:** el valor público se decide explícitamente en el mapeo (`string(status)` documentado como contrato, o un código numérico). Un cambio interno del tipo no debe romper el JSON en silencio.

### 7.3 `external`, `file` y `security`

- Implementan puertos que no son de persistencia. Nombre: la tecnología, si la comunica por sí sola (`S3ObjectStorageClient`, `BrevoNotificationSender`, `CSVPaymentFileReader`). Si no hay una tecnología nombrable, se usa el sufijo `Adapter`. **Nunca `Impl`.**
- **Integraciones opcionales (ADR 0003):** `shared/infrastructure/external` solo tiene **clientes técnicos** (`s3.Client`, `brevo.Client`, `asynq.Publisher`, `sqs.Consumer`), que no conocen puertos ni `apperr`. El puerto se define en el módulo que lo usa, y su adapter en `<m>/infrastructure/external` delega en el cliente y traduce sus errores centinela:

  ```go
  // internal/orders/infrastructure/external/s3_object_storage_client.go
  type S3ObjectStorageClient struct{ s3 *s3.Client }

  func (a *S3ObjectStorageClient) PresignInvoiceUpload(ctx context.Context, orderUUID uuid.UUID, size int64) (dto.UploadURL, error) {
      key, err := s3.NewKey("orders/invoices", "pdf", orderUUID) // prefijo fijo + uuid validado
      if err != nil {
          return dto.UploadURL{}, err
      }
      req, err := a.s3.PresignUpload(ctx, s3.Upload{Key: key, ContentType: "application/pdf", Size: size})
      if err != nil {
          return dto.UploadURL{}, fmt.Errorf("firmar subida de factura: %w", err) // técnico
      }
      return dto.UploadURL{URL: req.URL, Headers: req.Headers, ExpiresAt: req.ExpiresAt}, nil
  }
  ```

  Un `s3.ErrObjectNotFound` o un `*brevo.APIError` con `Rejected()` se traducen a un `apperr` del módulo **solo** si el caso de uso necesita distinguirlos (§6.6).
- Un adapter de un SDK externo captura el error **del cliente y del servicio** (en AWS SDK v2: cualquier `error` de la operación, sin filtrar solo por `smithy.APIError`). **No lo registra:** lo devuelve envuelto con la operación y el identificador afectado (técnico), o traducido a un `apperr` (anticipado). Lo registra una sola vez el borde (§11).
- Nunca se registran credenciales, URLs firmadas ni sus parámetros (`X-Amz-Credential`, `X-Amz-Signature`), ni cuerpos de peticiones, respuestas o mensajes que puedan contener datos personales.

### 7.3b `messaging`

- Un archivo por mensaje: el **topic** (`<modulo>:<evento>`, contrato estable), el **payload** con tags `json` en snake_case, el adapter `Outbox<Entidad>EventPublisher`, que implementa el puerto escribiendo en el outbox, y el handler de entrada que lo procesa en el worker.
- El handler es un adaptador de entrada: decodifica con `asynq.DecodePayload`, llama a un caso de uso y es **idempotente**, porque la entrega es al menos una vez. Usa la `version` del evento para descartar uno viejo.
- Un cambio incompatible del payload es un topic nuevo (`….v2`), nunca un cambio silencioso.
- Operación (alarmas, mensajes muertos, reencolado): `docs/outbox.md`.
- **El outbox es opcional:** con `REDIS_ENABLED=false`, `deps.Outbox` es `nil`, porque sin destino los mensajes se acumularían para siempre. Si los eventos del módulo son opcionales, el adapter los descarta (`NewExampleEventPublisher`); si son imprescindibles, el módulo lo exige con `module.MustHave`.

### 7.4 Raíz del módulo (`module.go`)

- `func Register(api *gin.RouterGroup, deps module.Deps)` arma el módulo en este orden: adapters → casos de uso → handler → rutas. Es el equivalente de `UseCaseConfig` en Spring. Delega en `RegisterWith(api, deps, Ports{…})`, que recibe los puertos: las pruebas HTTP le pasan dobles y así ejercitan el mismo cableado.
- Es el **único** lugar del módulo que conoce a la vez la aplicación y las implementaciones concretas.
- `cmd/api/modules.go` llama a `Register` de cada módulo. Un módulo recibe solo lo que usa de `Deps`.
- Los clientes opcionales de `Deps` (`Storage`, `Mail`, `Tasks`) son `nil` si su integración está desactivada. Un módulo que necesita uno lo exige en la primera línea de `Register`: `module.MustHave("orders", "S3_ENABLED", deps.Storage != nil)`.
- **Trabajo en segundo plano:** un módulo con tareas expone además `RegisterTasks(mux *asynq.ServeMux, deps module.Deps)`, que `cmd/worker/tasks.go` llama. El handler de una tarea es un adaptador de entrada, como un handler HTTP: decodifica el payload con `asynq.DecodePayload` (un payload inválido no se reintenta), llama al caso de uso y devuelve `error` solo ante un fallo técnico (se reintenta). Un fallo de negocio definitivo se devuelve envuelto con `asynq.SkipRetry`. El payload es un struct de infraestructura con tags `json`, nunca un dto. Los mensajes de SQS siguen las mismas reglas, y su handler debe ser idempotente (entrega al menos una vez).
- La inyección de dependencias es una decisión de **diseño** (desacoplar y probar sin infraestructura), no de rendimiento.

---

## 8. Resultados y errores

### 8.1 Tipos (`internal/shared/application/result`)

```go
type Category string // VALIDATION, UNAUTHORIZED, FORBIDDEN, NOT_FOUND, CONFLICT, BUSINESS_RULE, TECHNICAL
type Severity string // ERROR, WARNING, INFO

type Notification struct {
    Category Category
    Code     string // UPPER_SNAKE_CASE, en inglés, con el prefijo del módulo: EXAMPLE_NAME_TOO_SHORT
    Message  string // texto para el usuario, en español
    Severity Severity
    Field    string // opcional: campo del request (snake_case) que lo origina
}

type Failure struct{ /* Notification */ } // implementa error; se crea con NewFailure

type Result[T any] struct{ /* data, notifications, ok */ }

func Ok[T any](data T, extra ...Notification) Result[T]
func Fail[T any](n Notification, more ...Notification) Result[T]
func FromError[T any](err error) (Result[T], error) // *Failure → Fail; otro error → se propaga
```

- **Ningún nombre de `application` usa vocabulario HTTP** (`BadRequest`, `NotFoundError`, `StatusCode`). Solo existen categorías de negocio.
- El `code` es el contrato que consume el frontend: es **estable** y no se renombra. El `message` puede cambiar.

### 8.2 Fallo de negocio frente a fallo técnico

| Situación | Cómo se expresa | Quién decide el HTTP |
|---|---|---|
| Validación, regla de negocio, no encontrado, conflicto previsible, sin permiso | `*result.Failure` de `apperr` → `result.Fail` | `sharedhttp` según la categoría |
| Base de datos caída, timeout, dato corrupto, bug | `error` envuelto con contexto | middleware de errores → 500 `TECHNICAL` |

**La pregunta que separa ambos casos:** ¿el negocio puede explicarle esto al usuario con un mensaje claro y el usuario puede corregirlo? Si la respuesta es sí, es un `Failure`. Si no, es un `error`.

### 8.3 Categoría → HTTP (única tabla del proyecto, en `sharedhttp`)

| Categoría | HTTP |
|---|---|
| `VALIDATION` | 400 |
| `UNAUTHORIZED` | 401 |
| `FORBIDDEN` | 403 |
| `NOT_FOUND` | 404 |
| `CONFLICT` | 409 |
| `BUSINESS_RULE` | 422 |
| `TECHNICAL` | 500 |

En un resultado fallido con varias notificaciones manda la **primera** de severidad `ERROR`. Si se agrega una categoría, se agrega a esta tabla y a su prueba en el mismo cambio.

### 8.4 Errores técnicos que el borde HTTP sí reconoce

Todo lo que no aparece en esta tabla es 500 `UNEXPECTED_ERROR`. Un caso nuevo se agrega a esta tabla en el mismo cambio.

| Origen | HTTP | `code` | Nota |
|---|---|---|---|
| JSON mal formado, tipos incorrectos o texto que PostgreSQL no puede guardar (carácter NUL, UTF-8 inválido) | 400 | `INVALID_REQUEST_BODY` | Sin eco del cuerpo. `BindJSON` y `BindQuery` lo comprueban en todo lo decodificado |
| Fallo de tags `binding` | 400 | `REQUEST_FIELD_INVALID` | Una notificación por campo, con `field` |
| Path param o query param inválido (también texto que PostgreSQL no puede guardar) | 400 | `INVALID_REQUEST_PARAMETER` | |
| Cuerpo por encima del límite | 413 | `PAYLOAD_TOO_LARGE` | |
| Token ausente, inválido o vencido | 401 | `AUTH_TOKEN_INVALID` | Sin decir por qué falló |
| Falta un scope requerido | 403 | `AUTH_FORBIDDEN` | |
| Rate limit superado | 429 | `TOO_MANY_REQUESTS` | Cabecera `Retry-After` |
| `Idempotency-Key` ya usada con otro cuerpo | 422 | `IDEMPOTENCY_KEY_REUSED` | |
| Otra petición con la misma `Idempotency-Key` sigue en curso | 409 | `IDEMPOTENCY_IN_PROGRESS` | Cabecera `Retry-After` |
| Ruta inexistente / método no permitido | 404 / 405 | `ROUTE_NOT_FOUND` / `METHOD_NOT_ALLOWED` | |
| `23505` o `23503` **no traducidos** por el adapter | 409 | `DATA_CONFLICT` | **Red de seguridad:** se registra como `ERROR`, porque cada caso es una validación que falta |
| Otro SQLSTATE de integridad (NOT NULL, CHECK) | 500 | `UNEXPECTED_ERROR` | Es un bug, no un conflicto |
| `context.DeadlineExceeded`, o PostgreSQL abortó por `statement_timeout` (`57014`) o `lock_timeout` (`55P03`) | 503 | `SERVICE_TIMEOUT` | Se registra como `ERROR`: una consulta lenta o un bloqueo |
| Cliente canceló (`context.Canceled`) | — | — | Se registra en `INFO`; no se escribe respuesta |
| `panic` | 500 | `UNEXPECTED_ERROR` | Se registra con stack trace |

Nunca se exponen al cliente el nombre de un constraint, el SQL, el mensaje de un error técnico ni un stack trace. El detalle va al log, correlacionado por `request_id`.

---

## 9. Contrato HTTP

### 9.1 Sobre de respuesta (todas las respuestas JSON)

```json
{
  "data": { "uuid": "0192…", "name": "Demo", "status": "pending", "version": 1 },
  "page": null,
  "notifications": [],
  "request_id": "6f1c…"
}
```

- **Éxito:** `data` con el recurso y `notifications: []`, salvo que el caso de uso agregue un `INFO`/`WARNING`. No se agrega una notificación «SUCCESS».
- **Error:** `data: null` y al menos una notificación `{ "category", "code", "message", "severity", "field"? }`.
- `page` solo aparece en listados: `{ "page": 1, "size": 20, "total_elements": 57, "total_pages": 3 }`.
- `request_id` es el mismo valor que la cabecera `X-Request-ID` y que todos los logs de la petición.

### 9.2 Rutas y códigos

| Operación | Método y ruta | Éxito |
|---|---|---|
| Crear | `POST /api/v1/examples` | **201** + `Location: /api/v1/examples/{uuid}` |
| Obtener | `GET /api/v1/examples/{uuid}` | 200 |
| Listar | `GET /api/v1/examples?page=1&size=20&status=active` | 200 + `page` |
| Actualizar | `PUT /api/v1/examples/{uuid}` (con `version` en el cuerpo) | 200 |
| Acción de negocio | `POST /api/v1/examples/{uuid}/activate` | 200 |
| Eliminar o deshabilitar | `DELETE /api/v1/examples/{uuid}` | **204** sin cuerpo |

- Sustantivos en plural, sin verbos en la ruta. Las **intenciones de negocio** (activar, aprobar, anular) son sub-recursos `POST /{uuid}/<accion>`, no un PUT genérico que cambia `status`.
- Prefijo `/api/v1`. Un cambio incompatible del contrato crea `/api/v2`, no rompe `v1`.
- **Idempotencia (ADR 0007):** un `POST` que crea recursos admite `Idempotency-Key`: se declara en la ruta, después de auth (`g.POST("", write, deps.Idempotent, h.Create)`), y en OpenAPI (`#/components/parameters/IdempotencyKey`). Un reintento con la misma clave y el mismo cuerpo, durante 24 h, repite la respuesta sin ejecutar nada (`Idempotency-Replayed: true`). Los `5xx` no se guardan, para que el reintento se ejecute. Donde un duplicado sea inaceptable (pagos), se agrega además una clave natural con constraint único: una caída del proceso entre el commit y el guardado de la respuesta, o un plazo o una cancelación que llegan durante el commit, permiten una segunda ejecución (ADR 0007).
- Toda ruta lleva autenticación salvo `/health/*`, y cada ruta pública se justifica en `routes.go`. Las escrituras exigen además un scope (`example:write`).

### 9.4 OpenAPI

- `docs/openapi.yaml` es el contrato que consume el frontend (ADR 0004). **Se escribe a mano**, en el mismo cambio que la ruta, y se revisa en el PR como cualquier código. No se generan anotaciones (swag): el contrato se diseña, no se deduce del código.
- `cmd/api/openapi_test.go` falla si el documento no es OpenAPI válido, si una ruta registrada bajo `/api/v1` no está documentada o si una ruta documentada no existe. Cada operación tiene `operationId` único y documenta al menos `401`, `429`, `500` y `503`, que el borde HTTP puede responder en cualquier ruta.
- **Las respuestas reales se validan contra el contrato:** el `do` de las pruebas HTTP del módulo llama a `openapitest.ValidateResponse`, así que cada respuesta de cada prueba debe tener su estado documentado y cumplir su esquema. Los esquemas de respuesta y de request llevan `additionalProperties: false`: un campo nuevo sin documentar hace fallar la prueba.
- Los errores de negocio de cada respuesta se documentan con su `code` (`EXAMPLE_NAME_TAKEN`).
- Los bloques `# >>> module:<m>` / `# <<< module:<m>` y los marcadores `# @generator:*` los usa `make module`: no se borran.
- La API no sirve el documento (la CSP es `default-src 'none'`): se lee con un visor de OpenAPI.

### 9.3 Paginación

- Query params `page` (desde **1**) y `size` (por defecto 20, máximo 100). Un `page` < 1 se toma como 1; un `size` < 1 o > 100 es `VALIDATION`, igual que un `page` cuyo desplazamiento supera `MaxOffset` (100 000 filas): un `OFFSET` profundo recorre el índice en cada petición, y un recorrido completo se hace con filtros o con paginación por cursor. No se usan otros nombres (`limit`, `pageNumber`, `sizePerPage`).
- `shareddto.NewPageQuery(page, size)` es **el único lugar** que conoce la conversión: `Offset()` y `Limit()` para SQL, y `NewPageResult(items, total)` para la salida. Nadie suma ni resta 1 a mano.
- **El total se cuenta con tope:** un listado usa `sharedpersistence.CountUpTo(query, shareddto.MaxCount)`, nunca `Count`. Un `COUNT(*)` recorre la tabla en cada petición: con un millón de filas agota el pool a 300 rps y arrastra a todas las operaciones (`docs/load-baseline.md`). `total_elements` vale como máximo 100 100, y en el tope significa «100 100 o más».
- El orden de un listado es siempre explícito y estable (`ORDER BY created_at DESC, id DESC`).
- **Ordenamiento:** query param `sort=name,-createdAt` (`-` es descendente, máximo 3 campos). Todo campo permitido necesita un índice que empiece por su columna. Los campos son **lógicos** y en camelCase, declarados por el módulo en su dto (`dto.ExampleSortFields`). `sharedhttp.SortQuery` rechaza cualquier otro con 400 `field: sort`, y el adapter traduce cada uno a una columna con una lista cerrada. Nunca se interpola texto del cliente en un `ORDER BY`, y siempre se termina en `id DESC` como desempate.
- **Búsqueda de texto:** query param `q` (`sharedhttp.SearchQuery`), de 3 a 100 caracteres después de recortar los espacios. «Contiene», sin distinguir mayúsculas, con `%` y `_` escapados (literales), sobre un índice GIN de trigramas (`pg_trgm`). Con menos de 3 caracteres el índice no sirve y se recorrería la tabla entera.

---

## 10. Seguridad

- **JWT (solo verificación):**
  - algoritmo fijado en código (HS256; cambiarlo, p. ej. a RS256 con JWKS, es una decisión que requiere ADR);
  - el `sub` es el uuid del usuario, `tenant_uuid` es opcional, y `scope` lleva los scopes separados por espacios (como en OAuth 2.0);
  - se verifican `exp`, `nbf`, `iss` y `aud`, con un desfase de reloj de 30 s como máximo;
  - el secreto mide al menos 32 bytes;
  - un token inválido nunca produce un `Principal` parcial.
- **`Principal` (`shareddto`):**
  - contiene `UserUUID`, `TenantUUID` (opcional) y `Scopes`;
  - el middleware lo pone en el `context`, y el handler lo pasa al `Command`;
  - la autorización por scope se declara en la ruta: `sharedhttp.RequireScopes("example:write")`.
- **Middlewares globales, en orden** (`sharedhttp.NewEngine`):
  1. request id (se acepta el `X-Request-ID` del cliente solo si es `[A-Za-z0-9._-]{1,64}`);
  2. logging de acceso (envuelve a recovery para registrar el 500 de un panic);
  3. recovery;
  4. cabeceras de seguridad (`X-Content-Type-Options`, `X-Frame-Options: DENY`, `Referrer-Policy`, `Content-Security-Policy: default-src 'none'`, `Cache-Control: no-store`, y `Strict-Transport-Security` fuera de `develop`);
  5. CORS desde configuración (ningún origen por defecto, nunca `*`; la API usa Bearer, no cookies, así que no envía `Allow-Credentials`);
  6. límite de cuerpo (1 MiB por defecto);
  7. rate limit por IP;
  8. plazo de la petición (`HTTP_REQUEST_TIMEOUT`, menor que `HTTP_WRITE_TIMEOUT`);
  9. manejador de errores (el más interno).
- **IP del cliente:** solo se confía en `X-Forwarded-For` de los proxies listados en `HTTP_TRUSTED_PROXIES`. Fuera de develop/test es **obligatoria**: las subredes del balanceador, o `none` si no hay proxy delante. Se rechazan `0.0.0.0/0` y `::/0`, porque permitirían a cualquiera falsear su IP. Vacía detrás de un ALB, todos los clientes compartirían la IP del balanceador.
- **Rate limit:**
  - en memoria, por instancia. Con varias instancias, el límite real se aplica en el balanceador o WAF, o con un almacén compartido;
  - la clave es la IPv4, o el **/64** de una IPv6 (un cliente controla su /64 entero);
  - la memoria está acotada: LRU de 100 000 clientes con expiración de 10 minutos;
  - `/health/*` está exento: las sondas del balanceador salen de pocas IPs y un 429 sacaría de rotación una instancia sana;
  - `Retry-After` es el tiempo real hasta el siguiente token (`ceil(1/RPS)`).
- **Autorización por recurso (BOLA, OWASP API #1):** el scope de la ruta dice qué puede hacer un usuario, **no sobre qué registros**. Toda escritura sobre un registro comprueba además quién actúa, con una regla del model (`example.CanBeManagedBy`): el creador o quien tenga `<modulo>:admin`. El `Actor` lo arma el handler desde el `Principal`, nunca desde el cuerpo, y la regla se valida antes de escribir. Cada módulo decide y documenta quién puede **leer**: en `example` es un catálogo compartido (`example:read`), así que un 403 no revela nada. Si los datos son privados, la consulta filtra por dueño y un registro ajeno responde **404**, para no revelar que existe.
- **Secretos:**
  - nunca en archivos versionados; `.env.template` solo lleva marcadores (`change-me`);
  - en QA/producción, variables de entorno o un gestor de secretos.
- **Archivos (S3):**
  - las claves se construyen con identificadores **validados** (uuid) y un prefijo fijo;
  - se firman el tamaño y el tipo de contenido;
  - la URL firmada nunca se registra en el log.
- **Vulnerabilidades:** `govulncheck` falla el CI ante cualquier vulnerabilidad **alcanzable** (`make vuln` localmente); `gosec` corre dentro de `golangci-lint`. Una vulnerabilidad se corrige actualizando, no se silencia.
- **Imágenes:** el CI escanea `api`, `worker` y `migrate` con **Trivy** (`make scan`): falla ante HIGH/CRITICAL corregibles y publica el SBOM CycloneDX de cada una. Las imágenes que se publican en ECR llevan además SBOM y procedencia SLSA adjuntos (`--sbom --provenance`).
- **Herramientas de la cadena de suministro** (Trivy, actionlint, cosign) se ejecutan **fijadas por digest**, nunca por etiqueta (en marzo de 2026 las etiquetas de Trivy fueron reescritas con malware, GHSA-69fq-xp46-6x23). Al actualizarlas se verifica su firma con cosign (ver `Makefile`). Las GitHub Actions de terceros se reducen al mínimo y se fijan por SHA.
- **Versión de Go:** solo versiones con soporte de seguridad (las dos últimas mayores). Al salir una mayor nueva se actualiza el `toolchain`.

---

## 11. Configuración, logging y salud

- **Configuración:**
  - un solo struct tipado (`sharedconfig.Config`), cargado en `cmd/*` desde variables de entorno (`cmd/migrate` usa `LoadDatabase`, que solo exige `APP_ENV` y `DB_*`);
  - los valores sensibles son `config.Secret`: se imprimen como `[REDACTED]` con `fmt`, `slog` y JSON, y solo `Reveal()` da el valor real;
  - **se valida al arrancar y, si falla, el proceso termina** con la lista completa de lo que falta;
  - `.env` solo se lee con `APP_ENV=develop`;
  - no hay sufijos por entorno (`_D/_T/_Q/_P`): cada entorno define sus propias variables;
  - `cmd/worker` usa `LoadWorker`, que no exige HTTP ni JWT, y sí al menos una fuente de trabajo (`REDIS_ENABLED` o `SQS_ENABLED`);
  - los opcionales se activan explícitamente (`REDIS_ENABLED`, `S3_ENABLED`, `BREVO_ENABLED`, `SQS_ENABLED`) y, si están activos, sus variables pasan a ser obligatorias. Fuera de develop/test: `REDIS_PASSWORD` obligatoria, Redis con TLS salvo `REDIS_TLS=false` explícito (red privada), Brevo solo por `https`, y `AWS_PROFILE` rechazado (se usa el IAM Role).
- **Logging:**
  - `slog`; el logger de la petición sale del `context` (`logging.FromContext(ctx)`) con `request_id` y, si hay `Principal`, `user_uuid`;
  - claves en `snake_case`;
  - `application` no registra logs;
  - un error técnico se registra **una sola vez**, en el borde que lo recibe: el middleware HTTP, el `ErrorHandler` de Asynq o el consumidor de SQS. Los clientes y adapters no registran (§7.3);
  - en el worker, cada tarea y cada mensaje llevan en el `context` un logger con `task_type`/`task_id` o `message_id`; nunca su payload;
  - nunca se registran cuerpos de peticiones, tokens, contraseñas, cadenas de conexión ni URLs firmadas.
- **Observabilidad (OpenTelemetry, ADR 0005):**
  - se activa con `OTEL_ENABLED=true`. El destino y el muestreo usan las variables estándar (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG`, `OTEL_RESOURCE_ATTRIBUTES`). Apagado, los proveedores son no-op;
  - la aplicación exporta por OTLP/HTTP a un colector. En AWS, el ADOT Collector como **sidecar** de la task de ECS (`deploy/otel/adot-collector.yaml`): trazas a X-Ray, métricas a CloudWatch (EMF, namespace `MyApp`). El IAM Role de la task necesita permisos de X-Ray y de CloudWatch Logs;
  - se instrumenta **solo en bordes y adapters**: HTTP (`otelgin`), SQL (plugin propio de GORM en `sharedpersistence`), AWS (instrumentación nativa del SDK con `awsconfig.TracerProvider/MeterProvider`), HTTP saliente (`otelhttp`), Asynq y SQS (`sharedasynq`, `sharedsqs`). `application` no importa OpenTelemetry;
  - un cliente HTTP saliente nuevo usa `otelhttp.NewTransport`, y un cliente de AWS nuevo se crea a partir de `awsconfig.Load` y recibe `awsconfig.TracerProvider()`/`MeterProvider()` en sus opciones;
  - el contexto de traza se propaga en W3C: en HTTP, en las **cabeceras** de la tarea de Asynq y en los atributos `traceparent`/`tracestate` de un mensaje de SQS. Un productor de SQS que publique para este worker debe enviarlos;
  - **nunca va a un span:** valores de parámetros SQL (solo el SQL con `$1…`), cuerpos, cabeceras, query strings, tokens ni URLs firmadas;
  - un log emitido con `…Context(ctx, …)` lleva `trace_id` y `span_id`. En el borde y en los handlers de tareas se usan siempre las variantes con `Context`;
  - las métricas tienen dimensiones acotadas (ruta registrada, método, estado, tipo de tarea): nunca un uuid, un usuario o un valor libre como atributo, porque cada combinación es una serie de CloudWatch que se paga;
  - en develop, `develop.yml` envía las trazas a Jaeger (`http://localhost:16686`).
- **Salud** (GET y HEAD):
  - `GET /health/live` responde 200 si el proceso atiende, sin comprobar dependencias, para no reiniciar en bucle si cae la base de datos;
  - `GET /health/ready` comprueba la base de datos con una **conexión propia, fuera del pool de la aplicación** (`sharedpersistence.HealthChecker`), y Redis si está activo, con un timeout de 2 s. Con el pool saturado en un pico, la instancia sigue «lista»: lo que se mide es si la base responde, no si hay conexiones libres. Responde 200 `{"status":"UP"}` o 503 `{"status":"DOWN"}`, **sin detalles**; el motivo va al log;
  - el balanceador apunta a `/health/ready`;
  - no se crean otros endpoints de salud;
  - **el worker** no recibe tráfico: sirve solo `/health/live` en loopback (`WORKER_HEALTH_ADDR`, por defecto `127.0.0.1:8081`), que usa `worker healthcheck` y el `HEALTHCHECK` de su imagen. Cada bucle (Asynq, despachador del outbox, consumidor de SQS) registra un **latido** mientras avanza, aunque su dependencia falle, y la sonda responde 503 si alguno se colgó. Una dependencia caída no reinicia el worker, porque reiniciarlo no la arregla;
  - si cambia la ruta, se actualiza la infraestructura (target group, smoke tests) en el mismo cambio, nunca con un alias de compatibilidad silencioso.
- **Apagado ordenado:**
  - en la API, `SIGTERM` primero **drena**: `/health/ready` pasa a 503 (sin consultar dependencias; `/health/live` sigue en 200), se desactivan los keep-alive y se espera `HTTP_DRAIN_DELAY` (5 s; 0 en develop/test) para que el balanceador retire la instancia mientras el servidor sigue atendiendo. Después deja de aceptar conexiones, espera a las peticiones en curso (`HTTP_SHUTDOWN_TIMEOUT`), cierra las colas, vacía la telemetría y al final cierra la base de datos, porque la última recolección de métricas la consulta. Una segunda señal termina el proceso sin esperar;
  - `HTTP_DRAIN_DELAY` debe cubrir lo que tarda el balanceador en dejar de enviar tráfico (intervalo de la sonda × umbral de fallos, o el *deregistration delay*), y el orquestador debe esperar más que `HTTP_DRAIN_DELAY + HTTP_SHUTDOWN_TIMEOUT` antes de matar el proceso (`stop_grace_period`, `stopTimeout` de ECS, `terminationGracePeriodSeconds`);
  - en el worker, `SIGTERM` deja de tomar trabajo, espera a las tareas en curso (`WORKER_SHUTDOWN_TIMEOUT`), termina la publicación en curso del outbox (devuelve el resto de la tanda) y termina el lote de SQS en curso. El lote se procesa **en paralelo**, cada mensaje con un plazo contado **desde la recepción** (`SQS_VISIBILITY_TIMEOUT` menos un margen para el borrado), así que el apagado tarda como máximo una visibilidad. El orquestador debe esperar más que `max(WORKER_SHUTDOWN_TIMEOUT, SQS_VISIBILITY_TIMEOUT)`. Una segunda señal termina el proceso sin esperar.

---

## 12. Persistencia: transacciones y migraciones

### 12.1 `TransactionManager`

```go
// internal/shared/application/port
type TransactionManager interface {
    WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}
```

- La implementación con GORM pone la transacción en el `context` y `sharedpersistence.DB(ctx, db)` la recupera.
- **Solo reintenta errores transitorios** (`40001` serialización, `40P01` deadlock), con un backoff que **respeta la cancelación del `context`**. Un `*result.Failure` o cualquier otro error **nunca** se reintenta.
- Si `fn` devuelve un error (incluido un `Failure`), se hace rollback y el error se propaga tal cual.

### 12.2 Migraciones (golang-migrate)

```
scripts/db/migrations/
  000001_create_examples.up.sql
  000001_create_examples.down.sql
```

- Nombre: `{6 dígitos}_{descripcion_snake_case}.{up|down}.sql`. La descripción dice **qué** cambia.
- Siempre se crea el par `up`/`down`. Si no es reversible, el `down` es `SELECT 1;` con un comentario que explica por qué.
- **Nunca se edita una migración ya aplicada** en un entorno compartido: se crea una nueva. El CI lo verifica (`scripts/ci/check-migrations.sh`, `make check-migrations`): falla si un cambio modifica, borra o renombra una migración ya integrada, si a una nueva le falta su par, o si su número no es mayor que el último integrado.
- **Plazos de sesión:** la conexión de la aplicación fija `statement_timeout` (`DB_STATEMENT_TIMEOUT`, 30 s), `lock_timeout` (`DB_LOCK_TIMEOUT`, 10 s) e `idle_in_transaction_session_timeout` (`DB_IDLE_IN_TRANSACTION_TIMEOUT`, 60 s). Protegen el pool aunque un `context` no tenga plazo (worker, scripts). `cmd/migrate` usa `MigrationDSN`, sin `statement_timeout` pero con `lock_timeout`: una migración puede tardar, pero no debe bloquear el tráfico esperando un lock (si lo agota, se reintenta en un momento de menos carga). Con PgBouncer en modo *transaction*, que rechaza parámetros de arranque, se ponen en 0 y se definen en el rol (`ALTER ROLE … SET statement_timeout`).
- **Una sola fuente de verdad para los valores por defecto:** el `DEFAULT` del SQL y el model coinciden. Los tags GORM **no** declaran `default:` de negocio.
- Constraints con nombre explícito (`uq_examples_name`, `fk_orders_customer`, `ck_examples_status`), porque los adapters los referencian.
- Las migraciones **no** corren al arrancar el servidor: `make migrate-up` es un paso explícito del despliegue, antes de la nueva versión del binario.
- La prueba de integración aplica `up → down → up` sobre PostgreSQL real.

```bash
make migrate-create NAME=add_orders_table   # crea el par con el siguiente número
make migrate-up                              # aplica las pendientes
make migrate-down                            # revierte la última
make migrate-version                         # versión actual
make migrate-force VERSION=3                 # solo en emergencias
```

---

## 13. Pruebas

| Nivel | Qué se prueba | Cómo | Dónde |
|---|---|---|---|
| Model | Invariantes, transiciones de estado, `ParseXxx` | Tablas de casos (`t.Run`) | `application/model/*_test.go` |
| Caso de uso | Orquestación, fallos de negocio, que lo técnico se propague | Dobles de los puertos escritos a mano | `application/usecase/*_test.go` |
| HTTP | Contrato: estado, sobre, `code`, cabeceras (`Location`) | `httptest` contra el **router real** del módulo (`RegisterWith`, el mismo cableado de `Register`) con casos de uso reales y puertos falsos | `infrastructure/httpapi/*_test.go` |
| Contrato de puerto | Que el doble y el adapter real se comportan igual | `porttest.RunXxxRepositoryContract`, ejecutada contra el doble (unitaria) **y** contra el adapter (integración) | `application/port/porttest` |
| Persistencia | SQL real: mapeo, «no encontrado», constraints, versión, transacción del ctx | `pgtest` + migraciones reales, etiqueta `//go:build integration` | `infrastructure/persistence/*_integration_test.go` |
| Migraciones | Que cada migración es reversible | `up → down → up` con `pgtest` | `test/migrations` |
| Clientes externos | Contrato con el servicio (firma, cabeceras, errores) sin red, y Asynq contra Redis real | Credenciales estáticas (S3), `httptest` (Brevo), API falsa (SQS); `TEST_REDIS_ADDR` + etiqueta `integration` (Asynq) | `shared/infrastructure/external/*` |
| Contrato HTTP | `docs/openapi.yaml` válido y con las mismas rutas que el router real (§9.4) | `kin-openapi` + rutas de Gin | `cmd/api/openapi_test.go` |
| Respuestas contra el contrato | Cada respuesta de las pruebas HTTP cumple su esquema estricto y su estado está documentado | `openapitest.ValidateResponse` en el `do` de las pruebas | `shared/infrastructure/httpapi/openapitest` |
| Generador | Nombres, traducción de identificadores, plantillas, ediciones de archivos compartidos | Unitarias; en CI, `make module` (mínimo con nombre compuesto, y `FULL=1`) + `go test` del módulo generado | `tools/modulegen` |
| Punta a punta | El sistema armado: binarios reales de `cmd/api` y `cmd/worker` con PostgreSQL y Redis reales. Evento de la API al worker en una traza, `Idempotency-Key` concurrente, autorización, drenaje con SIGTERM, sonda del worker, configuración inválida | Etiqueta `integration`; Redis con `TEST_REDIS_ADDR` o Docker. Lo que dependa de `example` se omite tras `make remove-example` | `test/e2e` |
| Carga | Latencia y saturación con el volumen de datos esperado. No corre en el CI: los runners no dan cifras comparables | k6 (`make load`), resultados en `docs/load-baseline.md` | `test/load` |
| Colector de telemetría | Que cada métrica declarada en `deploy/otel/adot-collector.yaml` sale como métrica EMF con sus dimensiones | Imagen ADOT real y CloudWatch Logs falso (`make lint-ci`) | `deploy/otel/adot-collector_test.sh` |
| Arquitectura | Reglas D1-D7 | `go list -json` (imports directos de producción) | `test/architecture` |

- No se usan librerías de mocking. Los dobles de un puerto viven en `port/porttest` (un doble en memoria con la **misma semántica** que el adapter real), junto con la suite de contrato que ambos cumplen. Los dobles puntuales de una sola prueba van en su `_test.go`.
- `porttest` es código de `application`: solo biblioteca estándar (sin `testify`).
- **`pgtest`:** si existe `TEST_DATABASE_URL` (el *service container* del CI), la usa; si no, levanta un `postgres:16-alpine` desechable con el CLI de Docker. Cada paquete de integración declara `func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }` y vacía sus tablas con `pgtest.Truncate`. Los paquetes se ejecutan con `-p 1` porque comparten la base. (No se usa testcontainers-go: junto con golang-migrate exige Go 1.25.)
- Las reglas D1-D7 aplican al código de producción. Los `_test.go` de `application` pueden importar `testify`.
- La prueba de arquitectura incluye una **meta-prueba** (`TestRulesDetectViolations`): cada regla tiene casos que deben fallar. Una regla nueva se agrega con sus casos.
- No se usa `sqlmock`, porque compara el texto del SQL y no el comportamiento.
- Cada fallo de negocio de `apperr` tiene al menos una prueba que lo provoca.
- Una prueba HTTP de error comprueba el **cuerpo** (`code`, `category`), no solo el estado.
- `make test` corre unitarias, HTTP, OpenAPI y de arquitectura, sin Docker. `make test-integration` agrega las de integración (Docker o `TEST_DATABASE_URL`; Redis con `TEST_REDIS_ADDR`).
- **Cobertura informativa, sin umbral:** el CI publica el total, el detalle por paquete y la tendencia frente a `main` (`make cover` en local). Un porcentaje obligatorio premia pruebas que ejecutan sin comprobar. Sirve para ver qué quedó sin probar, no como objetivo.
- **`make check` es la puerta de calidad** antes de abrir un PR: `fmt-check`, `vet`, `lint`, `test` y `vuln`, lo mismo que el job «Test & Lint» del CI.

---

## 14. Nomenclatura Go

> **Idioma:** todo identificador Go (carpeta, paquete, tipo, campo, función, variable) va en **inglés**. Los comentarios y los mensajes al usuario van en español. Las columnas SQL de bases de datos existentes pueden estar en español; las de tablas nuevas, en inglés y en `snake_case`.

| Elemento | Convención | Ejemplo |
|---|---|---|
| Paquete | = carpeta, una palabra en minúsculas | `usecase`, `persistence`, `httpapi` |
| Alias de import de `shared` | `shared<paquete>` | `sharedhttp`, `shareddto`, `sharedport`, `sharedpersistence` |
| Archivo | `snake_case.go` | `create_example.go`, `example_repository_adapter.go` |
| Model | Nombre de dominio, sin sufijo | `Example`, `ExampleName`, `ExampleStatus` |
| Constructor / rehidratación | `NewXxx` / `RestoreXxx` | `NewExample`, `RestoreExample` |
| Caso de uso | `<Accion><Entidad>UseCase` + `Execute` | `CreateExampleUseCase.Execute` |
| Puerto | Rol, sin `I`/`Interface`/`Port` | `ExampleRepository`, `NotificationSender` |
| Adapter | Tecnología o `<Puerto>Adapter`, nunca `Impl` | `ExampleRepositoryAdapter`, `S3ObjectStorageClient` |
| Entity GORM | `<Nombre>Entity`, único en el proyecto | `ExampleEntity` |
| DTO | Sufijo de rol | `CreateExampleCommand`, `ExampleData` |
| Request / Response | `<Accion><Entidad>Request` / `<Entidad>Response` | `CreateExampleRequest`, `ExampleResponse` |
| Handler | `<Entidad>Handler`, un método por acción | `ExampleHandler.Create` |
| Fallo de negocio | `Err<Concepto>` en `apperr` | `ErrExampleNotFound` |
| Código de fallo | `<MODULO>_<MOTIVO>` | `EXAMPLE_INVALID_TRANSITION` |
| Scope | `<modulo>:<read\|write\|admin>` | `example:write` |
| Variables | `ctx`, `c` (gin), `cmd`, `q`, `res`, `err` | |

Acciones estándar: `Create`, `Get`, `List`, `Update`, `Delete`, `BulkCreate`, `Search` y las intenciones de negocio (`Activate`, `Approve`, `Cancel`…).

---

## 15. Ejemplo de referencia: módulo `example`

El módulo `internal/example` es la implementación de referencia de este estándar: muestra todos los patrones, incluidos los opcionales. `make module` genera por defecto un CRUD más simple, y con `FULL=1` una copia de este módulo (§16). Un cambio del estándar se refleja primero aquí.

### 15.1 Archivos

```
internal/example/
  module.go                            → Register: adapters → casos de uso → handler → rutas
  application/
    apperr/errors.go                   → EXAMPLE_NAME_* | _NOT_FOUND | _NAME_TAKEN | _VERSION_CONFLICT | _INVALID_TRANSITION | _STATUS_INVALID | _DELETE_ACTIVE | _FORBIDDEN
    model/
      example.go                       → Example: NewExample, RestoreExample, Rename, Activate, Deactivate
      example_name.go                  → ExampleName: 2-150 caracteres, sin espacios en los extremos
      example_status.go                → pending → active ⇄ inactive; ParseExampleStatus estricto
    port/
      example_repository.go            → ExampleRepository + ExampleFilter
      example_event_publisher.go       → ExampleEventPublisher (eventos por el outbox)
      porttest/memory.go               → InMemoryExampleRepository
      porttest/contract.go             → RunExampleRepositoryContract
      porttest/events.go               → RecordingExampleEventPublisher
    dto/example.go                     → Commands, Queries, ExampleData
    mapper/example_mapper.go           → ToExampleData
    usecase/
      create_example.go  get_example.go  list_examples.go  update_example.go  change_example_status.go  delete_example.go
  infrastructure/
    persistence/
      example_entity.go  example_entity_mapper.go  example_repository_adapter.go
    messaging/
      status_changed.go                → topic example:status_changed, payload, OutboxExampleEventPublisher, handler
    httpapi/
      example_handler.go  routes.go  example_response.go
      create_example.go  get_example.go  list_examples.go  update_example.go  change_example_status.go  delete_example.go
scripts/db/migrations/000001_create_examples.{up,down}.sql
scripts/db/migrations/000003_add_examples_name_search.{up,down}.sql  → índice de trigramas para q
scripts/db/migrations/000006_add_examples_updated_index.{up,down}.sql → índice para sort=updatedAt
```

| Ruta | Scope | Éxito | Fallos de negocio |
|---|---|---|---|
| `POST /api/v1/examples` | `example:write` | 201 + `Location` | 400 `EXAMPLE_NAME_*`, 409 `EXAMPLE_NAME_TAKEN` |
| `GET /api/v1/examples/{uuid}` | `example:read` | 200 | 404 `EXAMPLE_NOT_FOUND` |
| `GET /api/v1/examples?page&size&status&q&sort` | `example:read` | 200 + `page` | 400 (`status`, `size`, `q`, `sort`) |
| `PUT /api/v1/examples/{uuid}` (`name`, `version`) | `example:write` | 200 | 400, 403 `EXAMPLE_FORBIDDEN`, 404, 409 `EXAMPLE_VERSION_CONFLICT` / `_NAME_TAKEN` |
| `POST /api/v1/examples/{uuid}/activate` · `/deactivate` | `example:write` | 200 | 403 `EXAMPLE_FORBIDDEN`, 404, 422 `EXAMPLE_INVALID_TRANSITION` |
| `DELETE /api/v1/examples/{uuid}` | `example:write` | **204** | 403 `EXAMPLE_FORBIDDEN`, 404, 409 `EXAMPLE_VERSION_CONFLICT`, 422 `EXAMPLE_DELETE_ACTIVE` |

### 15.2 Fragmentos clave

```go
// application/usecase/create_example.go
func (uc *CreateExampleUseCase) Execute(ctx context.Context, cmd dto.CreateExampleCommand) (result.Result[dto.ExampleData], error) {
    name, err := model.NewExampleName(cmd.Name)
    if err != nil {
        return result.FromError[dto.ExampleData](err) // Failure → 400
    }
    example, err := model.NewExample(name, cmd.CreatedBy, uc.clock.Now())
    if err != nil {
        return result.FromError[dto.ExampleData](err)
    }
    created, err := uc.repo.Create(ctx, example)
    if err != nil {
        // Un duplicado llega como apperr.ErrExampleNameTaken (409); cualquier otro error es técnico (500).
        return result.FromError[dto.ExampleData](fmt.Errorf("crear example: %w", err))
    }
    return result.Ok(mapper.ToExampleData(created)), nil
}
```

```go
// infrastructure/persistence/example_repository_adapter.go
func (a *ExampleRepositoryAdapter) Update(ctx context.Context, e *model.Example) (*model.Example, error) {
    var en ExampleEntity
    res := sharedpersistence.DB(ctx, a.db).
        Model(&en).
        Clauses(clause.Returning{}).
        Where("uuid = ? AND version = ?", e.UUID(), e.Version()).
        Updates(map[string]any{
            "name": e.Name().String(), "status": string(e.Status()),
            "updated_at": e.UpdatedAt().UTC(), "version": gorm.Expr("version + 1"),
        })
    if res.Error != nil {
        if sharedpersistence.IsUniqueViolation(res.Error, "uq_examples_name") {
            return nil, apperr.ErrExampleNameTaken
        }
        return nil, fmt.Errorf("actualizar example: %w", res.Error)
    }
    if res.RowsAffected == 0 {
        return nil, a.whyNotUpdated(ctx, e.UUID()) // ErrExampleNotFound o ErrExampleVersionConflict
    }
    return toExampleModel(en)
}
```

```go
// infrastructure/httpapi/create_example.go
type CreateExampleRequest struct {
    Name string `json:"name" binding:"required,max=150"`
}

func (h *ExampleHandler) Create(c *gin.Context) {
    var req CreateExampleRequest
    if !sharedhttp.BindJSON(c, &req) {
        return // ya respondió 400/413
    }
    res, err := h.create.Execute(c.Request.Context(), dto.CreateExampleCommand{
        Name:      req.Name,
        CreatedBy: sharedhttp.MustPrincipal(c).UserUUID, // del token, nunca del body
    })
    if !sharedhttp.Handle(c, res, err) {
        return // 500 técnico o fallo de negocio, ya respondido
    }
    data := res.Data()
    c.Header("Location", "/api/v1"+basePath+"/"+data.UUID.String())
    sharedhttp.WriteData(c, http.StatusCreated, toExampleResponse(data), res.Notifications())
}
```

```go
// module.go
func Register(api *gin.RouterGroup, deps module.Deps) {
    repo := persistence.NewExampleRepositoryAdapter(deps.DB)
    handler := httpapi.NewExampleHandler(
        usecase.NewCreateExampleUseCase(repo, deps.Clock),
        usecase.NewGetExampleUseCase(repo),
        usecase.NewListExamplesUseCase(repo),
        usecase.NewUpdateExampleUseCase(repo, deps.Clock),
        usecase.NewChangeExampleStatusUseCase(repo, deps.Clock),
    )
    httpapi.RegisterRoutes(api, handler, deps.Auth)
}
```

---

## 16. Crear un módulo nuevo

```bash
make module NAME=orders ENTITY=order [PLURAL=orders] [LABEL=pedido]            # CRUD mínimo
make module NAME=purchase_orders ENTITY=purchase_order LABEL="orden de compra"  # nombres compuestos
make module NAME=orders ENTITY=order FULL=1                                     # copia de internal/example
```

| | Mínimo (por defecto) | Completo (`FULL=1`) |
|---|---|---|
| **Fuente** | Plantilla embebida en `tools/modulegen/template` | Copia de `internal/example` |
| **Incluye** | `Create`, `Get`, `List`, `Update`, `Delete`; autorización por recurso, concurrencia optimista, `Idempotency-Key`, pruebas y contrato | Lo anterior, más eventos por el outbox, máquina de estados, intenciones de negocio, `sort` y `q` |
| **Tras `make remove-example`** | Funciona | No disponible |

**Se empieza por el mínimo.** Lo demás se agrega cuando hace falta, copiando el patrón de `internal/example`, que sigue siendo la referencia del estándar.

1. El generador crea el módulo, su migración con el siguiente número, su registro en `cmd/api/modules.go` (y en `cmd/worker/tasks.go` con `FULL=1`), sus bloques en `docs/openapi.yaml` y su fila en la tabla de dueños (§5.3). Queda compilando y con sus pruebas en verde; el CI lo comprueba en cada cambio.
2. Se adapta a la entidad real: model e invariantes, migración, contrato OpenAPI y los mensajes de `apperr` (género y redacción).
3. `make check` y `make test-integration` en verde.

- **Nombres en `snake_case`.** Cada contexto toma su forma: `purchase_orders` da el paquete `purchaseorders`, el tipo `PurchaseOrder`, la variable `purchaseOrder`, la tabla `purchase_orders`, la ruta `/purchase-orders`, los scopes `purchase_orders:write` y los códigos `PURCHASE_ORDERS_…`.
- **Un cambio del estándar se aplica en los dos lugares:** `internal/example` y la plantilla mínima. Sus pruebas se ejecutan en el CI, pero las convenciones que no se prueban hay que revisarlas a mano.
- **Quitar el módulo de referencia:** `make remove-example` borra su código, sus migraciones, sus registros, sus bloques de OpenAPI y su fila de dueños. Si sus migraciones ya se aplicaron en un entorno compartido, no se borran: se crea una migración nueva que elimina la tabla (§12.2).

---

## 17. Uso de la plantilla

```bash
cp -r plantilla_backend_go/ mi-proyecto/ && cd mi-proyecto/ && rm -rf .git && git init
# module path: go.mod, imports, .golangci.yml (depguard, goimports) y este documento
grep -rl 'myapp.v1.0.0' --exclude-dir=.git . | xargs sed -i 's|myapp.v1.0.0|mimodulo.v1.0.0|g'
cp .env.template .env      # completar con valores reales de desarrollo; nunca se versiona

# Con Docker (no requiere Go local)
make up                                                   # API con hot reload + Postgres + Redis
docker compose -f develop.yml run --rm app_migrate        # migraciones

# Sin Docker para la API (Postgres propio; en .env: DB_HOST=localhost)
make migrate-up && make run
```

### Docker

**Un solo `Dockerfile`** en la raíz, con un target por uso (ADR 0002):

| Target | Uso | Imagen |
|---|---|---|
| `dev` | Desarrollo con Air; el código se monta como volumen | golang:alpine, usuario del host |
| `api` | Servidor HTTP | distroless `nonroot`, ~50 MB (incluye los SDK opcionales y OpenTelemetry, ADR 0003 y 0005), `HEALTHCHECK` = `/app/api healthcheck` |
| `migrate` | Migraciones: paso explícito del despliegue, antes de la API | distroless `nonroot` + `scripts/db/migrations` |
| `worker` | Procesos en segundo plano (Asynq, outbox, SQS) | distroless `nonroot`, sin HTTP externo, `HEALTHCHECK` = `/app/worker healthcheck` (latidos en loopback) |

| Archivo | Ambiente | Características |
|---|---|---|
| `develop.yml` | Desarrollo | Air con hot reload, Postgres, Redis, RedisInsight, Asynqmon; puertos solo en `127.0.0.1`; corre con el UID/GID del host |
| `test.yml` | Pruebas | Postgres efímero (tmpfs) + contenedor que corre la suite de integración: `make test-docker` |
| `quality.yml` | QA | Imágenes `api`/`worker`/`migrate`; `read_only`, `cap_drop: ALL`, `no-new-privileges`; base de datos externa; `ENV_FILE` (por defecto `.env.qa`) |
| `production.yml` | Producción | Igual que QA, sin herramientas de administración; `ENV_FILE` (por defecto `.env.prod`) |

Reglas:
- **Imágenes fijadas por versión y digest** (`imagen:x.y.z@sha256:…`), también en compose y CI. Dependabot propone las actualizaciones. Para actualizar a mano: `docker buildx imagetools inspect imagen:tag`.
- **Nada de secretos en imágenes:** `.dockerignore` excluye `.env*`. Los `.env.qa`/`.env.prod` nunca se versionan (`.gitignore`).
- **Runtime sin shell:** las imágenes distroless no tienen `sh`, `wget` ni `curl`. No se agregan para «depurar»: se usan los logs y `/health/*`.
- **Despliegue en AWS (`docs/deploy.md`):**
  - `.github/workflows/deploy.yml` **construye una vez y promueve**: un push a `main` con el CI en verde publica en ECR (etiqueta = SHA de 12 caracteres) y despliega en QA; producción se despliega a mano con una etiqueta ya probada en QA, **sin reconstruir**;
  - `scripts/deploy/ecs-deploy.sh` ejecuta primero las migraciones como task (si fallan, los servicios no se tocan), después actualiza los servicios cambiando solo la imagen de la app, y exige que **esa** versión quede `COMPLETED` (si ECS la revierte, el pipeline falla). Volver a una versión anterior es una ejecución con `rollback`, que no ejecuta migraciones;
  - `scripts/deploy/ecr-publish.sh` publica solo las imágenes que faltan, porque las etiquetas son inmutables;
  - credenciales por **OIDC**, con un rol por entorno cuyo `sub` exige `refs/heads/main`;
  - las migraciones son *forward-only*: la versión anterior debe tolerar el esquema nuevo (cambios incompatibles en dos despliegues).
- **Despliegue con compose** (EC2): `docker compose -f production.yml run --rm app_migrate` y después `up -d`. `stop_grace_period` es mayor que `HTTP_DRAIN_DELAY + HTTP_SHUTDOWN_TIMEOUT`.
- **AWS:** en `develop` se monta `~/.aws` (solo lectura) y se usa `AWS_PROFILE`. En QA/producción, IAM Role; nunca se montan credenciales.

---

## 18. Checklist antes de generar o aprobar código

1. ¿Representa una regla o concepto de negocio? → `application/model` o `application/usecase` del módulo, sin librerías externas.
2. ¿Necesita hablar con algo externo? → puerto en `application/port` del módulo que lo consume, implementación en su `infrastructure`.
3. ¿Lleva gin, gorm, jwt, un SDK o tags `json`/`gorm`/`binding`? → no puede estar en `application`.
4. ¿Importa algo de otro módulo de negocio? → **prohibido**. Se usa `shared` (§5) o se replantea el diseño.
5. ¿Un `model`/`port` a `shared` porque «podría» repetirse? → no.
6. ¿Escribe en una tabla? → comprobar en §5.3 que este módulo es el dueño.
7. ¿El handler conoce un adapter o un puerto? → corregirlo: solo conoce casos de uso.
8. ¿Un `if err != nil { return result.Fail(...) }` con un error que no es de `apperr`? → corregirlo: `result.FromError`.
9. ¿Una consulta sin `sharedpersistence.DB(ctx, …)`? → corregirlo.
10. ¿Un «no encontrado» como entidad vacía o `nil, nil`? → `(nil, false, nil)`.
11. ¿Un mapper que registra un error y sigue con un valor vacío? → devolver el error.
12. ¿Un campo JSON sin tag `snake_case` explícito, o un query param que no es `camelCase`? → corregirlo.
13. ¿Un listado con `page, size int` sueltos, con `page - 1` a mano o con un `Count` sin tope? → `shareddto.PageQuery` y `sharedpersistence.CountUpTo`.
14. ¿Un endpoint de salud nuevo, una ruta con verbo o un 200 al crear? → §9.2 y §11.
15. ¿Una ruta nueva o un cambio de contrato sin actualizar `docs/openapi.yaml`? → documentarlo en el mismo cambio (§9.4).
16. ¿Un dato sensible en un log o en un archivo versionado? → eliminarlo.
17. ¿Un fallo de negocio nuevo sin prueba que lo provoque? → agregarla.
18. ¿Una regla de este documento cambió? → actualizar el documento y, si es una decisión de arquitectura, crear un ADR, en el mismo cambio.
19. ¿Una escritura sobre un registro sin comprobar quién actúa? → regla en el model y `Actor` desde el `Principal` (§10).
20. ¿El cambio completa una mejora de `docs/roadmap.md`? → marcarla y moverla a «Hechas» con su commit, en el mismo cambio.
