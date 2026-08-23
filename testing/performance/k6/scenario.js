// Reusable HTTP-level load-test scenario — k6 (Grafana's own load-test engine), not a hand-rolled
// stress-test client (`testing/README.md`'s Performance section). One scenario per invocation
// (`LABEL`/`QS` env vars), run for any entity/route — nothing here is `crm.customers`-specific.
// Orchestrated by `run.sh`, which sequences seed -> rate-limit-bucket wait -> one `docker compose
// run` per scenario; this file only knows about a single scenario's request shape.
import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://host.docker.internal:3000';
const ENTITY = __ENV.ENTITY;
const TOKEN = __ENV.TOKEN;
const QS = __ENV.QS || '';
const LABEL = __ENV.LABEL || 'scenario';
const REQUESTS = Number(__ENV.REQUESTS || 250);
const CONCURRENCY = Number(__ENV.CONCURRENCY || 20);
// k6 scenario keys only allow `[a-zA-Z0-9_-]` — `LABEL` (e.g. "filter+sort") can't be used
// directly as the key (verified live: k6 rejects "+" with "configuration errors"). The `scenario`
// *tag* on each metric below still carries the original, human-readable `LABEL`.
const SCENARIO_KEY = LABEL.replace(/[^a-zA-Z0-9_-]/g, '_');

export const options = {
  scenarios: {
    [SCENARIO_KEY]: {
      executor: 'shared-iterations',
      vus: CONCURRENCY,
      iterations: REQUESTS,
      maxDuration: '5m',
    },
  },
  thresholds: {
    http_req_failed: ['rate==0'],
  },
};

function authedGet(qs) {
  const res = http.get(`${BASE_URL}/api/${ENTITY}${qs}`, {
    headers: { Authorization: `Bearer ${TOKEN}` },
    tags: { scenario: LABEL },
  });
  check(res, { 'status is 200': (r) => r.status === 200 });
  return res;
}

// `LABEL === "cursor"` runs a two-step keyset pagination scenario: fetch a real `nextCursor`
// once (setup(), single VU, not part of the timed load), then every VU appends it to QS.
export function setup() {
  if (LABEL !== 'cursor') {
    return {};
  }
  const first = http.get(`${BASE_URL}/api/${ENTITY}${QS}`, {
    headers: { Authorization: `Bearer ${TOKEN}` },
  });
  const body = first.json();
  const cursor = body && body.page && body.page.nextCursor;
  if (!cursor) {
    console.log('no nextCursor returned (dataset smaller than one page?) — cursor scenario will just repeat QS');
  }
  return { cursor };
}

export default function (data) {
  let qs = QS;
  if (LABEL === 'cursor' && data && data.cursor) {
    const sep = qs.includes('?') ? '&' : '?';
    qs = `${qs}${sep}cursor=${encodeURIComponent(data.cursor)}`;
  }
  authedGet(qs);
}
