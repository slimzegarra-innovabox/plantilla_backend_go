#!/usr/bin/env bash
# Prueba de deploy/otel/adot-collector.yaml con la imagen ADOT real (necesita Docker, no
# AWS): envía por OTLP cada métrica declarada y comprueba, en un CloudWatch Logs falso,
# que sale como métrica EMF (bloque _aws) con sus dimensiones. Sin ese bloque, EMF es un
# simple log: CloudWatch no crea la métrica y sus alarmas no reciben datos.
#
# Los puntos se envían como gauge: el tipo no influye en la selección, y una suma o un
# histograma acumulados necesitarían dos envíos para producir un delta.
set -euo pipefail

# La imagen es la que fija production.yml: así no se prueba una versión y se despliega
# otra, también cuando Dependabot la actualiza.
fake_image="python:3.13.18-alpine@sha256:4d511039677430640754cd1417737349067dfcdaba4dcf7e576334516f9f4c30"
here="$(cd "$(dirname "$0")" && pwd)"
image="$(sed -n 's/^ *image: *\(.*aws-otel-collector.*\)$/\1/p' "$here/../../production.yml" | head -1)"
[[ "$image" == *@sha256:* ]] || { echo "FALLA: production.yml no fija la imagen de ADOT por digest" >&2; exit 1; }
tmp="$(mktemp -d)"
container="adot-test-$$"
cleanup() {
  docker rm -f "$container" "$container-fake" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT
fail() { docker logs "$container" 2>&1 | grep -E "\berror\b" | tail -3 >&2 || true; echo "FALLA: $*" >&2; exit 1; }

# CloudWatch Logs falso: guarda cada evento EMF, uno por línea. Comparte la red del
# colector, así que no depende del firewall del host ni de su sistema operativo.
cat >"$tmp/fake.py" <<'PY'
import http.server, json
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if "PutLogEvents" in self.headers.get("X-Amz-Target", ""):
            with open("/out/emf.log", "a") as f:
                for e in json.loads(body).get("logEvents", []):
                    f.write(e["message"] + "\n")
        self.send_response(200)
        self.send_header("Content-Type", "application/x-amz-json-1.1")
        self.end_headers()
        self.wfile.write(b"{}")
    def log_message(self, *a):
        pass
http.server.HTTPServer(("127.0.0.1", 8099), H).serve_forever()
PY

# La configuración real; solo se apunta el exportador al servidor falso.
sed 's|^  awsemf:|  awsemf:\n    endpoint: http://127.0.0.1:8099\n    region: us-east-1|' \
  "$here/adot-collector.yaml" >"$tmp/config.yaml"
chmod a+r "$tmp/config.yaml" "$tmp/fake.py"

docker run -d --name "$container" -p 127.0.0.1::4318 \
  -e AWS_REGION=us-east-1 -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  -v "$tmp/config.yaml:/config.yaml:ro" "$image" --config /config.yaml >/dev/null
docker run -d --name "$container-fake" --network "container:$container" --user "$(id -u):$(id -g)" \
  -v "$tmp:/out" "$fake_image" python -I /out/fake.py >/dev/null
endpoint="http://$(docker port "$container" 4318/tcp | head -1)/v1/metrics"

# <métrica> <atributos, como "clave=valor,clave=valor">: uno por declaración del colector.
metrics=(
  "http.server.request.duration http.route=/x,http.request.method=GET,http.response.status_code=200"
  "messaging.process.duration messaging.destination.name=t,outcome=ok"
  "outbox.pending "
  "outbox.oldest_pending_age "
  "outbox.dead "
  "outbox.publish.count outcome=published,topic=t"
  "db.client.connection.count db.client.connection.state=idle"
  "db.client.connection.max "
  "db.client.connection.wait_count "
  "db.client.connection.wait_time "
  "go.memory.used "
  "go.goroutine.count "
)
now="$(date +%s)000000000"
payload="$(printf '%s\n' "${metrics[@]}" | jq -Rn --arg now "$now" '
  [inputs | split(" ") as [$name, $attrs] | {
    name: $name,
    gauge: {dataPoints: [{asInt: "7", timeUnixNano: $now, attributes: (
      ($attrs // "") | split(",") | map(split("=") | {key: .[0], value: {stringValue: .[1]}}))}]}
  }]
  | {resourceMetrics: [{
      resource: {attributes: [{key: "service.name", value: {stringValue: "myapp-test"}}]},
      scopeMetrics: [{scope: {name: "test"}, metrics: .}]}]}')"

sent=false
for _ in $(seq 60); do
  # Reenvía hasta que el receptor acepte: el colector tarda en arrancar.
  curl -fsS -o /dev/null -X POST -H 'Content-Type: application/json' -d "$payload" "$endpoint" 2>/dev/null && { sent=true; break; }
  sleep 1
done
[[ "$sent" == true ]] || fail "el colector no aceptó OTLP en $endpoint"

# Espera a que llegue todo; el exportador agrupa y vacía cada pocos segundos.
for _ in $(seq 60); do
  [[ -s "$tmp/emf.log" ]] && [[ "$(jq -r 'to_entries[] | select(.value | type == "number") | .key' "$tmp/emf.log" | sort -u | wc -l)" -ge "${#metrics[@]}" ]] && break
  sleep 1
done
[[ -s "$tmp/emf.log" ]] || fail "no llegó ningún evento a CloudWatch Logs"

for entry in "${metrics[@]}"; do
  name="${entry%% *}"; attrs="${entry#* }"
  expected="$(jq -cn --arg a "$attrs" '["service.name"] + (($a | split(",")) | map(split("=")[0]))')"
  jq -es --arg m "$name" --argjson d "$expected" '
    [.[] | select(has($m)) | ._aws.CloudWatchMetrics[]?
         | select(.Namespace == "MyApp" and any(.Metrics[]; .Name == $m))
         | .Dimensions[] | sort] | any(. == ($d | sort))' "$tmp/emf.log" >/dev/null \
    || fail "$name no salió como métrica EMF de MyApp con dimensiones $expected. Evento: $(jq -c --arg m "$name" 'select(has($m))' "$tmp/emf.log" | head -1)"
done

echo "ok  deploy/otel/adot-collector.yaml"
