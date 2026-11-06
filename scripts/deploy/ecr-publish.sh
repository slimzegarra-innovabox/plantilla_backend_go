#!/usr/bin/env bash
# Publica en ECR las imágenes de un commit, con SBOM y procedencia (docs/deploy.md).
#
#   REGISTRY=… scripts/deploy/ecr-publish.sh <tag>
#
# Las etiquetas de ECR son inmutables, y la procedencia cambia el digest en cada
# construcción: reconstruir una etiqueta existente falla. Por eso solo se construye lo
# que falta, y así se puede repetir un despliegue o reintentar una publicación a medias.
# Se reutiliza una imagen de la misma etiqueta, que es el mismo commit.
set -euo pipefail

tag="${1:?uso: ecr-publish.sh <tag>}"
: "${REGISTRY:?}"
prefix="${IMAGE_PREFIX:-myapp}"
builder_ready=false

for t in api worker migrate; do
  if err="$(aws ecr describe-images --repository-name "$prefix-$t" --image-ids "imageTag=$tag" 2>&1 >/dev/null)"; then
    echo "$prefix-$t:$tag ya existe: se reutiliza"
    continue
  fi
  if [[ "$err" != *ImageNotFoundException* ]]; then
    # Un fallo de permisos o de red no significa que falte la imagen.
    echo "consultar $prefix-$t:$tag: $err" >&2
    exit 1
  fi
  if [[ "$builder_ready" == false ]]; then
    # El driver docker-container permite adjuntar SBOM y procedencia (SLSA) a la imagen.
    docker buildx create --use --name release >/dev/null
    builder_ready=true
  fi
  docker buildx build --target "$t" --build-arg VERSION="$tag" \
    --sbom=true --provenance=mode=max \
    --tag "$REGISTRY/$prefix-$t:$tag" --push .
done
