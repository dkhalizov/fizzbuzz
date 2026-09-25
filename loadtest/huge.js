// Largest responses: limit=10,000,000 is a response of about 88 MB. Each one is
// downloaded whole, so VUS requests at once also need that much of the client's
// own bandwidth. Run one at a time to measure the service. Increase VUS only
// when the client link has capacity. If not, the client becomes the bottleneck,
// and its timeouts look like service failures.
//
//   k6 run -e BASE_URL=https://fizzbuzz.khalizov.com loadtest/huge.js
//   k6 run -e BASE_URL=https://fizzbuzz.khalizov.com -e VUS=4 -e ITERATIONS=10 loadtest/huge.js
//
// The script discards the bodies. Only the announced size is important. Watch
// the data_received line in the summary: it is traffic that you pay for.
// MAX_INFLIGHT_BYTES is for several of these at the same time. The service
// answers 200, or 503 with Retry-After. It never holds a full response in memory.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'https://fizzbuzz.khalizov.com';
const VUS = Number(__ENV.VUS || 1);
const ITERATIONS = Number(__ENV.ITERATIONS || 5);

const busy = new Counter('admission_503');
const unexpected = new Counter('unexpected_status');

export const options = {
  discardResponseBodies: true,
  scenarios: {
    huge: {
      executor: 'per-vu-iterations',
      exec: 'huge',
      vus: VUS,
      iterations: ITERATIONS,
      maxDuration: __ENV.MAX_DURATION || '10m',
    },
  },
  thresholds: {
    'checks': ['rate>0.99'],
  },
};

// Different shapes, all of them at the top limit. The period repeat is the
// path with int1=1: no number appears and the bytes repeat.
const CASES = [
  `${BASE}/fizzbuzz?int1=3&int2=5&limit=10000000&str1=fizz&str2=buzz`,
  `${BASE}/fizzbuzz?int1=1&int2=1&limit=10000000&str1=fizz&str2=buzz`,
  `${BASE}/fizzbuzz?int1=1000000&int2=1000001&limit=10000000&str1=fizz&str2=buzz`,
  `${BASE}/fizzbuzz?int1=9999&int2=9998&limit=10000000&str1=${encodeURIComponent('é'.repeat(400))}&str2=buzz`,
];

export function huge() {
  const url = CASES[__ITER % CASES.length];
  const res = http.get(url, { tags: { name: 'huge' } });
  if (res.status === 503) busy.add(1);
  const good = res.status === 200 || (res.status === 503 && !!res.headers['Retry-After']);
  const len = Number(res.headers['Content-Length']);
  if (!good) unexpected.add(1, { status: String(res.status) });
  // The exact size depends on the words, so the check is a floor, and the
  // counter records the truncated size instead.
  else if (len < 80e6) unexpected.add(1, { status: `short body ${len}` });
  check(res, {
    '200, or 503 with Retry-After': () => good,
    'announced size is at least 80 MB': () => good && len >= 80e6,
  });
}
