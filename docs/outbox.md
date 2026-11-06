# Operación del outbox

Diseño y razones en el [ADR 0006](adr/0006-outbox-transaccional.md). Aquí, cómo operarlo.

## Alarmas recomendadas (CloudWatch, namespace `MyApp`)

| Métrica | Alarma | Qué significa |
|---|---|---|
| `outbox.oldest_pending_age` | > 300 s durante 5 min | El despachador no está publicando: worker caído, Redis inalcanzable o `REDIS_ENABLED=false` en el worker |
| `outbox.dead` | > 0 | Hay mensajes que agotaron sus 20 intentos **con el destino respondiendo**, y esperan intervención. Una caída del destino no cuenta: se ve en `outbox.oldest_pending_age` y en `outbox.publish.count` con `outcome=unavailable` |
| `outbox.pending` | crece sin bajar | El ritmo de publicación no alcanza: más réplicas del worker o un `Batch` mayor |

La API y el worker emiten estas métricas, así que `outbox.oldest_pending_age` sigue informando aunque el worker haya caído. Conviene que la alarma trate los datos faltantes como incumplimiento (*treat missing data as breaching*).

Con `REDIS_ENABLED=false` el outbox está desactivado: no se escribe nada, y la API lo avisa en el log al arrancar.

## Consultas

```sql
-- Pendientes y su antigüedad
SELECT topic, count(*), now() - min(created_at) AS mas_antiguo
FROM outbox WHERE published_at IS NULL AND dead_at IS NULL GROUP BY topic;

-- Mensajes muertos y su último error
SELECT uuid, topic, attempts, last_error, created_at FROM outbox
WHERE dead_at IS NOT NULL ORDER BY created_at;
```

## Reencolar un mensaje muerto

Una vez corregida la causa (normalmente, el destino estaba caído o mal configurado):

```sql
UPDATE outbox SET dead_at = NULL, attempts = 0, available_at = now(), last_error = NULL
WHERE uuid = '<uuid>';
```

Si murieron muchos por la misma causa, por ejemplo un destino que aceptaba conexiones pero rechazaba escrituras (Redis sin memoria), se reencolan juntos:

```sql
-- Revisar antes el resultado de la consulta de mensajes muertos y acotar por topic y fecha.
UPDATE outbox SET dead_at = NULL, attempts = 0, available_at = now(), last_error = NULL
WHERE dead_at IS NOT NULL AND topic = '<topic>' AND dead_at >= '<inicio del incidente>';
```

Si el mensaje ya no tiene sentido, por ejemplo porque el evento fue superado por otro más nuevo, se documenta el motivo y se deja muerto. No se borra: es el registro de que no se entregó.

## Garantías para quien consume

- **Al menos una vez:** el handler debe ser idempotente. El `TaskID` de Asynq es el id del mensaje y deduplica republicados dentro de una ventana de 1 h. Fuera de ella, el evento trae su `version` para descartar uno viejo.
- **Sin orden garantizado** entre mensajes. Si el orden importa para una misma entidad, se compara la `version`.
- **El contrato** (topic y payload) vive en `internal/<modulo>/infrastructure/messaging`. Un cambio incompatible es un topic nuevo (`orders:status_changed.v2`), no un cambio silencioso del payload.
