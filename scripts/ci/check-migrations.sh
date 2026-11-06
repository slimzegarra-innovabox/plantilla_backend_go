#!/usr/bin/env bash
# Verifica la regla de CLAUDE.md §12.2: «nunca se edita una migración ya aplicada».
#
#   scripts/ci/check-migrations.sh <base>     # <base>: commit/rama con lo ya integrado
#
# Falla si, respecto de <base>:
#   - se modifica, borra o renombra un archivo de migración existente;
#   - un archivo nuevo no sigue {6 dígitos}_{snake_case}.{up|down}.sql;
#   - una migración nueva no tiene su par up/down;
#   - el número de una migración nueva no es mayor que el último de <base>
#     (una migración intercalada nunca se aplicaría en entornos ya migrados).
set -euo pipefail

base="${1:?uso: check-migrations.sh <base>}"
dir="scripts/db/migrations"
fail=0
err() { echo "ERROR: $*" >&2; fail=1; }

# Cambios respecto de la base, sin detectar renombres (un renombre es borrar + agregar).
while IFS=$'\t' read -r status file; do
  [[ -z "$status" ]] && continue
  if [[ "$status" != "A" ]]; then
    err "$file: una migración ya integrada no se modifica ni se borra ($status). Cree una migración nueva (CLAUDE.md §12.2)."
  fi
done < <(git diff --name-status --no-renames "$base" HEAD -- "$dir")

last_base="$(git ls-tree --name-only "$base" "$dir/" | sed -n 's#.*/\([0-9]\{6\}\)_.*#\1#p' | sort -n | tail -1)"
last_base="${last_base:-000000}"

while read -r file; do
  [[ -z "$file" ]] && continue
  name="$(basename "$file")"
  if [[ ! "$name" =~ ^([0-9]{6})_[a-z0-9]+(_[a-z0-9]+)*\.(up|down)\.sql$ ]]; then
    err "$file: el nombre debe ser {6 dígitos}_{descripcion_snake_case}.{up|down}.sql"
    continue
  fi
  num="${BASH_REMATCH[1]}"
  if ((10#$num <= 10#$last_base)); then
    err "$file: el número $num no es mayor que el último ya integrado ($last_base)"
  fi
  pair="${file%.*.sql}"
  [[ "$name" == *.up.sql ]] && pair="$pair.down.sql" || pair="$pair.up.sql"
  if [[ ! -f "$pair" ]]; then
    err "$file: falta su par $(basename "$pair")"
  fi
done < <(git diff --name-only --diff-filter=A --no-renames "$base" HEAD -- "$dir")

if ((fail)); then
  exit 1
fi
echo "ok  migraciones: sin cambios en las ya integradas (base $(git rev-parse --short "$base"))"
