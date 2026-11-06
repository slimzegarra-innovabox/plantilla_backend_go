// Prueba de carga del módulo example (roadmap #13). Se ejecuta con `make load`.
//
// Mezcla parecida a una API real: mayoría de lecturas y algunas escrituras. La carga se
// define por llegadas por segundo (arrival rate): si el servidor se atrasa, las
// peticiones se acumulan como en producción, y no se oculta la saturación.
//
//   RATE=300 DURATION=60s   carga sostenida (por defecto)
//   MODE=ramp               sube de a poco hasta encontrar el punto de saturación
import http from 'k6/http';
import crypto from 'k6/crypto';
import encoding from 'k6/encoding';
import { check } from 'k6';
import { Rate } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:18080';
const RATE = Number(__ENV.RATE || 300);
const DURATION = __ENV.DURATION || '60s';
const SECRET = __ENV.JWT_SECRET;
const USER = '11111111-1111-4111-8111-111111111111';

// Una respuesta inesperada: distinta de las que cada operación admite.
const unexpected = new Rate('unexpected_responses');

const steady = {
  executor: 'constant-arrival-rate',
  rate: RATE,
  timeUnit: '1s',
  duration: DURATION,
  preAllocatedVUs: 100,
  maxVUs: 1000,
};

const ramp = {
  executor: 'ramping-arrival-rate',
  startRate: 100,
  timeUnit: '1s',
  preAllocatedVUs: 200,
  maxVUs: 2000,
  stages: [
    { target: 500, duration: '20s' },
    { target: 1000, duration: '20s' },
    { target: 2000, duration: '20s' },
    { target: 4000, duration: '20s' },
  ],
};

export const options = {
  scenarios: { load: __ENV.MODE === 'ramp' ? ramp : steady },
  // Objetivos de la carga sostenida. En modo ramp se espera superarlos: es lo que se busca.
  thresholds: __ENV.MODE === 'ramp' ? {} : {
    'http_req_duration{op:get}': ['p(95)<50'],
    'http_req_duration{op:list}': ['p(95)<150'],
    'http_req_duration{op:search}': ['p(95)<200'],
    'http_req_duration{op:create}': ['p(95)<100'],
    'http_req_duration{op:update}': ['p(95)<100'],
    unexpected_responses: ['rate<0.001'],
  },
  discardResponseBodies: false,
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

function b64url(s) {
  return encoding.b64encode(s, 'rawurl');
}

// Token HS256 con el mismo formato que verifica la API.
function token() {
  const now = Math.floor(Date.now() / 1000);
  const head = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const body = b64url(JSON.stringify({
    sub: USER, iss: __ENV.JWT_ISSUER, aud: __ENV.JWT_AUDIENCE, iat: now, nbf: now, exp: now + 7200,
    scope: 'example:read example:write',
  }));
  const sig = crypto.hmac('sha256', SECRET, `${head}.${body}`, 'base64rawurl');
  return `${head}.${body}.${sig}`;
}

// setup toma uuids existentes para las lecturas y las modificaciones.
export function setup() {
  const auth = { headers: { Authorization: `Bearer ${token()}` } };
  const res = http.get(`${BASE}/api/v1/examples?size=100`, auth);
  if (res.status !== 200) throw new Error(`setup: ${res.status} ${res.body}`);
  return { token: token(), ids: res.json('data').map((e) => e.uuid) };
}

function pick(list) {
  return list[Math.floor(Math.random() * list.length)];
}

export default function (data) {
  const headers = { Authorization: `Bearer ${data.token}`, 'Content-Type': 'application/json' };
  const r = Math.random();
  let res;
  let ok;

  if (r < 0.5) {
    res = http.get(`${BASE}/api/v1/examples/${pick(data.ids)}`, { headers, tags: { op: 'get' } });
    ok = res.status === 200;
  } else if (r < 0.75) {
    const page = 1 + Math.floor(Math.random() * 50);
    res = http.get(`${BASE}/api/v1/examples?page=${page}&size=20`, { headers, tags: { op: 'list' } });
    ok = res.status === 200;
  } else if (r < 0.85) {
    res = http.get(`${BASE}/api/v1/examples?q=${crypto.md5(String(Math.random()), 'hex').slice(0, 4)}&size=20`, { headers, tags: { op: 'search' } });
    ok = res.status === 200;
  } else if (r < 0.95) {
    res = http.post(`${BASE}/api/v1/examples`, JSON.stringify({ name: `k6 ${__VU}-${__ITER}-${crypto.md5(String(Math.random()), 'hex').slice(0, 8)}` }), {
      headers: { ...headers, 'Idempotency-Key': crypto.md5(String(Math.random()), 'hex') }, tags: { op: 'create' },
    });
    ok = res.status === 201;
  } else {
    // Una modificación real: leer para conocer la versión y escribir. Un 409 por versión
    // es correcto: otro escritor concurrente se adelantó sobre el mismo registro.
    const id = pick(data.ids);
    const current = http.get(`${BASE}/api/v1/examples/${id}`, { headers, tags: { op: 'get' } });
    res = http.put(`${BASE}/api/v1/examples/${id}`, JSON.stringify({
      name: `k6 upd ${crypto.md5(String(Math.random()), 'hex').slice(0, 10)}`, version: current.json('data.version'),
    }), { headers, tags: { op: 'update' } });
    ok = res.status === 200 || res.status === 409;
  }

  unexpected.add(!ok);
  check(res, { 'respuesta esperada': () => ok });
}
