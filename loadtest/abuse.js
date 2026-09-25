// Abuse test: requests that try to break the service. Most cases must fail with a
// 4xx, so a 4xx is not a failure here. Only a 5xx, or a status other than the
// documented one, fails the run.
//
//   k6 run -e BASE_URL=https://fizzbuzz.khalizov.com loadtest/abuse.js
//   k6 run -e BASE_URL=https://fizzbuzz.khalizov.com -e DURATION=2m loadtest/abuse.js
//
// Responses stay below 10 MB here, so k6 can hold a body per VU and check it.
// The largest responses are in huge.js, which discards them.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

// A status other than the documented one, by status and case. The summary
// names the status that arrived.
const unexpected = new Counter('unexpected_status');

const BASE = __ENV.BASE_URL || 'https://fizzbuzz.khalizov.com';
const DURATION = __ENV.DURATION || '30s';

// Every documented rejection is a 4xx, so tag it expected. http_req_failed then
// counts only real server errors.
http.setResponseCallback(http.expectedStatuses({ min: 200, max: 499 }));

const MARK = 'zqmarker7f3a'; // a param value and a path that must not reach /metrics

// A host on the internet keeps the probe endpoints closed. Both answers are
// correct, so the cases follow the host.
const PUBLIC = !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])(:|$)/.test(BASE);

export const options = {
  scenarios: {
    abuse: {
      executor: 'constant-arrival-rate',
      exec: 'abuse',
      rate: Number(__ENV.RATE || 20),
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 10,
      maxVUs: 100,
    },
  },
  thresholds: {
    'http_req_failed': ['rate==0'], // one 5xx fails the run
    'checks': ['rate>0.99'],
  },
};

const OK = 'int1=3&int2=5&limit=100&str1=fizz&str2=buzz';
const JUNK = Array.from({ length: 50 }, (_, i) => `junk${i}=x`).join('&');
const LONG_STR = 'a'.repeat(1024);       // the byte limit exactly
const MULTI = 'é'.repeat(400);           // 800 bytes, 400 runes
const ESCAPE = '"\u0001<&>\\\u{1D11E}';  // JSON escapes and an astral rune

// Each case states the answer the service documents. errContains checks the
// first failing parameter, in the documented order.
const CASES = [
  // Valid requests at the edges.
  { name: 'limit 100k', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100000&str1=fizz&str2=buzz` },
  { name: 'int1=1 repeats a period', url: `${BASE}/fizzbuzz?int1=1&int2=7&limit=100000&str1=fizz&str2=buzz` },
  { name: 'equal divisors', url: `${BASE}/fizzbuzz?int1=5&int2=5&limit=1000&str1=ab&str2=cd` },
  { name: 'int64 max divisors', url: `${BASE}/fizzbuzz?int1=9223372036854775807&int2=1&limit=1000&str1=x&str2=y` },
  { name: 'str is 1024 bytes', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=${LONG_STR}&str2=buzz` },
  { name: 'str is 800 bytes of 2-byte runes', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=${encodeURIComponent(MULTI)}&str2=buzz` },
  { name: 'str needs JSON escapes', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=${encodeURIComponent(ESCAPE)}&str2=buzz` },
  { name: '50 unknown params', url: `${BASE}/fizzbuzz?${OK}&${JUNK}` },
  { name: 'leading zeros', url: `${BASE}/fizzbuzz?int1=03&int2=05&limit=0100&str1=fizz&str2=buzz` },
  { name: 'reordered params', url: `${BASE}/fizzbuzz?str2=buzz&str1=fizz&limit=100&int2=5&int1=3` },
  { name: 'marker as a param value', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=10&str1=${MARK}&str2=b` },
  { name: 'HEAD sets no body', method: 'HEAD', url: `${BASE}/fizzbuzz?${OK}`, head: true },
  { name: 'QUERY method', method: 'QUERY', url: `${BASE}/fizzbuzz`, body: OK, form: true },
  { name: 'stats', url: `${BASE}/stats`, json: 'object' },
  { name: 'stats ignores a query', url: `${BASE}/stats?who=me`, json: 'object' },
  { name: 'healthz', url: `${BASE}/healthz`, want: PUBLIC ? [403, 404] : 200, json: 'object' },

  // Rejected parameters. The range check names the first bad one.
  { name: 'int1=0', url: `${BASE}/fizzbuzz?int1=0&int2=5&limit=100&str1=f&str2=b`, want: 400, errContains: 'int1' },
  { name: 'int1=-1', url: `${BASE}/fizzbuzz?int1=-1&int2=5&limit=100&str1=f&str2=b`, want: 400, errContains: 'int1' },
  { name: 'int1=1.5', url: `${BASE}/fizzbuzz?int1=1.5&int2=5&limit=100&str1=f&str2=b`, want: 400, errContains: 'int1' },
  { name: 'int1 empty', url: `${BASE}/fizzbuzz?int1=&int2=5&limit=100&str1=f&str2=b`, want: 400, errContains: 'int1' },
  { name: 'int1 above int64', url: `${BASE}/fizzbuzz?int1=9223372036854775808&int2=5&limit=100&str1=f&str2=b`, want: 400, errContains: 'int1' },
  { name: 'int1 twice', url: `${BASE}/fizzbuzz?int1=3&int1=5&int2=5&limit=100&str1=f&str2=b`, want: 400, errContains: 'int1' },
  { name: 'str2 missing', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=f`, want: 400, errContains: 'str2' },
  { name: 'limit=0 beats bad str1', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=0&str1=&str2=b`, want: 400, errContains: 'limit' },
  { name: 'limit=-5', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=-5&str1=f&str2=b`, want: 400, errContains: 'limit' },
  { name: 'limit above max', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=10000001&str1=f&str2=b`, want: 400, errContains: 'limit' },
  { name: 'limit=1e3', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=1e3&str1=f&str2=b`, want: 400, errContains: 'limit' },
  { name: 'str1 empty', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=&str2=b`, want: 400, errContains: 'str1' },
  { name: 'str1 is 1025 bytes', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=${'a'.repeat(1025)}&str2=b`, want: 400, errContains: 'str1' },
  // Hardcoded escapes: encodeURIComponent cannot emit invalid UTF-8 or a bad escape.
  { name: 'str1 is not UTF-8', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=%FF&str2=b`, want: 400, errContains: 'str1' },
  { name: 'malformed percent escape', url: `${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=%ZZ&str2=b`, want: 400, errContains: 'query' },

  // Wrong method, wrong path, traversal.
  { name: 'POST is not allowed', method: 'POST', url: `${BASE}/fizzbuzz?${OK}`, want: 405 },
  { name: 'PUT on stats', method: 'PUT', url: `${BASE}/stats`, want: 405 },
  { name: 'unknown path', url: `${BASE}/zzqmarkerpath`, want: 404 },
  { name: 'path traversal to source', url: `${BASE}/../go.mod`, want: [400, 403, 404] },
  { name: 'encoded traversal', url: `${BASE}/%2e%2e%2fgo.mod`, want: [400, 403, 404] },
  { name: 'traversal out of web', url: `${BASE}/web/../../go.mod`, want: [400, 403, 404] },
  // A CDN in front rejects an oversized header with 403. The service answers
  // 431 when it gets one.
  { name: 'header is 20 KiB', url: `${BASE}/healthz`, headers: { 'X-Pad': 'p'.repeat(20000) }, want: [403, 431] },
];

export function abuse() {
  const c = CASES[__ITER % CASES.length];
  const want = c.want || 200;
  const headers = { ...(c.headers || {}) };
  if (c.form) headers['Content-Type'] = 'application/x-www-form-urlencoded';
  const res = http.request(c.method || 'GET', c.url, c.body || null, {
    headers,
    // A served body is checked in bytes: a JS string length counts UTF-16
    // units, not bytes, and an error body is read as text.
    responseType: want === 200 && !c.head ? 'binary' : 'text',
    tags: { name: `abuse: ${c.name}` },
  });
  const good = Array.isArray(want) ? want.includes(res.status) : res.status === want;
  if (!good) unexpected.add(1, { status: String(res.status), case: c.name });
  check(res, {
    [`${c.name}: status is ${want}`]: () => good,
  });
  // A rejected request always has a reason. A served request has a JSON body of
  // the documented shape.
  const opens = c.json === 'object' ? 0x7b : 0x5b; // '{' or '['
  const closes = c.json === 'object' ? 0x7d : 0x5d;
  if (want === 200 && !c.head) {
    check(res, {
      [`${c.name}: Content-Length matches the body`]: (r) =>
        Number(r.headers['Content-Length']) === r.body.byteLength,
      [`${c.name}: body is JSON`]: (r) => {
        const b = new Uint8Array(r.body);
        return b[0] === opens && b[b.length - 1] === closes;
      },
    });
  }
  if (want === 200 && c.head) {
    check(res, {
      [`${c.name}: Content-Length announced`]: (r) => Number(r.headers['Content-Length']) > 0,
      [`${c.name}: no body`]: (r) => !r.body || r.body.length === 0,
    });
  }
  if (want !== 200) {
    check(res, { [`${c.name}: leaks no Go source`]: (r) => !String(r.body).includes('package main') });
  }
  if (c.errContains) {
    check(res, { [`${c.name}: error names ${c.errContains}`]: (r) => String(r.json('error')).includes(c.errContains) });
  }
}

// Labels must stay bounded: no raw path and no param value may reach /metrics,
// and the number of series must not depend on the traffic. A public host must
// not serve /metrics at all, so there the check is that it stays closed.
export function teardown() {
  const res = http.get(`${BASE}/metrics`);
  if (res.status !== 200) {
    check(res, { 'metrics: not public': (r) => r.status === 403 || r.status === 404 });
    console.log(`/metrics is closed (${res.status}); the label check runs where it is open`);
    return;
  }
  const body = res.body || '';
  const series = body.split('\n').filter((l) => l.startsWith('http_request_duration_seconds_bucket')).length;
  check(res, {
    'metrics: no param value in a label': () => !body.includes(MARK),
    'metrics: no raw path in a label': () => !body.includes('zzqmarkerpath'),
    'metrics: series stay bounded': () => series > 0 && series < 200,
  });
  console.log(`http_request_duration_seconds series: ${series}`);
}
