// Stress test: find the maximum rate. Traffic ramps in stages and then holds the top
// rate, while warm keep-alive connections and connection-per-request clients
// stay open.
//
//   k6 run -e TOP=4000 -e MAX_VUS=1200 loadtest/stress.js
//   k6 run -e BASE_URL=http://localhost:8080 -e TOP=400 -e LIMIT=100 loadtest/stress.js
//
// The hold is the stage that finds the maximum: a ramp touches its top rate
// only at the last instant. The run reports where the latency and the errors
// start. It does not pass or fail on a rate.
//
// One local instance has a low maximum: it is one process on one machine, and
// k6 shares that machine with it. Against a deployed host the replica count, the
// CDN and Redis set the maximum. A long run against a public host costs traffic
// and can look like an attack to the CDN.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';
import exec from 'k6/execution';

const BASE = __ENV.BASE_URL || 'https://fizzbuzz.khalizov.com';
const LIMIT = Number(__ENV.LIMIT || 100);
const TOP = Number(__ENV.TOP || 2000);
const HOLD = __ENV.HOLD || '60s';
const PRE_VUS = Number(__ENV.PRE_VUS || 100);
const MAX_VUS = Number(__ENV.MAX_VUS || 600);

const srv5xx = new Counter('server_5xx');   // the service broke
const other4xx = new Counter('client_4xx'); // the load generator sent something wrong
const noAnswer = new Counter('no_answer');  // the client saw no HTTP response
// The rate the service actually served, not the rate the schedule asked for.
// dropped_iterations in the summary is the difference, and it is client-side.
const served = new Counter('phase_served');

const QUERY = `int1=3&int2=5&limit=${LIMIT}&str1=fizz&str2=buzz`;

// setup runs once for the whole run, so the target appears one time and not
// once for each VU.
export function setup() {
  console.log(`stress: ${BASE} up to ${TOP}/s held for ${HOLD}, limit=${LIMIT}`);
}

// The last two stages hold TOP. The VU pool has to drive the top rate: Little's
// law, a rate of TOP with a 200 ms response needs TOP/5 VUs.
const STAGES = [
  { target: 100, duration: '10s' },
  { target: 500, duration: '10s' },
  { target: 1500, duration: '20s' },
  { target: TOP, duration: '20s' },
  { target: TOP, duration: HOLD },
  { target: 0, duration: '10s' },
];
const RAMP_END = `${STAGES.reduce((n, s) => n + parseInt(s.duration, 10), 0)}s`;

// Each stage is tagged with its target rate, so the summary shows where the
// latency and the errors appear.
function phase() {
  let t = (Date.now() - exec.scenario.startTime) / 1000;
  for (const s of STAGES) {
    const secs = parseInt(s.duration, 10);
    if (t <= secs) return `rps${s.target}`;
    t -= secs;
  }
  return 'rps0';
}

// Ceilings, not targets: a request that takes ten seconds is a failure. They
// also make the summary print one line for each scenario and each rate stage,
// so a stage that breaks shows its own line.
const thresholds = {
  'http_req_duration{scenario:ramp}': ['p(95)<10000'],
  'http_req_duration{scenario:keepalive}': ['p(95)<10000'],
  'http_req_duration{scenario:newconn}': ['p(95)<10000'],
};
// The count and failure thresholds never fail. They make the summary print the
// requests served and the failure rate for each stage.
for (const s of STAGES) {
  thresholds[`http_req_duration{phase:rps${s.target}}`] = ['p(95)<10000'];
  thresholds[`phase_served{phase:rps${s.target}}`] = ['count>=0'];
  thresholds[`http_req_failed{phase:rps${s.target}}`] = ['rate<=1'];
}

export const options = {
  discardResponseBodies: true, // bodies are not checked at these rates
  scenarios: {
    ramp: {
      executor: 'ramping-arrival-rate',
      exec: 'ramp',
      startRate: 50,
      timeUnit: '1s',
      stages: STAGES,
      preAllocatedVUs: PRE_VUS,
      maxVUs: MAX_VUS,
    },
    keepalive: { executor: 'constant-vus', exec: 'warm', vus: 50, duration: RAMP_END },
    newconn: { executor: 'constant-vus', exec: 'cold', vus: 50, duration: RAMP_END },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  thresholds,
};

function count(res, phaseTag) {
  if (res.status === 0) noAnswer.add(1);
  else if (res.status >= 500) srv5xx.add(1, { status: String(res.status) });
  else if (res.status >= 400) other4xx.add(1, { status: String(res.status) });
  served.add(1, { phase: phaseTag });
}

export function ramp() {
  const p = phase();
  const res = http.get(`${BASE}/fizzbuzz?${QUERY}`, { tags: { name: 'ramp', phase: p } });
  count(res, p);
  check(res, { 'ramp: 200': (r) => r.status === 200 });
}

// Warm connections: 50 VUs hold their keep-alive connection open between
// requests, so the server keeps 50 idle connections.
export function warm() {
  const res = http.get(`${BASE}/fizzbuzz?${QUERY}`, { tags: { name: 'keepalive' } });
  count(res, 'keepalive');
  check(res, { 'keepalive: 200': (r) => r.status === 200 });
  sleep(0.5);
}

// Cold connections: one TCP connection for each request.
export function cold() {
  const res = http.get(`${BASE}/fizzbuzz?${QUERY}`, {
    headers: { Connection: 'close' },
    tags: { name: 'newconn' },
  });
  count(res, 'newconn');
  check(res, { 'newconn: 200': (r) => r.status === 200 });
  sleep(0.05);
}
