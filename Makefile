.PHONY: help up down build run check fmt-check vet test test-integration test-docker vuln docker-build lint lint-fix tidy fmt \
        health logs shell module remove-example scan lint-ci check-migrations cover load \
        migrate-up migrate-down migrate-version migrate-create migrate-force

# Entorno activo (sobreescribir con: make up ENV=quality)
ENV ?= develop

COMPOSE_FILE = $(ENV).yml
APP_CONTAINER = app_backend

# develop.yml ejecuta el contenedor con el usuario del host (archivos generados sin dueño root).
export UID := $(shell id -u)
export GID := $(shell id -g)

# Versión inyectada en el binario de las imágenes.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# ──────────────────────────────────────────────────────────────
# AYUDA
# ──────────────────────────────────────────────────────────────
help:
	@echo ""
	@echo "  Plantilla Go Backend — comandos disponibles"
	@echo ""
	@echo "  Docker"
	@echo "    make up          Levantar en modo $(ENV) (hot reload)"
	@echo "    make down        Detener y eliminar contenedores"
	@echo "    make build       Reconstruir imagen sin cache"
	@echo "    make logs        Ver logs del backend en tiempo real"
	@echo "    make shell       Abrir shell en el contenedor del backend (solo develop: las imágenes distroless no tienen shell)"
	@echo "    make docker-build    Construir la imagen de la API (target api, VERSION=$(VERSION))"
	@echo "    make scan        Construir api/worker/migrate, escanearlas con Trivy y generar sus SBOM en sbom/"
	@echo "    make health      Consultar /health/ready del servidor"
	@echo ""
	@echo "  Calidad"
	@echo "    make check            Todo lo que exige el CI sin Docker: formato, vet, lint, pruebas, vulnerabilidades"
	@echo "    make lint-ci          Workflows (actionlint), scripts (shellcheck) y prueba del despliegue (requiere Docker)"
	@echo ""
	@echo "  Módulos"
	@echo "    make module NAME=orders ENTITY=order [PLURAL=orders] [LABEL=pedido] [FULL=1]"
	@echo "                          Crear un módulo: CRUD mínimo, o con FULL=1 copia de internal/example"
	@echo "                          (eventos, máquina de estados, búsqueda). Admite snake_case: purchase_order"
	@echo "    make remove-example   Quitar el módulo de referencia (FULL=1 deja de estar disponible)"
	@echo ""
	@echo "  Go"
	@echo "    make run              Correr el servidor localmente (sin Docker)"
	@echo "    make test             Pruebas unitarias, HTTP y de arquitectura (sin Docker)"
	@echo "    make test-integration Pruebas contra PostgreSQL real (Docker o TEST_DATABASE_URL)"
	@echo "    make test-docker      Pruebas de integración en contenedores (sin Go local)"
	@echo "    make cover            Cobertura con integración; reporte en coverage.html"
	@echo "    make load             Prueba de carga con k6 (RATE, DURATION, SEED, MODE=ramp); ver docs/load-baseline.md"
	@echo "    make vuln             Vulnerabilidades alcanzables (govulncheck)"
	@echo "    make fmt              Formatear código (gofmt)"
	@echo "    make lint             Lint con golangci-lint"
	@echo "    make lint-fix         Lint + aplicar correcciones automáticas"
	@echo "    make tidy             go mod tidy"
	@echo ""
	@echo "  Migraciones (golang-migrate)"
	@echo "    make migrate-up           Aplicar todas las migraciones pendientes"
	@echo "    make migrate-down         Revertir la última migración"
	@echo "    make migrate-version      Ver versión actual del esquema"
	@echo "    make migrate-force VERSION=N  Forzar versión (emergencias)"
	@echo "    make migrate-create NAME=xxx  Crear par up/down con el siguiente número"
	@echo "    make check-migrations [BASE=origin/main]  Ninguna migración ya integrada fue editada"
	@echo ""
	@echo "  Usar otro entorno: make up ENV=quality"
	@echo ""

# ──────────────────────────────────────────────────────────────
# DOCKER
# ──────────────────────────────────────────────────────────────
up:
	docker compose -f $(COMPOSE_FILE) up

down:
	docker compose -f $(COMPOSE_FILE) down

build:
	docker compose -f $(COMPOSE_FILE) build --no-cache

logs:
	docker compose -f $(COMPOSE_FILE) logs -f $(APP_CONTAINER)

shell:
	docker compose -f $(COMPOSE_FILE) exec $(APP_CONTAINER) sh

health:
	@curl -s -w " [%{http_code}]\n" http://localhost:8080/health/ready

# ──────────────────────────────────────────────────────────────
# PRUEBAS DE CARGA (k6): make load [RATE=300 DURATION=60s SEED=100000] | make load MODE=ramp
# ──────────────────────────────────────────────────────────────
K6_IMAGE ?= grafana/k6:2.3.0@sha256:2dd4756ec6e14e62778916e1eb850108e4b499304c02df56760fa0bdb0fba0b8

load:
	K6_IMAGE=$(K6_IMAGE) test/load/run.sh

# ──────────────────────────────────────────────────────────────
# WORKFLOWS Y SCRIPTS DE DESPLIEGUE
# ──────────────────────────────────────────────────────────────
ACTIONLINT_IMAGE ?= rhysd/actionlint:1.7.12@sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667

lint-ci:
	docker run --rm -v $(CURDIR):/repo -w /repo $(ACTIONLINT_IMAGE) -color
	docker run --rm -v $(CURDIR):/repo -w /repo --entrypoint shellcheck $(ACTIONLINT_IMAGE) scripts/deploy/*.sh scripts/ci/*.sh test/load/*.sh deploy/otel/*.sh
	scripts/deploy/ecs-deploy_test.sh
	scripts/deploy/ecr-publish_test.sh
	deploy/otel/adot-collector_test.sh
	scripts/ci/check-migrations_test.sh
	scripts/ci/coverage_test.sh

# Comprueba que ninguna migración ya integrada cambió respecto de BASE (por defecto origin/main).
BASE ?= origin/main
check-migrations:
	scripts/ci/check-migrations.sh $(BASE)

# ──────────────────────────────────────────────────────────────
# ESCANEO DE IMÁGENES (Trivy)
# ──────────────────────────────────────────────────────────────
# Trivy se ejecuta fijado por DIGEST, nunca por etiqueta: en marzo de 2026 su cadena de
# suministro fue comprometida (GHSA-69fq-xp46-6x23) y las etiquetas se reescribieron con
# malware. Para actualizarlo: elegir una versión con más de 2 semanas, obtener su digest
# (docker buildx imagetools inspect aquasec/trivy:<v>) y VERIFICAR la firma:
#   docker run --rm ghcr.io/sigstore/cosign/cosign:<v> verify aquasec/trivy@<digest> \
#     --certificate-identity-regexp '^https://github\.com/aquasecurity/trivy/\.github/workflows/.+' \
#     --certificate-oidc-issuer https://token.actions.githubusercontent.com
TRIVY_IMAGE ?= aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c
SCAN_TAG ?= scan
SCAN_DIR := $(CURDIR)/sbom
# Falla ante vulnerabilidades HIGH/CRITICAL con corrección disponible (las que no tienen
# parche no se pueden resolver actualizando y solo generarían ruido).
# Con el usuario del host (los SBOM no quedan de root) y la caché de la base de
# vulnerabilidades dentro de sbom/ (ignorada por git).
TRIVY = docker run --rm --user $(UID):$(GID) -e TRIVY_CACHE_DIR=/scan/.cache -v $(SCAN_DIR):/scan $(TRIVY_IMAGE)

scan:
	@mkdir -p $(SCAN_DIR)
	@set -e; for t in api worker migrate; do \
	  if [ -z "$$SKIP_BUILD" ]; then docker build -q --target $$t --build-arg VERSION=$(VERSION) -t myapp-$$t:$(SCAN_TAG) . >/dev/null; fi; \
	  docker save myapp-$$t:$(SCAN_TAG) -o $(SCAN_DIR)/$$t.tar; \
	  echo "== $$t"; \
	  $(TRIVY) image --input /scan/$$t.tar --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed \
	    --exit-code 1 --no-progress --quiet --format table; \
	  $(TRIVY) image --input /scan/$$t.tar --format cyclonedx --output /scan/$$t.cdx.json --quiet; \
	  rm -f $(SCAN_DIR)/$$t.tar; \
	done
	@echo "✔ imágenes sin vulnerabilidades HIGH/CRITICAL corregibles; SBOM en sbom/*.cdx.json"

docker-build:
	docker build --target api --build-arg VERSION=$(VERSION) -t myapp-api:$(VERSION) .

test-docker:
	docker compose -f test.yml run --rm --build tests; status=$$?; \
	docker compose -f test.yml down -v; exit $$status

# ──────────────────────────────────────────────────────────────
# GO LOCAL (sin Docker)
# ──────────────────────────────────────────────────────────────
run:
	go run ./cmd/api

# check es la puerta de calidad local: lo mismo que el job «Test & Lint» del CI.
# La integración (Docker o TEST_DATABASE_URL) va aparte: make test-integration.
check: fmt-check vet lint test vuln
	@echo "✔ make check en verde"

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "Archivos sin formato (make fmt):"; echo "$$out"; exit 1; fi

vet:
	go vet -tags integration ./...

test:
	go test -race -count=1 ./...

test-integration:
	go test -tags integration -race -count=1 -p 1 ./...

# Cobertura con las pruebas de integración (PostgreSQL y Redis: Docker o TEST_DATABASE_URL
# / TEST_REDIS_ADDR). Deja el reporte navegable en coverage.html.
cover:
	@rm -rf coverage-e2e && mkdir -p coverage-e2e
	E2E_COVERDIR=$(CURDIR)/coverage-e2e go test -tags integration -count=1 -p 1 -covermode=atomic -coverpkg=./cmd/...,./internal/... -coverprofile=coverage.out ./...
	go tool covdata textfmt -i=coverage-e2e -o=coverage-e2e.out && tail -n +2 coverage-e2e.out >>coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@rm -rf coverage-e2e coverage-e2e.out
	@scripts/ci/coverage.sh coverage.out

vuln:
	govulncheck ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

lint:
	golangci-lint run ./...

lint-fix:
	golangci-lint run --fix ./...

# ──────────────────────────────────────────────────────────────
# MÓDULOS (tools/modulegen)
# ──────────────────────────────────────────────────────────────
module:
	@test -n "$(NAME)" -a -n "$(ENTITY)" || (echo "Uso: make module NAME=orders ENTITY=order [PLURAL=orders] [LABEL=pedido] [FULL=1]" && exit 1)
	go run ./tools/modulegen -name "$(NAME)" -entity "$(ENTITY)" -plural "$(PLURAL)" -label "$(LABEL)" $(if $(FULL),-full)

remove-example:
	go run ./tools/modulegen -remove-example

# ──────────────────────────────────────────────────────────────
# MIGRACIONES
# ──────────────────────────────────────────────────────────────

MIGRATIONS_DIR = scripts/db/migrations

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down 1

migrate-version:
	go run ./cmd/migrate version

migrate-force:
	@test -n "$(VERSION)" || (echo "Usage: make migrate-force VERSION=N" && exit 1)
	go run ./cmd/migrate force $(VERSION)

# Crea un par de archivos .up.sql y .down.sql con el nombre dado.
# Uso: make migrate-create NAME=add_users_table
migrate-create:
	@test -n "$(NAME)" || (echo "Usage: make migrate-create NAME=descripcion_snake_case" && exit 1)
	@NEXT=$$(printf "%06d" $$(ls $(MIGRATIONS_DIR)/*.up.sql 2>/dev/null | wc -l | awk '{print $$1 + 1}')); \
	touch "$(MIGRATIONS_DIR)/$${NEXT}_$(NAME).up.sql"; \
	touch "$(MIGRATIONS_DIR)/$${NEXT}_$(NAME).down.sql"; \
	echo "Created: $(MIGRATIONS_DIR)/$${NEXT}_$(NAME).up.sql"; \
	echo "Created: $(MIGRATIONS_DIR)/$${NEXT}_$(NAME).down.sql"
