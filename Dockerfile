# syntax=docker/dockerfile:1
#
# Imagen única con un target por uso:
#
#   dev      → desarrollo con hot reload (Air), código montado como volumen
#   api      → servidor HTTP          (cmd/api)
#   migrate  → migraciones SQL         (cmd/migrate), para el paso de despliegue
#   worker   → procesos en segundo plano (cmd/worker)
#
#   docker build --target api -t myapp-api .
#
# Reglas (CLAUDE.md §17):
#   - Imágenes base fijadas por versión Y digest: un rebuild produce lo mismo.
#     Para actualizarlas: docker buildx imagetools inspect <imagen:tag>.
#   - Runtime distroless "nonroot": sin shell, sin gestor de paquetes, UID 65532.
#   - Binarios estáticos (CGO_ENABLED=0): ningún binario necesita libc.
#   - Nada de secretos en la imagen: .dockerignore excluye .env*.

ARG GO_IMAGE=golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

# ========= Dependencias (capa cacheable) =========
FROM ${GO_IMAGE} AS deps
WORKDIR /src
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# ========= Desarrollo (Air) =========
# develop.yml lo ejecuta con el UID/GID del host, para que lo que genera Air en el
# código montado (tmp/) sea del desarrollador y no de root. Las cachés de Go viven
# en /cache (1777), que un volumen con nombre hereda al crearse.
# Esta imagen NUNCA se despliega.
FROM deps AS dev
RUN --mount=type=cache,target=/go/pkg/mod \
    GOBIN=/usr/local/bin go install github.com/air-verse/air@v1.67.4 \
 && mkdir -p /cache && chmod 1777 /cache
ENV HOME=/tmp \
    GOCACHE=/cache/go-build \
    GOMODCACHE=/cache/mod
WORKDIR /app
EXPOSE 8080
CMD ["air"]

# ========= Compilación =========
FROM deps AS build
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    for cmd in api migrate worker; do \
      go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/$cmd ./cmd/$cmd; \
    done

# ========= Runtime: API =========
FROM ${RUNTIME_IMAGE} AS api
COPY --from=build /out/api /app/api
USER nonroot:nonroot
ENV GIN_MODE=release \
    HTTP_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
    CMD ["/app/api", "healthcheck"]
ENTRYPOINT ["/app/api"]

# ========= Runtime: migraciones =========
# Paso explícito del despliegue, ANTES de la nueva versión de la API:
#   docker run --rm --env-file .env.prod myapp-migrate up
FROM ${RUNTIME_IMAGE} AS migrate
COPY --from=build /out/migrate /app/migrate
COPY scripts/db/migrations /app/migrations
USER nonroot:nonroot
ENV MIGRATIONS_PATH=/app/migrations
ENTRYPOINT ["/app/migrate"]
CMD ["up"]

# ========= Runtime: worker =========
FROM ${RUNTIME_IMAGE} AS worker
COPY --from=build /out/worker /app/worker
# Liveness: los bucles del worker (Asynq, outbox, SQS) registran latidos; la sonda
# falla si alguno se colgó. Escucha en 127.0.0.1:8081 (WORKER_HEALTH_ADDR).
HEALTHCHECK --interval=30s --timeout=3s --start-period=30s --retries=3 \
    CMD ["/app/worker", "healthcheck"]
USER nonroot:nonroot
ENTRYPOINT ["/app/worker"]
