# ADR 0003 — Integraciones opcionales: clientes técnicos compartidos y puertos en el módulo

- **Estado:** aceptado
- **Fecha:** 2026-11-05

## Contexto

La plantilla ofrece cuatro integraciones opcionales: almacenamiento de objetos (S3), correo transaccional (Brevo), tareas en segundo plano (Asynq sobre Redis) y consumo de colas (SQS). En el estándar anterior:

- cada integración era un «provider» compartido con su propia interfaz, que los módulos usaban directamente;
- leía variables de entorno por su cuenta (`os.Getenv` disperso), con valores por defecto silenciosos (`redis:6379`, `us-east-1`);
- registraba datos sensibles: la `X-Amz-Credential` de la URL firmada, los cuerpos de los mensajes de SQS y la respuesta completa de Brevo (que puede incluir correos);
- el consumidor de SQS se arrancaba y detenía por endpoints HTTP;
- las claves de S3 se armaban con el nombre de archivo y una carpeta enviados por el cliente.

Al migrar, había que decidir **dónde vive el puerto**. El estándar dice que un puerto se define en el módulo que lo consume (§6.3) y que `shared` no importa módulos (D4). Si el adapter compartido implementara el puerto directamente, el puerto tendría que estar en `shared/application`, con sus tipos: eso es compartir un `port` de forma preventiva, lo que §5.1 prohíbe.

## Decisión

1. **`internal/shared/infrastructure/external/` contiene clientes técnicos, no adapters de puertos:**
   - `s3`, `brevo`, `asynq`, `sqs` y `awsconfig` (la configuración común del SDK);
   - su API usa sus propios tipos (`s3.Upload`, `brevo.TemplateEmail`) y errores centinela (`s3.ErrObjectNotFound`, `*brevo.APIError`);
   - no conocen ningún puerto ni ningún `apperr`.
2. **El puerto y su adapter viven en el módulo que los consume:**
   - el puerto en `<m>/application/port`, con el vocabulario del negocio (`ObjectStorageClient`, `NotificationSender`);
   - el adapter en `<m>/infrastructure/external`, que delega en el cliente compartido y traduce sus errores centinela a los `apperr` del módulo.
3. **Activación explícita:**
   - cada integración se activa con `<X>_ENABLED=true` y, solo entonces, sus variables se vuelven obligatorias y se validan al arrancar;
   - desactivada, su cliente es `nil` en `module.Deps`;
   - un módulo que la necesita lo declara en su `Register` con `module.MustHave`, y el proceso no arranca a medio configurar.
4. **Reglas de seguridad aplicadas en el código:**
   - **S3:**
     - las claves solo se construyen con `s3.NewKey`, a partir de un prefijo fijo y uuids, y todo método las valida;
     - la URL de subida firma el tipo de contenido, el tamaño exacto y el cifrado SSE;
     - el paquete no registra nada.
   - **Brevo:** los errores llevan solo el estado HTTP y el código de Brevo, nunca los cuerpos.
   - **SQS:**
     - el consumidor corre solo en `cmd/worker`, sin control por HTTP, y se detiene con la señal del proceso;
     - nunca registra el cuerpo de un mensaje;
     - al apagar, termina el lote en curso.
   - **Redis:**
     - contraseña obligatoria fuera de develop/test;
     - TLS activo por defecto fuera de develop/test. Solo se desactiva de forma explícita, para un Redis en la red privada de compose.
   - **AWS:** `AWS_PROFILE` solo se admite en develop. En QA/producción se usa el IAM Role.
5. **Los clientes no registran errores:**
   - envuelven el error con la operación y la clave, y lo registra **una sola vez** el borde que lo recibe: el middleware HTTP, el `ErrorHandler` de Asynq o el consumidor de SQS;
   - esto reemplaza la redacción anterior de §7.3 («el adapter registra el error una vez con su contexto»), que con el borde HTTP registrando también producía logs duplicados.

## Consecuencias

- Usar una integración cuesta un adapter delgado más por módulo. A cambio, el puerto habla el idioma del negocio, el doble de pruebas no depende del SDK, y la traducción de errores a `apperr` queda donde se conoce el significado.
- Los binarios enlazan los SDK aunque la integración esté desactivada: la imagen `api` pasa de ~17 MB a ~36 MB. Se acepta, porque la activación es configuración y no compilación. Un proyecto que nunca use S3 puede borrar el paquete y su cableado en `cmd/`.
- Asynq se prueba contra un Redis real (`TEST_REDIS_ADDR`, etiqueta `integration`), y S3 se prueba firmando sin red con credenciales estáticas. Brevo y SQS se prueban con dobles (`httptest`, una API falsa).
