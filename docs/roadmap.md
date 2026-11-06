# Hoja de ruta de mejoras

Mejoras de la plantilla identificadas en las revisiones de arquitectura posteriores a la migración al estándar hexagonal (2026-11-05).

**Cómo se usa:**
- Al completar una mejora, en el **mismo commit** que la implementa:
  - se marca su casilla;
  - se mueve a «Hechas» con el commit y la fecha;
  - si cambió una regla, se actualiza `CLAUDE.md` y, si es una decisión de arquitectura, se escribe su ADR.
- Una mejora nueva entra en «Pendientes» con su prioridad y su criterio de «hecho».
- **Prioridad:** alta = antes del primer proyecto en producción; media = cuando aparezca el primer caso que la necesite; baja = deuda conocida.

---

## Pendientes

### Prioridad alta

- [ ] **17. Usarla de verdad**
  - **Problema:** nunca corrió en producción. El CD y el envío a CloudWatch no tocaron AWS.
  - **Hecho cuando:** un módulo real está desplegado en QA con el pipeline y sus trazas se ven en CloudWatch; lo aprendido queda aquí como pendientes.
- [ ] **18. Aligerar la plantilla (resto)**
  - **Problema:** CLAUDE.md tiene unas 900 líneas, y no se sabe aún qué sobra en `shared`.
  - **Hecho cuando:** CLAUDE.md es sensiblemente más corto y lo que no se use queda fuera. Se hace **después** de la 17, con lo aprendido.
- [ ] **2b. Firma de imágenes con cosign** (resto opcional del punto 2)
  - **Hecho cuando:** el job `publish` firma cada imagen por digest con cosign *keyless* (OIDC de GitHub), y el despliegue verifica la firma antes de actualizar ECS.


### Prioridad media

- [ ] **7. Multiempresa**
  - **Problema:** `TenantUUID` llega en el token, pero ninguna consulta lo usa.
  - **Hecho cuando:**
    - se decide entre aplicarlo y quitarlo;
    - si se aplica: hay un filtro obligatorio en los adapters, Row-Level Security en PostgreSQL como segunda barrera, una prueba de contrato «la empresa A no ve datos de la B» y un ADR.
- [ ] **10. JWT con RS256/JWKS**
  - **Problema:** con HS256, cualquiera que tenga el secreto puede *emitir* tokens.
  - **Hecho cuando:**
    - se verifica RS256/ES256 con llaves obtenidas por JWKS y caché con rotación;
    - HS256 queda solo como opción explícita;
    - hay un ADR.

### Prioridad baja

- [ ] **11. Rate limit distribuido:** hoy es por instancia, así que con N réplicas el límite real se multiplica por N. **Hecho cuando** el límite se aplique en Redis o en el WAF, con la decisión documentada.
- [ ] **12. Auditoría:** **hecho cuando** exista un registro de quién cambió qué y cuándo, para las entidades que lo exijan, con un patrón reutilizable.
- [ ] **15. Proteger `main`:** exigir PR y CI en verde para fusionar.
  - **Bloqueado:** GitHub no permite reglas de rama en repositorios privados con plan gratuito.
  - **Opciones:** GitHub Pro o de organización, o hacer el repositorio público (antes, la acción pendiente de rotar el secreto).


---

## Acciones pendientes fuera del código

- [ ] **Rotar el `JWT_SECRET` antiguo:** sigue en el historial de git y es obligatorio antes de hacer público el repositorio.
- [ ] **Configurar el CD**: crear la infraestructura y las variables de `docs/deploy.md`, y personalizar el `sub` OIDC del repositorio para que incluya la rama.
- [ ] **Agregar `HTTP_TRUSTED_PROXIES` a los `ENV_FILE` de QA y producción:** son las subredes del ALB, o `none`. Sin esa variable, la versión `3d007cf` o posterior no arranca.

---

## Hechas

| Mejora | Commit | Fecha |
|---|---|---|
| Apagado sin cortes detrás del balanceador (drenaje con `/health/ready` en 503, `HTTP_DRAIN_DELAY`) | `76e9c0a` | 2026-11-05 |
| Plazos de sesión en PostgreSQL (`statement_timeout`, `lock_timeout`, `idle_in_transaction_session_timeout`) y su error como 503 | `76e9c0a` | 2026-11-05 |
| Acciones del CI en Node 24, fijadas por SHA | `76e9c0a` | 2026-11-05 |
| Observabilidad con OpenTelemetry → ADOT → X-Ray / CloudWatch, logs con `trace_id` (ADR 0005) | `a6f6401` | 2026-11-05 |
| Pruebas de carga con k6 (`make load`) y línea base en `docs/load-baseline.md`; `CountUpTo` corrige el colapso por `COUNT(*)` con un millón de filas | `8db3019` | 2026-11-05 |
| Cobertura informativa en el CI con las pruebas de integración: total, detalle por paquete, tendencia frente a `main` y reporte HTML (`make cover`) | `fd1b6cb` | 2026-11-05 |
| `make module` mínimo por defecto (plantilla embebida, CRUD de ~1 150 líneas; copia completa con `FULL=1`) y nombres compuestos; funciona tras `make remove-example` | `ea91256` | 2026-11-05 |
| Prueba de punta a punta en el repo (`test/e2e`, binarios reales en CI); pruebas HTTP por `RegisterWith`; pruebas del bucle del despachador y su limpieza | `f3c0b8e` | 2026-11-05 |
| Autorización por recurso (`CanBeManagedBy`, 403 `EXAMPLE_FORBIDDEN`, `example:admin`); outbox desactivado sin Redis; métricas de atraso también desde la API y en el colector | `2a791c4` | 2026-11-05 |
| `Idempotency-Key` en los POST (ADR 0007): reserva atómica en PostgreSQL, respuesta repetida sin ejecutar, 422 con otro cuerpo, 409 en curso, 5xx libera la clave | `8c9512c` | 2026-11-05 |
| Respuestas validadas contra OpenAPI: `openapitest` en cada prueba HTTP, esquemas estrictos, `429` y `503` documentados en todas las operaciones | `e423082` | 2026-11-05 |
| Módulo de referencia completo: `DELETE` (204, regla «no se elimina un activo», versión), `sort` con campos permitidos y desempate estable, búsqueda `q` con trigramas y comodines literales; el generador copia todas las migraciones del módulo | `cc8b030` | 2026-11-05 |
| Healthcheck del worker: latidos de Asynq, outbox y SQS; `/health/live` en loopback y `HEALTHCHECK` de la imagen; una dependencia caída no lo reinicia | `12484db` | 2026-11-05 |
| Migraciones inmutables: el CI falla si se edita, borra o renombra una migración integrada, si falta un par o si el número no es mayor (`make check-migrations`) | `12484db` | 2026-11-05 |
| Outbox transaccional (ADR 0006): `Writer` en la transacción, despachador con `SKIP LOCKED`, lease, backoff y mensajes muertos, traza preservada; evento `status_changed` en `example` y generador actualizado | `f87f8b8` | 2026-11-05 |
| Pipeline de CD: publicar en ECR y desplegar en ECS por OIDC (QA automático, producción por promoción sin reconstruir), migraciones antes de los servicios, `docs/deploy.md` | `cb6d288` | 2026-11-05 |
| Escaneo de imágenes con Trivy fijado por digest y firma verificada, SBOM CycloneDX en CI, SBOM y procedencia en las imágenes publicadas; actionlint y shellcheck en CI | `cb6d288` | 2026-11-05 |
| Correcciones de la revisión independiente: SQS en paralelo, `HTTP_TRUSTED_PROXIES` obligatoria, rate limit acotado e IPv6 /64, health check fuera del pool, validaciones de configuración, límite de paginación | `3d007cf` | 2026-11-05 |
| Métricas en CloudWatch: `service.name` como dimensión en el colector, con prueba contra la imagen ADOT real | `2cd2906` | 2026-11-05 |
| Métricas ante una base caída y al apagar: un fallo del atraso del outbox ya no descarta todas las métricas, y la telemetría se vacía antes de cerrar la base | `e1a3752` | 2026-11-05 |
| Volver a una versión anterior que no conoce la última migración: `rollback` en el workflow, sin ejecutar migraciones | `b5cbb05` | 2026-11-05 |
| El despliegue falla si ECS revirtió la versión: comprueba la revisión y `rolloutState`, en lugar de `services-stable` | `f383adb` | 2026-11-05 |
| Repetir un despliegue o reintentar una publicación a medias: `ecr-publish.sh` publica solo las imágenes que faltan | `b799e7a` | 2026-11-05 |
| Outbox: el lease cubre la publicación real de la tanda, sin duplicados entre réplicas, y una caída del destino no gasta intentos ni mata mensajes; reencolado masivo documentado | `1454cc9` | 2026-11-05 |
| Limpieza del outbox y de idempotencia al ritmo de escritura, y métrica del atraso solo con índices parciales (de Seq Scan de 82 ms a 0,12 ms con 2M de filas) | `751ac1b` | 2026-11-05 |
| Texto que PostgreSQL no puede guardar (carácter NUL, UTF-8 inválido) responde 400, no 500: `BindJSON` y `BindQuery`, también en los módulos generados | `603d860` | 2026-11-05 |
| SQS: el borrado tiene su propio plazo, y el del proceso se cuenta desde la entrega. Exigir visibilidad mayor que `SQS_WAIT_TIME` deja de hacer falta, porque el long polling ya no consume el plazo | `1731e93` | 2026-11-05 |
| Listados: índice para `sort=updatedAt` (272 ms con 90 MB en disco a 52-88 ms en la página más profunda, con 1M de filas) y mínimo de `q` validado tras recortar (`sharedhttp.SearchQuery`) | `0a551a1` | 2026-11-05 |
| Idempotencia: reserva con dueño, y duración derivada de `HTTP_REQUEST_TIMEOUT` en lugar de validarla. ADR 0007 corregido: la doble ejecución también puede darse si el plazo o la cancelación llegan durante el commit | `2b1c8fb` | 2026-11-05 |
| Generador: nombres que romperían la compilación se rechazan (lista medida generando cada candidato), `-plural keys` funciona, `remove-example` deja el árbol en verde y el CI lo comprueba | `90a1408` | 2026-11-05 |
| CD y trazas: espera propia de las migraciones (hasta 1 hora), `IMAGE_PREFIX` y `CONTAINER_*` llegan desde el workflow, y `CountUpTo` ya no genera un span falso | `1b58cd5` | 2026-11-05 |
