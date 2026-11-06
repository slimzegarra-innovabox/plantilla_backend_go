#!/usr/bin/env bash
# Levanta PostgreSQL y la API, carga datos y ejecuta k6 (make load). No toca nada existente.
#
#   SEED=100000 RATE=300 DURATION=60s MODE=steady test/load/run.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

PG_IMAGE="postgres:16.15-alpine@sha256:0cefaabd882e42bcb2a2f3dc56d3ce4770560ba62fd7f197f1a2e008fd5731ab"
K6_IMAGE="${K6_IMAGE:?falta K6_IMAGE}"
SEED="${SEED:-100000}"
PORT=18080
PG_PORT=55490
pg="load-pg-$$"
tmp="$(mktemp -d)"
api_pid=""

cleanup() {
  [[ -n "$api_pid" ]] && kill -TERM "$api_pid" 2>/dev/null && wait "$api_pid" 2>/dev/null || true
  docker rm -f "$pg" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

export APP_ENV=test LOG_LEVEL=warn DB_HOST=127.0.0.1 DB_PORT=$PG_PORT DB_USER=app DB_PASSWORD=app-load-only DB_NAME=app DB_SSLMODE=disable
export JWT_SECRET=load-test-only-0123456789abcdef0123 JWT_ISSUER=load JWT_AUDIENCE=load
export HTTP_ADDR=127.0.0.1:$PORT HTTP_RATE_LIMIT_RPS=0

docker run -d --rm --name "$pg" -e POSTGRES_USER=app -e POSTGRES_PASSWORD=$DB_PASSWORD -e POSTGRES_DB=app \
  -p 127.0.0.1:$PG_PORT:5432 "$PG_IMAGE" -c max_connections=100 >/dev/null

go build -o "$tmp/api" ./cmd/api
go build -o "$tmp/migrate" ./cmd/migrate
for _ in $(seq 1 120); do "$tmp/migrate" up >/dev/null 2>&1 && break; sleep 0.5; done
"$tmp/migrate" version >/dev/null

echo "== cargando $SEED filas"
docker exec "$pg" psql -q -U app -d app -c "
  INSERT INTO examples (uuid, name, status, version, created_by, created_at, updated_at)
  SELECT gen_random_uuid(), 'item ' || md5(i::text), 'pending', 1, '11111111-1111-4111-8111-111111111111',
         now() - (i || ' seconds')::interval, now()
  FROM generate_series(1, $SEED) i;
  ANALYZE examples;"

"$tmp/api" &
api_pid=$!
for _ in $(seq 1 40); do curl -sf -o /dev/null "http://127.0.0.1:$PORT/health/live" && break; sleep 0.25; done

# --network host: k6 llega a la API en loopback. La carga y el servidor comparten
# máquina: las cifras sirven para comparar versiones, no como capacidad de producción.
docker run --rm --network host -v "$PWD/test/load:/load:ro" \
  -e BASE_URL="http://127.0.0.1:$PORT" -e JWT_SECRET -e JWT_ISSUER -e JWT_AUDIENCE \
  -e RATE="${RATE:-300}" -e DURATION="${DURATION:-60s}" -e MODE="${MODE:-steady}" \
  "$K6_IMAGE" run --quiet /load/examples.js

echo "== estado de PostgreSQL"
docker exec "$pg" psql -U app -d app -tAc "SELECT count(*) || ' filas en examples' FROM examples"
