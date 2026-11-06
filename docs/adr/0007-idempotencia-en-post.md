# ADR 0007 — Idempotency-Key en los POST que crean recursos

- **Estado:** aceptado
- **Fecha:** 2026-11-05

## Contexto

Un cliente con la red inestable no sabe si su `POST` llegó: si reintenta, puede crear un duplicado. Si no reintenta, puede perder la operación. Los `PUT` ya están protegidos por la versión y las intenciones (`/activate`) por la máquina de estados, pero un `POST` de creación no tiene nada que lo distinga de otro legítimo.

## Decisión

1. **Cabecera `Idempotency-Key`, opcional**, en las rutas que la declaran (`g.POST("", write, deps.Idempotent, h.Create)`). Sin cabecera, la petición sigue su curso normal. El middleware `sharedhttp.Idempotent` va **después de auth**, porque el alcance de la clave es **usuario + método + ruta**.
2. **Comportamiento:**

   | Situación | Respuesta |
   |---|---|
   | Primera vez | Se ejecuta y se guarda la respuesta |
   | Reintento, mismo cuerpo | La misma respuesta, sin ejecutar, con `Idempotency-Replayed: true` |
   | Misma clave, otro cuerpo (SHA-256) | 422 `IDEMPOTENCY_KEY_REUSED` |
   | La primera sigue en curso | 409 `IDEMPOTENCY_IN_PROGRESS` con `Retry-After` |
   | Clave mal formada | 400 `INVALID_REQUEST_PARAMETER` |

3. **Qué se guarda:**
   - Las respuestas `2xx` y `4xx`: un fallo de negocio también es un resultado, y repetirlo es lo correcto.
   - Un `5xx`, un error técnico o un `panic` **liberan la clave**: no hay resultado que repetir y el reintento debe ejecutarse de verdad.
   - El `request_id` del cuerpo repetido se reemplaza por el de la petición actual, para que el sobre siga coincidiendo con `X-Request-ID` y con los logs (§9.1).
4. **En PostgreSQL**, tabla `idempotency_keys` (dueño: `shared`), y no en Redis, que es opcional. La reserva es un único `INSERT … ON CONFLICT … WHERE`, atómico entre réplicas. Una reserva vencida (el proceso cayó) o una clave vencida (24 h) se reutilizan. `cmd/api` borra las vencidas al arrancar y cada hora.
5. **La reserva tiene dueño y dura más que la petición.** Dura lo mayor entre 1 min y `HTTP_REQUEST_TIMEOUT` + 30 s. Si aun así un handler ignora el `context` y la supera, un reintento toma la reserva. Por eso `Complete` y `Abandon` solo actúan si la reserva sigue siendo de quien la pidió (`owner`): la petición vencida no pisa la respuesta de la nueva, que puede traer otro cuerpo, ni libera su reserva.

## Consecuencias

- **Límite conocido: la idempotencia no es exactamente una vez.** El guardado de la respuesta no está en la transacción del caso de uso, porque el middleware no la conoce. Un reintento **se ejecuta de nuevo** aunque el cambio ya esté confirmado en estos casos:
  - el proceso cae entre el commit y el guardado: la clave queda reservada y se reutiliza al vencer;
  - el plazo de la petición o la cancelación del cliente llegan durante el `INSERT` o el `COMMIT`: la base puede haber confirmado, pero el handler recibe un error de `context`, responde un error técnico y libera la clave;
  - falla el guardado de la respuesta (la base no responde tras el commit): la clave se reutiliza al vencer.

  Las ventanas son pequeñas, pero no dependen solo de una caída. Donde un duplicado sea inaceptable (pagos), además hay que tener una clave natural con constraint único en la tabla de negocio: el reintento recibe entonces un conflicto, no un duplicado.
- La tabla guarda los cuerpos de respuesta durante 24 h, con la misma sensibilidad que los datos que contienen. No se registran en logs.
- Una respuesta de más de 1 MiB no se guarda: se libera la clave y se avisa en el log. No es lo esperado en un `POST` de creación.
