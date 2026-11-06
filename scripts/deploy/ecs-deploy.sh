#!/usr/bin/env bash
# Despliega una versión ya publicada en ECR a un entorno de ECS (CLAUDE.md §17).
#
#   scripts/deploy/ecs-deploy.sh <tag>
#
# Orden (el mismo que el despliegue manual con compose):
#   1. migraciones: una task de un solo uso con la imagen migrate; si falla, se aborta
#      SIN tocar los servicios (la versión anterior sigue sirviendo);
#   2. api y worker: nueva revisión de su task definition con la imagen nueva;
#   3. espera a que la versión nueva quede estable en ambos servicios. Falla si ECS la
#      revirtió: que el servicio esté estable no basta, porque puede estarlo con la anterior.
#
# Cada task definition nueva copia la vigente y cambia SOLO la imagen del contenedor
# de la aplicación: sidecars (ADOT), variables, secretos y recursos se conservan.
# El rollback automático lo hace ECS con el deployment circuit breaker del servicio.
#
# Variables (las define el GitHub Environment de cada entorno):
#   REGISTRY            123456789012.dkr.ecr.us-east-1.amazonaws.com
#   ECS_CLUSTER         nombre del clúster
#   ECS_SERVICE_API     servicio de la API
#   ECS_SERVICE_WORKER  servicio del worker (vacío si el entorno no tiene worker)
#   MIGRATE_TASK_FAMILY familia de la task definition de migraciones
#   SUBNETS             subredes privadas para la task de migraciones (coma)
#   SECURITY_GROUPS     security groups de la task de migraciones (coma)
#   IMAGE_PREFIX        prefijo de los repositorios de ECR (por defecto: myapp)
#   CONTAINER_API / CONTAINER_WORKER / CONTAINER_MIGRATE  nombres de los contenedores
#                       (por defecto: api / worker / migrate)
#   SKIP_MIGRATIONS     true al volver a una versión anterior: su imagen migrate no conoce
#                       las migraciones más nuevas y fallaría, y el esquema no retrocede
#   DEPLOY_TIMEOUT      segundos de espera a que los servicios queden estables (por defecto: 1800)
#   MIGRATION_TIMEOUT   segundos de espera a que terminen las migraciones (por defecto: 3600)
set -euo pipefail

tag="${1:?uso: ecs-deploy.sh <tag>}"
: "${REGISTRY:?}" "${ECS_CLUSTER:?}" "${ECS_SERVICE_API:?}" "${MIGRATE_TASK_FAMILY:?}" "${SUBNETS:?}" "${SECURITY_GROUPS:?}"
prefix="${IMAGE_PREFIX:-myapp}"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

# new_revision <familia|arn> <contenedor> <imagen> → ARN de la revisión nueva.
new_revision() {
  local current="$1" container="$2" image="$3" file="$workdir/td.json"
  aws ecs describe-task-definition --task-definition "$current" --query taskDefinition --output json \
    | jq --arg c "$container" --arg img "$image" '
        if any(.containerDefinitions[]; .name == $c) then . else error("la task definition no tiene el contenedor \($c)") end
        | .containerDefinitions |= map(if .name == $c then .image = $img else . end)
        | del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities,
              .registeredAt, .registeredBy, .deregisteredAt)' >"$file"
  aws ecs register-task-definition --cli-input-json "file://$file" \
    --query taskDefinition.taskDefinitionArn --output text
}

csv_to_json() { jq -cR 'split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(length > 0))' <<<"$1"; }

# --- 1. Migraciones -----------------------------------------------------------
if [[ "${SKIP_MIGRATIONS:-false}" == "true" ]]; then
  log "vuelta a una versión anterior: las migraciones no se ejecutan"
else
  log "migraciones: $prefix-migrate:$tag"
  migrate_td="$(new_revision "$MIGRATE_TASK_FAMILY" "${CONTAINER_MIGRATE:-migrate}" "$REGISTRY/$prefix-migrate:$tag")"
  network="$(jq -cn --argjson s "$(csv_to_json "$SUBNETS")" --argjson g "$(csv_to_json "$SECURITY_GROUPS")" \
    '{awsvpcConfiguration: {subnets: $s, securityGroups: $g, assignPublicIp: "DISABLED"}}')"
  task="$(aws ecs run-task --cluster "$ECS_CLUSTER" --task-definition "$migrate_td" --launch-type FARGATE \
    --network-configuration "$network" --started-by "deploy-$tag" --query 'tasks[0].taskArn' --output text)"
  if [[ -z "$task" || "$task" == "None" ]]; then
    log "ERROR: no se pudo iniciar la task de migraciones"
    exit 1
  fi
  # Espera propia: `aws ecs wait tasks-stopped` se corta a los 10 minutos, y una migración
  # puede tardar más. Cortar el job no detiene la task, y dejaría el despliegue a medias.
  migration_deadline=$((SECONDS + ${MIGRATION_TIMEOUT:-3600}))
  until [[ "$(aws ecs describe-tasks --cluster "$ECS_CLUSTER" --tasks "$task" \
    --query 'tasks[0].lastStatus' --output text || true)" == "STOPPED" ]]; do
    if (( SECONDS >= migration_deadline )); then
      log "ERROR: las migraciones no terminaron a tiempo; los servicios NO se actualizaron y la task $task sigue en marcha"
      exit 1
    fi
    sleep "${DEPLOY_POLL_INTERVAL:-15}"
  done
  exit_code="$(aws ecs describe-tasks --cluster "$ECS_CLUSTER" --tasks "$task" \
    --query "tasks[0].containers[?name=='${CONTAINER_MIGRATE:-migrate}'].exitCode | [0]" --output text)"
  if [[ "$exit_code" != "0" ]]; then
    log "ERROR: las migraciones terminaron con código $exit_code; los servicios NO se actualizaron (ver los logs de la task $task). Si es una vuelta a una versión anterior, usa SKIP_MIGRATIONS=true"
    exit 1
  fi
  log "migraciones aplicadas"
fi

# --- 2. Servicios ------------------------------------------------------------
deploy_service() {
  local service="$1" container="$2" image="$3" current td
  current="$(aws ecs describe-services --cluster "$ECS_CLUSTER" --services "$service" \
    --query 'services[0].taskDefinition' --output text)"
  td="$(new_revision "$current" "$container" "$image")"
  log "$service → $td"
  aws ecs update-service --cluster "$ECS_CLUSTER" --service "$service" --task-definition "$td" \
    --query 'service.serviceName' --output text >/dev/null
  services+=("$service")
  revisions+=("$td")
}

services=()
revisions=()
deploy_service "$ECS_SERVICE_API" "${CONTAINER_API:-api}" "$REGISTRY/$prefix-api:$tag"
if [[ -n "${ECS_SERVICE_WORKER:-}" ]]; then
  deploy_service "$ECS_SERVICE_WORKER" "${CONTAINER_WORKER:-worker}" "$REGISTRY/$prefix-worker:$tag"
fi

# --- 3. Estabilidad ----------------------------------------------------------
# wait_rollout <servicio> <revisión>: termina bien solo si esa revisión es el despliegue
# PRIMARY y llegó a COMPLETED. `aws ecs wait services-stable` no sirve: tras un rollback
# del circuit breaker queda una sola deployment, la anterior, y también da éxito.
#
# La API de ECS es de consistencia eventual: justo después de update-service, otra
# revisión como PRIMARY puede ser una lectura vieja. Solo es un rollback si ya se vio la
# nueva o si pasó el margen.
timeout_at=$((SECONDS + ${DEPLOY_TIMEOUT:-1800}))
wait_rollout() {
  local service="$1" revision="$2" primary state reason seen=false stale_until=$((SECONDS + ${DEPLOY_STALE_GRACE:-60}))
  while :; do
    # shellcheck disable=SC2016  # las comillas invertidas son de JMESPath
    IFS=$'\t' read -r primary state reason <<<"$(aws ecs describe-services --cluster "$ECS_CLUSTER" --services "$service" \
      --query 'services[0].deployments[?status==`PRIMARY`] | [0].[taskDefinition,rolloutState,rolloutStateReason]' --output text)" || true
    if [[ "$primary" == "$revision" ]]; then
      seen=true
      case "$state" in
        COMPLETED) log "$service estable en $revision"; return 0 ;;
        FAILED) log "ERROR: el despliegue de $service falló: $reason"; return 1 ;;
      esac
    elif [[ -n "$primary" && ("$seen" == true || $SECONDS -ge $stale_until) ]]; then
      log "ERROR: ECS revirtió $service a $primary; $tag NO quedó desplegado ($reason)"
      return 1
    fi
    if (( SECONDS >= timeout_at )); then
      log "ERROR: $service no quedó estable a tiempo (último estado: ${state:-desconocido}); ECS sigue intentándolo"
      return 1
    fi
    sleep "${DEPLOY_POLL_INTERVAL:-15}"
  done
}

log "esperando a que ${services[*]} queden estables"
for i in "${!services[@]}"; do
  wait_rollout "${services[$i]}" "${revisions[$i]}"
done
log "desplegado $tag"
