#!/usr/bin/env bash
# Prueba de scripts/ci/coverage.sh con un perfil fijo.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FALLA: $*" >&2; exit 1; }

cat >"$tmp/c.out" <<'PROFILE'
mode: atomic
mod/internal/a/x.go:1.1,2.2 3 1
mod/internal/a/x.go:3.1,4.2 1 0
mod/internal/b/y.go:1.1,2.2 2 0
mod/internal/b/y.go:1.1,2.2 2 5
mod/internal/b/y.go:3.1,4.2 2 0
mod/internal/a/port/porttest/memory.go:1.1,2.2 50 0
mod/tools/modulegen/main.go:1.1,2.2 50 0
mod/test/e2e/e2e.go:1.1,2.2 50 0
PROFILE

out="$("$here/coverage.sh" "$tmp/c.out" "50.0" "$tmp/total")"

# a: 3/4 = 75; b: 2/4 = 50 (el bloque repetido cuenta una vez y cubierto); total 5/8.
grep -qF '### Cobertura: 62.5% (+12.5 puntos frente a la última de main: 50.0%)' <<<"$out" || fail "total o tendencia: $out"
grep -qF "| 50.0% | \`internal/b\` |" <<<"$out" || fail "paquete b: $out"
grep -qF "| 75.0% | \`internal/a\` |" <<<"$out" || fail "paquete a: $out"
[[ "$(cat "$tmp/total")" == "62.5" ]] || fail "archivo del total"
! grep -qE 'porttest|tools|e2e\.go' <<<"$out" || fail "se debían excluir los helpers: $out"
[[ "$(grep -n 'internal/b' <<<"$out" | cut -d: -f1)" -lt "$(grep -n 'internal/a' <<<"$out" | cut -d: -f1)" ]] || fail "orden de menor a mayor"
plain="$("$here/coverage.sh" "$tmp/c.out")"
grep -qF '### Cobertura: 62.5%' <<<"$plain" || fail "sin tendencia"
! grep -q 'frente a' <<<"$plain" || fail "sin total anterior no hay comparación"

echo "ok  scripts/ci/coverage.sh"
