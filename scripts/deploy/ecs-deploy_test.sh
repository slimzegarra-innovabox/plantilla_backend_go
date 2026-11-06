#!/usr/bin/env bash
# Prueba de scripts/deploy/ecs-deploy.sh con un `aws` falso (no necesita cuenta de AWS):
# comprueba el orden, que solo cambia la imagen del contenedor de la app, que un fallo
# de las migraciones aborta sin actualizar los servicios y que volver a una versión
# anterior no las ejecuta, y que no se da por bueno un despliegue que ECS revirtió.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/aws" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_LOG"
case "$1 $2" in
  "ecs describe-task-definition")
    printf '%s' '{"family":"f","taskDefinitionArn":"arn:td/f:1","revision":1,"status":"ACTIVE","containerDefinitions":[{"name":"api","image":"old/api"},{"name":"worker","image":"old/worker"},{"name":"migrate","image":"old/migrate"},{"name":"adot","image":"aws-otel-collector:v0.50.0"}]}' ;;
  "ecs register-task-definition")
    for a in "$@"; do [[ "$a" == file://* ]] && cp "${a#file://}" "$FAKE_DIR/registered-$(date +%s%N).json"; done
    echo "arn:td/f:2" ;;
  "ecs run-task") echo "arn:task/1" ;;
  "ecs describe-tasks")
    if [[ "$*" == *lastStatus* ]]; then
      line="$(head -1 "$FAKE_DIR/task")"
      [[ "$(wc -l <"$FAKE_DIR/task")" -gt 1 ]] && sed -i 1d "$FAKE_DIR/task"
      echo "$line"
    else
      echo "$FAKE_MIGRATE_EXIT"
    fi ;;
  "ecs describe-services")
    if [[ "$*" == *deployments* ]]; then
      # Cada consulta consume una línea de la secuencia; la última se repite.
      line="$(head -1 "$FAKE_DIR/rollout")"
      [[ "$(wc -l <"$FAKE_DIR/rollout")" -gt 1 ]] && sed -i 1d "$FAKE_DIR/rollout"
      printf '%b\n' "$line"
    else
      echo "arn:td/svc:7"
    fi ;;
  "ecs update-service") echo "svc" ;;
  *) echo "llamada inesperada: $*" >&2; exit 1 ;;
esac
FAKE
chmod +x "$tmp/aws"

run() {
  : >"$tmp/log"; rm -f "$tmp"/registered-*.json
  printf '%s\n' "${ROLLOUT[@]}" >"$tmp/rollout"
  printf '%s\n' "${TASK[@]}" >"$tmp/task"
  PATH="$tmp:$PATH" FAKE_LOG="$tmp/log" FAKE_DIR="$tmp" FAKE_MIGRATE_EXIT="$1" \
    REGISTRY=reg ECS_CLUSTER=c ECS_SERVICE_API=svc-api ECS_SERVICE_WORKER=svc-worker \
    MIGRATE_TASK_FAMILY=migrate SUBNETS="subnet-a, subnet-b" SECURITY_GROUPS=sg-1 DEPLOY_POLL_INTERVAL=0 \
    "$here/ecs-deploy.sh" abc1234
}
fail() { echo "FALLA: $*" >&2; exit 1; }
new='arn:td/f:2' old='arn:td/svc:7'
ROLLOUT=("$new\\tCOMPLETED\\tok")
TASK=(STOPPED)

# 1. Camino feliz.
run 0 2>/dev/null || fail "el despliegue debía terminar bien"
grep -q '"subnets":\["subnet-a","subnet-b"\]' "$tmp/log" || fail "subredes mal formadas (se esperaba la lista sin espacios)"
[[ "$(grep -c '^ecs update-service' "$tmp/log")" == 2 ]] || fail "debía actualizar api y worker"
first_update="$(grep -n '^ecs update-service' "$tmp/log" | head -1 | cut -d: -f1)"
run_task="$(grep -n '^ecs run-task' "$tmp/log" | cut -d: -f1)"
(( run_task < first_update )) || fail "las migraciones deben correr antes de actualizar los servicios"
[[ "$(grep -c '^ecs describe-services.*deployments' "$tmp/log")" == 2 ]] || fail "debía comprobar el despliegue de api y worker"
api_td="$(grep -l '"image": "reg/myapp-api:abc1234"' "$tmp"/registered-*.json)" || fail "la API no recibió la imagen nueva"
jq -e '[.containerDefinitions[] | select(.name=="adot") | .image] == ["aws-otel-collector:v0.50.0"]' "$api_td" >/dev/null \
  || fail "el sidecar no debía cambiar"
jq -e 'has("taskDefinitionArn") or has("revision") or has("status") | not' "$api_td" >/dev/null \
  || fail "quedaron campos de solo lectura en la task definition"

# 2. Migraciones fallidas: no se toca ningún servicio.
if run 1 2>/dev/null; then fail "un fallo de migraciones debía abortar"; fi
! grep -q '^ecs update-service' "$tmp/log" || fail "no debía actualizar servicios tras migraciones fallidas"

# 3. Vuelta a una versión anterior: sin migraciones, y los servicios se actualizan.
SKIP_MIGRATIONS=true run 1 2>/dev/null || fail "volver a una versión anterior no debía depender de las migraciones"
! grep -q '^ecs run-task' "$tmp/log" || fail "no debía ejecutar las migraciones"
! grep -l '"image": "reg/myapp-migrate:abc1234"' "$tmp"/registered-*.json >/dev/null || fail "no debía registrar la task de migraciones"
[[ "$(grep -c '^ecs update-service' "$tmp/log")" == 2 ]] || fail "debía actualizar api y worker"

# 4. Sigue la secuencia real de un despliegue: en curso y después completado.
ROLLOUT=("$new\\tIN_PROGRESS\\tx" "$new\\tCOMPLETED\\tok")
run 0 2>"$tmp/out" || fail "un despliegue que termina bien debía pasar"
grep -q 'desplegado abc1234' "$tmp/out" || fail "debía informar el despliegue"

# 5. Una lectura vieja justo después de update-service no es un rollback.
ROLLOUT=("$old\\tCOMPLETED\\tok" "$new\\tCOMPLETED\\tok")
run 0 2>/dev/null || fail "una lectura vieja no debía tomarse como rollback"

# 6. Circuit breaker: la versión nueva se vio y ECS volvió a la anterior. La deployment
#    queda COMPLETED y el servicio estable, pero el despliegue falló.
ROLLOUT=("$new\\tIN_PROGRESS\\tx" "$old\\tCOMPLETED\\trollback")
if run 0 2>"$tmp/out"; then fail "un despliegue revertido debía fallar"; fi
grep -q 'ECS revirtió' "$tmp/out" || fail "debía informar la reversión"
! grep -q 'desplegado abc1234' "$tmp/out" || fail "no debía informar el despliegue"

# 7. Revertido antes de llegar a verse la versión nueva: falla al vencer el margen.
ROLLOUT=("$old\\tCOMPLETED\\trollback")
if DEPLOY_STALE_GRACE=0 run 0 2>/dev/null; then fail "debía fallar si la versión nueva nunca aparece"; fi

# 8. Rollout fallido sin rollback automático.
ROLLOUT=("$new\\tFAILED\\tcircuit breaker")
if run 0 2>/dev/null; then fail "un rollout FAILED debía fallar"; fi

# 9. No queda estable a tiempo.
ROLLOUT=("$new\\tIN_PROGRESS\\tx")
if DEPLOY_TIMEOUT=0 run 0 2>"$tmp/out"; then fail "debía fallar al vencer el plazo"; fi
grep -q 'no quedó estable a tiempo' "$tmp/out" || fail "debía informar el vencimiento"

# 10. Migración larga: espera mientras la task sigue en marcha.
ROLLOUT=("$new\\tCOMPLETED\\tok")
TASK=(PENDING RUNNING RUNNING STOPPED)
run 0 2>/dev/null || fail "debía esperar a que terminaran las migraciones"
[[ "$(grep -c 'lastStatus' "$tmp/log")" == 4 ]] || fail "debía consultar la task hasta verla STOPPED"

# 11. Migración que no termina: falla sin tocar los servicios.
TASK=(RUNNING)
if MIGRATION_TIMEOUT=0 run 0 2>"$tmp/out"; then fail "debía fallar al vencer el plazo de las migraciones"; fi
grep -q 'no terminaron a tiempo' "$tmp/out" || fail "debía informar el vencimiento"
! grep -q '^ecs update-service' "$tmp/log" || fail "no debía actualizar servicios"
TASK=(STOPPED)

# 12. Prefijo y contenedores configurables.
IMAGE_PREFIX=acme CONTAINER_API=worker run 0 2>/dev/null || fail "debía desplegar con otro prefijo y contenedor"
grep -l '"image": "reg/acme-migrate:abc1234"' "$tmp"/registered-*.json >/dev/null || fail "debía usar IMAGE_PREFIX"
cat "$tmp"/registered-*.json | jq -es 'any(.[].containerDefinitions[]; .name == "worker" and .image == "reg/acme-api:abc1234")' >/dev/null \
  || fail "debía usar CONTAINER_API"

echo "ok  scripts/deploy/ecs-deploy.sh"
