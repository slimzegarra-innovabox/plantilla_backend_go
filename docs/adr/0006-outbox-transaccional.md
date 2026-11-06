# ADR 0006 — Outbox transaccional para publicar después del commit

- **Estado:** aceptado
- **Fecha:** 2026-11-05

## Contexto

Un caso de uso que guarda un cambio y además avisa a otro proceso (encolar una tarea, emitir un evento) tiene tres opciones ingenuas, y las tres fallan:

- **Publicar después del commit:** si el proceso cae entre el commit y la publicación, el aviso se pierde.
- **Publicar antes del commit:** si después falla el commit, sale un aviso de un cambio que no existe.
- **Publicar dentro de la transacción:** lo prohíbe §6.2. El `TransactionManager` puede reintentar el cierre, lo que publicaría dos veces, y la cola no participa del rollback.

## Decisión

1. **Tabla `outbox`** (migración `000002`), cuyo dueño de escritura es `shared`. El caso de uso escribe el mensaje con `sharedoutbox.Writer` **en su misma transacción**: el cambio y el aviso se confirman o se descartan juntos. `Writer` rechaza escribir fuera de una transacción (`ErrNoTransaction`).
2. **Despachador en `cmd/worker`** (`sharedoutbox.Dispatcher`, activo con `REDIS_ENABLED`):
   - reserva pendientes con un único `UPDATE … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED) RETURNING`, que deja un *lease* e incrementa el intento. Varias réplicas pueden correr a la vez sin tomar el mismo mensaje;
   - publica **fuera de toda transacción**, cada mensaje con su plazo;
   - si la publicación funciona, marca el mensaje como publicado;
   - si falla y el destino responde (`Publisher.Ping`), el problema es del mensaje: reintenta con backoff exponencial (de 1 s a 5 min) y tras 20 intentos lo marca como **muerto**, y requiere intervención;
   - si falla y el destino no responde, es una **caída**: no gasta el intento, devuelve la tanda y espera 30 s (`UnavailableDelay`). Si `MaxAttempts` contara las caídas, una de Redis de una hora mataría todo lo pendiente. Se decide con `Ping` y no por el texto del error porque un error mal clasificado como caída bloquearía la cola para siempre;
   - el lease se reserva para toda la tanda, pero se publica en serie: no se empieza una publicación después de `Lease - PublishTimeout`, y lo no publicado se devuelve sin gastar intentos. Si no, otra réplica retomaría mensajes aún sin publicar. Al apagar ocurre lo mismo;
   - si el despachador cae con mensajes tomados, otro los retoma al vencer el lease.
3. **Entrega al menos una vez.** El id del mensaje (uuid v7) se usa como `TaskID` de Asynq, con 1 h de retención: un republicado dentro de esa ventana se descarta. Fuera de ella, el consumidor debe ser idempotente; el evento lleva la `version` para descartar uno viejo. No hay garantía de orden entre mensajes.
4. **Traza:** el `Writer` guarda el contexto W3C de la petición en `headers`. El despachador lo restaura al publicar, así el consumidor queda en la misma traza que la petición original.
5. **Patrón por módulo** (como ADR 0003): el puerto lo define el módulo (`ExampleEventPublisher`) y su adapter en `infrastructure/messaging` escribe en el outbox. El mismo paquete define el contrato (topic `<modulo>:<evento>` y payload con tags `json`) y el handler que lo consume en el worker, así productor y consumidor no se desalinean.
6. **Observabilidad:** métricas `outbox.pending`, `outbox.oldest_pending_age` (la alarma clave), `outbox.dead` y `outbox.publish.count`, más un span por publicación.

## Consecuencias

- Un caso de uso que emite eventos siempre usa `WithinTransaction`, porque son dos escrituras.
- La tabla crece con el historial. El despachador borra los publicados de más de 7 días, en tandas.
- El destino hoy es Asynq. Otro destino (SQS, EventBridge) es otro `outbox.Publisher`, sin cambios en los módulos.
- **El outbox es opcional:** con `REDIS_ENABLED=false` no hay despachador que lo vacíe, así que `deps.Outbox` es `nil` y no se escribe nada. La API avisa en el log al arrancar. Un módulo decide si sus eventos se descartan o si los exige con `module.MustHave`.
- **Las métricas de atraso las emiten la API y el worker:** si solo las emitiera el worker, al morir este la alarma se quedaría sin datos justo cuando hace falta.
- Un mensaje muerto no se pierde: queda en la tabla con su `last_error` hasta que alguien lo reencola o lo descarta (`docs/outbox.md`).
