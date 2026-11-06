#!/usr/bin/env bash
# Prueba de scripts/deploy/ecr-publish.sh con `aws` y `docker` falsos: solo construye lo
# que falta, y un error que no sea «no existe» no se toma como imagen faltante.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/aws" <<'FAKE'
#!/usr/bin/env bash
for existing in $FAKE_EXISTING; do
  [[ "$*" == *"--repository-name myapp-$existing "* ]] && { echo '{}'; exit 0; }
done
echo "$FAKE_ERROR" >&2
exit 254
FAKE
cat >"$tmp/docker" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_LOG"
FAKE
chmod +x "$tmp/aws" "$tmp/docker"

run() {
  : >"$tmp/log"
  PATH="$tmp:$PATH" FAKE_LOG="$tmp/log" FAKE_EXISTING="$1" FAKE_ERROR="$2" REGISTRY=reg "$here/ecr-publish.sh" abc1234 >/dev/null 2>&1
}
fail() { echo "FALLA: $*" >&2; exit 1; }
not_found='An error occurred (ImageNotFoundException) when calling the DescribeImages operation'
built() { grep -c '^buildx build' "$tmp/log" || true; }

# 1. Nada publicado: construye las tres, con SBOM y procedencia.
run "" "$not_found" || fail "debía publicar"
[[ "$(built)" == 3 ]] || fail "debía construir las tres imágenes"
grep -q -- '--sbom=true --provenance=mode=max --tag reg/myapp-api:abc1234 --push' "$tmp/log" || fail "imagen sin SBOM, procedencia o etiqueta"

# 2. Publicación a medias: construye solo lo que falta.
run "api" "$not_found" || fail "debía completar la publicación"
[[ "$(built)" == 2 ]] || fail "debía construir solo worker y migrate"
! grep -q 'myapp-api' "$tmp/log" || fail "no debía reconstruir api"

# 3. Todo publicado: repetir el despliegue no construye nada.
run "api worker migrate" "$not_found" || fail "debía reutilizar las imágenes"
[[ ! -s "$tmp/log" ]] || fail "no debía invocar docker"

# 4. Otro error (permisos, red): falla sin construir.
if run "" "An error occurred (AccessDeniedException)"; then fail "un error de permisos debía fallar"; fi
[[ ! -s "$tmp/log" ]] || fail "no debía construir tras un error de consulta"

# 5. IMAGE_PREFIX cambia los repositorios.
: >"$tmp/log"
PATH="$tmp:$PATH" FAKE_LOG="$tmp/log" FAKE_EXISTING="" FAKE_ERROR="$not_found" REGISTRY=reg IMAGE_PREFIX=acme \
  "$here/ecr-publish.sh" abc1234 >/dev/null 2>&1 || fail "debía publicar con el prefijo"
grep -q 'reg/acme-api:abc1234' "$tmp/log" || fail "debía usar IMAGE_PREFIX"

echo "ok  scripts/deploy/ecr-publish.sh"
