# ADR 0002 — Versión de Go soportada e imágenes de contenedor

- **Estado:** aceptado
- **Fecha:** 2026-11-05

## Contexto

En la fase 4 de la migración, `govulncheck` encontró **51 vulnerabilidades de la biblioteca estándar alcanzables** desde el código, más 9 en módulos (`x/net`, `x/text`, `pgx`, AWS SDK). La causa principal era el toolchain: **Go 1.24 ya no recibe parches de seguridad** (Go soporta las dos últimas versiones mayores; hoy 1.26 y 1.27). Unas 35 de esas vulnerabilidades solo están corregidas en 1.25 o posterior, varias en el servidor HTTP/2 (agotamiento de memoria, caídas por condición de carrera), que afectan directamente a una API expuesta.

En la fase 3 se había evitado subir de versión de Go para no adoptar testcontainers-go. Ese motivo era una comodidad de pruebas; este es de seguridad.

Además, las imágenes de contenedor:

- corrían como **root** en producción (Alpine);
- usaban etiquetas flotantes (`asynqmon:latest`, `postgres:latest`) e instalaban Air con `curl | sh`;
- incluían **libvips** en todas las imágenes, aunque ningún binario la usa;
- copiaban el `.env` del desarrollador dentro de la imagen (`COPY . .` sin `.dockerignore`, y un `COPY .env .env` explícito);
- estaban repartidas en 7 Dockerfiles duplicados y desalineados.

## Decisión

1. **Go:**
   - directiva `go 1.26.0`, la versión mínima soportada, para que los proyectos derivados tengan margen;
   - `toolchain go1.27.3`, la última estable, para compilar.
   - **Regla:** la plantilla solo usa versiones de Go con soporte de seguridad. Cuando sale una versión mayor nueva, se actualiza el toolchain y, si la mínima pierde soporte, también la directiva.
2. **`govulncheck` es una puerta de calidad:**
   - en CI falla con cualquier vulnerabilidad **alcanzable**;
   - localmente se corre con `make vuln`.
3. **Imágenes de contenedor:**
   - un solo `Dockerfile` multi-etapa, con los targets `dev`, `api`, `migrate` y `worker`;
   - runtime `gcr.io/distroless/static-debian13:nonroot`: sin shell ni gestor de paquetes, UID 65532;
   - binarios estáticos (`CGO_ENABLED=0`, `-trimpath`);
   - **todas las imágenes fijadas por versión y digest**, también las de compose y CI;
   - Dependabot mantiene al día módulos, imágenes y Actions;
   - `.dockerignore` excluye cualquier `.env*` salvo la plantilla;
   - la imagen no tiene `wget` ni `curl`, así que la sonda del contenedor la hace el propio binario: `api healthcheck` consulta `/health/live`;
   - las migraciones son una imagen propia (`target migrate`) que se ejecuta como paso explícito del despliegue.
4. **Ejecución en QA/producción** (compose):
   - `read_only`, `cap_drop: [ALL]` y `no-new-privileges`;
   - Redis sin puerto publicado;
   - herramientas sin autenticación (Asynqmon) solo con un perfil explícito y en `127.0.0.1`.
5. **Desarrollo:**
   - el contenedor corre con el UID/GID del host, para que no genere archivos de root en el repositorio;
   - todos los puertos se publican solo en `127.0.0.1`.

## Consecuencias

- Hoy hay 0 vulnerabilidades alcanzables. Queda un aviso no alcanzable: el paquete `x/crypto/openpgp` sin mantenimiento, que no importamos.
- La imagen de la API pasa de Alpine con libvips a ~17 MB.
- No se puede abrir una shell en un contenedor de QA o producción. Para diagnosticar se usan los logs, `/health/*` o una imagen de depuración aparte; nunca se agrega una shell a la imagen de runtime.
- Con `GOTOOLCHAIN=auto` (el valor por defecto de Go), quien tenga instalada una versión más antigua descarga automáticamente go1.27.3 la primera vez.
- Las herramientas (`golangci-lint`, `govulncheck`) deben estar compiladas con un Go ≥ 1.26 para analizar el código. La versión de CI está fijada (`v2.15.0`, `v1.9.0`).
