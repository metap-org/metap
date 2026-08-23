// Seeds SEED_COUNT rows for a load-test run, in parallel across CONCURRENCY VUs — same k6
// engine as `scenario.js` (`testing/README.md`'s Performance section), not a shell loop.
import http from 'k6/http';

const BASE_URL = __ENV.BASE_URL || 'http://host.docker.internal:3000';
const ENTITY = __ENV.ENTITY;
const TOKEN = __ENV.TOKEN;
const TEMPLATE = __ENV.SEED_TEMPLATE; // JSON object text with `{i}` placeholders, e.g. {"code":"LOAD-{i}"}
const SEED_COUNT = Number(__ENV.SEED_COUNT || 500);
const CONCURRENCY = Number(__ENV.CONCURRENCY || 20);
const RUN_TAG = __ENV.RUN_TAG || String(Date.now());

export const options = {
  scenarios: {
    seed: {
      executor: 'shared-iterations',
      vus: CONCURRENCY,
      iterations: SEED_COUNT,
      maxDuration: '5m',
    },
  },
};

export default function () {
  const i = `${RUN_TAG}-${__VU}-${__ITER}`;
  const payload = JSON.parse(TEMPLATE.replace(/\{i\}/g, i));
  http.post(`${BASE_URL}/api/${ENTITY}`, JSON.stringify({ data: payload }), {
    headers: { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' },
  });
}
