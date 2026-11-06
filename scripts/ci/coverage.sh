#!/usr/bin/env bash
# Resume un perfil de cobertura de `go test` en Markdown (roadmap #14). Es informativo:
# no hay umbral obligatorio y nunca hace fallar el CI.
#
#   scripts/ci/coverage.sh <coverage.out> [total_anterior] [archivo_total]
#
# - Excluye lo que no es código de producción: dobles y helpers de prueba, y tools/.
# - total_anterior: el total de la última ejecución en verde de main, para la tendencia.
# - archivo_total: dónde guardar el total, para que la próxima ejecución lo compare.
set -euo pipefail

profile="${1:?uso: coverage.sh <coverage.out> [total_anterior] [archivo_total]}"
previous="${2:-}"
total_file="${3:-}"
exclude='/(porttest|pgtest|openapitest|idempotencytest)/|/tools/|/test/'

# Cada línea: archivo:rango sentencias contador. Con varios paquetes, la misma línea se
# cuenta una vez, cubierta si alguna ejecución pasó por ella.
summary="$(awk -v exclude="$exclude" '
  NR == 1 { next }
  $1 ~ exclude { next }
  { stmts[$1] = $2; if ($3 > 0) hit[$1] = 1 }
  END {
    for (block in stmts) {
      file = block; sub(/:.*/, "", file)
      pkg = file; sub(/\/[^\/]*$/, "", pkg)
      total[pkg] += stmts[block]; all += stmts[block]
      if (block in hit) { covered[pkg] += stmts[block]; allc += stmts[block] }
    }
    for (pkg in total) printf "%.1f\t%s\n", 100 * covered[pkg] / total[pkg], pkg
    printf "TOTAL\t%.1f\n", (all > 0 ? 100 * allc / all : 0)
  }' "$profile")"

current="$(awk -F'\t' '$1 == "TOTAL" { print $2 }' <<<"$summary")"
[[ -n "$total_file" ]] && echo "$current" >"$total_file"

trend=""
if [[ -n "$previous" ]]; then
  trend="$(awk -v c="$current" -v p="$previous" 'BEGIN { d = c - p; printf " (%+.1f puntos frente a la última de main: %.1f%%)", d, p }')"
fi

echo "### Cobertura: ${current}%${trend}"
echo
echo "Incluye las pruebas de integración y lo que ejecutan los binarios en \`test/e2e\`."
echo
echo "<details><summary>Por paquete, de menor a mayor</summary>"
echo
echo "| Cobertura | Paquete |"
echo "|---|---|"
grep -v '^TOTAL' <<<"$summary" | sort -n | awk -F'\t' '{ sub(/^[^\/]*\//, "", $2); printf "| %s%% | `%s` |\n", $1, $2 }'
echo
echo "</details>"
