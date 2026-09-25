// Load test for a deployed instance. Run it with:
//
//   k6 run loadtest/k6.js
//   k6 run -e BASE_URL=http://localhost:8080 -e RATE=200 -e DURATION=2m loadtest/k6.js
//
// Each request counts in /stats. The "small" scenario uses the classic
// parameters, so the classic request stays the winner in /stats after a run.
import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.BASE_URL || 'https://fizzbuzz.khalizov.com';
const RATE = Number(__ENV.RATE || 20); // requests per second for the small scenario
const DURATION = __ENV.DURATION || '1m';

export const options = {
  scenarios: {
    small: {
      executor: 'constant-arrival-rate',
      exec: 'small',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 20,
      maxVUs: 200,
    },
    mixed: {
      executor: 'constant-arrival-rate',
      exec: 'mixed',
      rate: Math.max(1, Math.floor(RATE / 4)),
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 10,
      maxVUs: 100,
    },
    query: {
      executor: 'constant-arrival-rate',
      exec: 'query',
      rate: Math.max(1, Math.floor(RATE / 4)),
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 5,
      maxVUs: 50,
    },
    stats: {
      executor: 'constant-arrival-rate',
      exec: 'stats',
      rate: 1,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 2,
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    'checks': ['rate>0.99'],
    'http_req_duration{scenario:small}': ['p(95)<300'],
    'http_req_duration{scenario:query}': ['p(95)<300'],
    'http_req_duration{scenario:stats}': ['p(95)<300'],
    'http_req_duration{scenario:mixed}': ['p(95)<2000'],
  },
};

// The body is read as bytes: a JS string length counts UTF-16 units, not bytes.
const BINARY = { responseType: 'binary' };

// The byte count must equal Content-Length, and the body must be a JSON array.
function checkArray(res) {
  check(res, {
    'status is 200': (r) => r.status === 200,
    'Content-Length matches the body': (r) =>
      Number(r.headers['Content-Length']) === r.body.byteLength,
    'body is a JSON array': (r) => {
      const b = new Uint8Array(r.body);
      return b[0] === 0x5b && b[b.length - 1] === 0x5d; // '[' and ']'
    },
  });
}

export function small() {
  checkArray(http.get(`${BASE}/fizzbuzz?int1=3&int2=5&limit=100&str1=fizz&str2=buzz`,
    { ...BINARY, tags: { name: 'fizzbuzz small' } }));
}

const LIMITS = [1000, 10000, 100000];
const WORDS = ['fizz', 'buzz', 'foo', 'bar', 'é', '<&>'];
const pick = (a) => a[Math.floor(Math.random() * a.length)];

export function mixed() {
  const q = [
    `int1=${1 + Math.floor(Math.random() * 20)}`,
    `int2=${1 + Math.floor(Math.random() * 20)}`,
    `limit=${pick(LIMITS)}`,
    `str1=${encodeURIComponent(pick(WORDS))}`,
    `str2=${encodeURIComponent(pick(WORDS))}`,
  ].join('&');
  checkArray(http.get(`${BASE}/fizzbuzz?${q}`, { ...BINARY, tags: { name: 'fizzbuzz mixed' } }));
}

export function query() {
  checkArray(http.request('QUERY', `${BASE}/fizzbuzz`,
    'int1=3&int2=5&limit=100&str1=fizz&str2=buzz',
    {
      ...BINARY,
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      tags: { name: 'fizzbuzz query' },
    }));
}

export function stats() {
  const res = http.get(`${BASE}/stats`, { tags: { name: 'stats' } });
  check(res, {
    'stats status is 200': (r) => r.status === 200,
    'stats has hits': (r) => r.json('hits') > 0,
  });
}
