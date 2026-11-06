#!/usr/bin/env bash
# Prueba de scripts/ci/check-migrations.sh sobre repositorios git temporales.
set -euo pipefail
check="$(cd "$(dirname "$0")" && pwd)/check-migrations.sh"
fail() { echo "FALLA: $*" >&2; exit 1; }

# scenario <nombre> <debe_pasar:0|1> <comandos sobre el repo, después del commit base>
scenario() {
  local name="$1" want="$2" cmds="$3" repo
  repo="$(mktemp -d)"
  (
    cd "$repo"
    git init -q && git config user.email t@t && git config user.name t
    mkdir -p scripts/db/migrations
    echo "CREATE TABLE a();" >scripts/db/migrations/000001_create_a.up.sql
    echo "DROP TABLE a;" >scripts/db/migrations/000001_create_a.down.sql
    git add -A && git commit -qm base && git tag base
    eval "$cmds"
    git add -A && git commit -qm cambio --allow-empty
    if "$check" base >/dev/null 2>&1; then got=0; else got=1; fi
    [[ "$got" == "$want" ]] || fail "$name: se esperaba $( ((want)) && echo fallo || echo éxito )"
  )
  rm -rf "$repo"
}

scenario "sin cambios" 0 ":"
scenario "migración nueva con su par" 0 "echo x >scripts/db/migrations/000002_add_b.up.sql; echo y >scripts/db/migrations/000002_add_b.down.sql"
scenario "editar una ya integrada" 1 "echo cambio >>scripts/db/migrations/000001_create_a.up.sql"
scenario "borrar una ya integrada" 1 "git rm -q scripts/db/migrations/000001_create_a.down.sql"
scenario "renombrar una ya integrada" 1 "git mv scripts/db/migrations/000001_create_a.up.sql scripts/db/migrations/000001_crear_a.up.sql"
scenario "nueva sin par" 1 "echo x >scripts/db/migrations/000002_add_b.up.sql"
scenario "número intercalado" 1 "echo x >scripts/db/migrations/000001_otra.up.sql; echo y >scripts/db/migrations/000001_otra.down.sql"
scenario "nombre inválido" 1 "echo x >scripts/db/migrations/2_AddB.up.sql"
scenario "otros archivos no importan" 0 "mkdir -p internal && echo x >internal/a.go"

echo "ok  scripts/ci/check-migrations.sh"
