'use strict';

const FORM_TYPE = 'application/x-www-form-urlencoded';
const FIELDS = ['int1', 'int2', 'limit', 'str1', 'str2'];
// The map draws at most MAP_CAP elements. The page still reads the rest of the response.
const MAP_CAP = 4_000_000;
// Most browsers accept a canvas side of up to 8192 pixels.
const MAX_SIDE = 8192;
const PREVIEW_BYTES = 1500;
const REASONS = { 200: 'OK', 400: 'Bad Request', 404: 'Not Found', 405: 'Method Not Allowed', 413: 'Content Too Large', 415: 'Unsupported Media Type', 503: 'Service Unavailable' };

const $ = (id) => document.getElementById(id);
const form = $('form');
const canvas = $('canvas');
const ctx = canvas.getContext('2d');
const frame = $('frame');
const colsInput = $('cols');
const nf = new Intl.NumberFormat('en');

let current = null; // The request that is in progress.
let map = null;     // The data that the canvas shows.
let lastCurl = '';

// ---- request ----

function readForm() {
  const data = new FormData(form);
  const q = new URLSearchParams();
  for (const k of FIELDS) q.set(k, data.get(k));
  return { q, method: data.get('method') };
}

function fill(values, method) {
  for (const k of FIELDS) if (values[k] !== undefined) form.elements[k].value = values[k];
  if (method) form.elements.method.value = method;
  methodHint();
}

async function send() {
  if (current) current.ctrl.abort();
  const { q, method } = readForm();
  const qs = q.toString();
  const run = { ctrl: new AbortController(), got: 0, last: 0, t0: performance.now(), tHead: 0, done: false };
  current = run;
  history.replaceState(null, '', '?' + qs + (method === 'QUERY' ? '&method=QUERY' : ''));
  clearError();
  setBusy(true);
  showExchange(method, qs, null);
  lastCurl = method === 'GET'
    ? `curl '${location.origin}/fizzbuzz?${qs}'`
    : `curl -X QUERY '${location.origin}/fizzbuzz' -H 'Content-Type: ${FORM_TYPE}' --data '${qs}'`;

  try {
    const res = await fetch(method === 'GET' ? '/fizzbuzz?' + qs : '/fizzbuzz', method === 'GET'
      ? { signal: run.ctrl.signal }
      : { method: 'QUERY', headers: { 'Content-Type': FORM_TYPE }, body: qs, signal: run.ctrl.signal });
    run.tHead = performance.now();
    showExchange(method, qs, res);
    if (!res.ok) {
      const body = await res.json().catch(() => ({ error: res.statusText || String(res.status) }));
      fail(res.status, body.error);
      return;
    }
    const p = Object.fromEntries(q);
    startMap(p);
    const tok = tokenizer(p, map);
    const preview = [];
    let previewLen = 0;
    const reader = res.body.getReader();
    requestAnimationFrame(function tick() {
      paint(run);
      if (!run.done && current === run) requestAnimationFrame(tick);
    });
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      run.got += value.length;
      run.last = value[value.length - 1];
      if (previewLen < PREVIEW_BYTES) {
        preview.push(value.subarray(0, PREVIEW_BYTES - previewLen));
        previewLen += preview.at(-1).length;
        if (previewLen >= PREVIEW_BYTES) showRaw(preview, p, true);
      }
      if (map.count < map.n) tok(value);
    }
    run.done = true;
    if (previewLen < PREVIEW_BYTES) showRaw(preview, p, false);
    paint(run);
    finish(run);
  } catch (err) {
    run.done = true;
    if (err.name === 'AbortError') {
      if (current === run) status(`Stopped after ${nf.format(run.got)} bytes.`, '');
    } else {
      status(`Request failed: ${err.message}. Check the server, then send the request again.`, 'bad');
    }
  } finally {
    if (current === run) {
      current = null;
      setBusy(false);
      refreshStats();
    }
  }
}

function setBusy(busy) {
  const b = $('run');
  b.textContent = busy ? 'Stop' : 'Send request';
  b.classList.toggle('busy', busy);
}

function fail(code, message) {
  status(`${code} ${REASONS[code] || ''}`.trim(), 'bad');
  const err = $('error');
  err.textContent = message;
  err.hidden = false;
  const field = String(message).split(':')[0];
  if (form.elements[field]) form.elements[field].setAttribute('aria-invalid', 'true');
}

function clearError() {
  $('error').hidden = true;
  for (const k of FIELDS) form.elements[k].removeAttribute('aria-invalid');
}

function status(text, kind) {
  const s = $('status');
  s.textContent = text;
  s.className = 'status' + (kind ? ' ' + kind : '');
}

// ---- tokenizer ----

// tokenizer classifies each array element when it arrives and checks it
// against the rule. It does not keep the response in memory.
function tokenizer(p, m) {
  const s1 = p.str1, s2 = p.str2, s12 = p.str1 + p.str2;
  const a = Number(p.int1), b = Number(p.int2);
  const dec = new TextDecoder();
  let buf = new Uint8Array(16384);
  let len = 0, inStr = false, esc = false, digits = true, num = 0;

  const push = (c) => {
    if (len === buf.length) { const g = new Uint8Array(len * 2); g.set(buf); buf = g; }
    buf[len++] = c;
    if (digits) {
      if (c >= 48 && c <= 57) num = num * 10 + (c - 48);
      else digits = false;
    }
  };
  const end = () => {
    const i = m.count + 1;
    let cat;
    if (digits && len > 0 && num === i) cat = 0;
    else {
      const s = JSON.parse('"' + dec.decode(buf.subarray(0, len)) + '"');
      cat = s === s12 ? 3 : s === s1 ? 1 : s === s2 ? 2 : 4;
    }
    const want = (i % a === 0 ? 1 : 0) | (i % b === 0 ? 2 : 0);
    if (cat !== want) m.wrong++;
    m.cats[m.count++] = cat;
    m.counts[cat]++;
  };

  return (chunk) => {
    for (let k = 0; k < chunk.length && m.count < m.n; k++) {
      const c = chunk[k];
      if (!inStr) {
        if (c === 34) { inStr = true; len = 0; digits = true; num = 0; }
      } else if (esc) {
        esc = false; push(c);
      } else if (c === 92) {
        esc = true; digits = false; push(c);
      } else if (c === 34) {
        inStr = false; end();
      } else {
        push(c);
      }
    }
  };
}

// ---- pattern map ----

function period(a, b, n) {
  if (a > n && b > n) return 0;
  if (a > n || b > n) return Math.min(a, b);
  const gcd = (x, y) => (y ? gcd(y, x % y) : x);
  const l = (a / gcd(a, b)) * b;
  return l <= n ? l : Math.min(a, b);
}

function startMap(p) {
  const limit = Number(p.limit);
  const n = Math.min(limit, MAP_CAP);
  map = {
    n, limit, cats: new Uint8Array(n), count: 0, drawn: 0, wrong: 0,
    counts: [0, 0, 0, 0, 0], s1: p.str1, s2: p.str2,
    period: period(Number(p.int1), Number(p.int2), n),
  };
  $('l1').textContent = p.str1;
  $('l2').textContent = p.str2;
  setCols(defaultCols());
}

function defaultCols() {
  const r = frame.clientWidth / Math.max(frame.clientHeight, 1);
  const target = Math.round(Math.sqrt(map.n * Math.min(Math.max(r, 0.6), 3)));
  const P = map.period;
  let c = P && P <= target ? P * Math.max(1, Math.round(target / P)) : target;
  return clampCols(c);
}

function clampCols(c) {
  const lo = Math.max(1, Math.ceil(map.n / MAX_SIDE));
  return Math.min(Math.max(c, lo), Math.min(map.n, MAX_SIDE));
}

function setCols(c) {
  map.cols = c;
  map.rows = Math.ceil(map.n / c);
  const lo = Math.max(1, Math.ceil(map.n / MAX_SIDE));
  colsInput.min = lo;
  colsInput.max = Math.max(c, Math.min(map.n, MAX_SIDE, Math.max(64, c * 4)));
  colsInput.value = c;
  const P = map.period;
  $('cols-out').textContent = P && c % P === 0 ? `${nf.format(c)} = ${c / P} × ${nf.format(P)}` : nf.format(c);
  $('snap').disabled = !P;
  canvas.width = map.cols;
  canvas.height = map.rows;
  map.img = ctx.createImageData(map.cols, map.rows);
  map.px = new Uint32Array(map.img.data.buffer);
  map.drawn = 0;
  fit();
  draw();
}

function fit() {
  if (!map) return;
  const cs = getComputedStyle(frame);
  const w = frame.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
  const h = frame.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
  let s = Math.min(w / map.cols, h / map.rows);
  if (s >= 1) s = Math.floor(s);
  canvas.style.width = map.cols * s + 'px';
  canvas.style.height = map.rows * s + 'px';
}

function palette() {
  const cs = getComputedStyle(document.documentElement);
  return ['--cell', '--thread-a', '--thread-b', '--thread-ab', '--danger'].map((v) => {
    const n = parseInt(cs.getPropertyValue(v).trim().slice(1), 16);
    return (0xff000000 | ((n & 0xff) << 16) | (n & 0xff00) | (n >> 16)) >>> 0;
  });
}

// draw paints the elements that arrived after the last call. Cells that are
// not drawn yet stay transparent, so the response appears over the graph paper.
function draw() {
  const m = map;
  if (!m || m.drawn >= m.count) return;
  const pal = palette();
  for (let i = m.drawn; i < m.count; i++) m.px[i] = pal[m.cats[i]];
  const y0 = Math.floor(m.drawn / m.cols), y1 = Math.floor((m.count - 1) / m.cols);
  ctx.putImageData(m.img, 0, 0, 0, y0, m.cols, y1 - y0 + 1);
  m.drawn = m.count;
  canvas.setAttribute('aria-label', `Pattern map of ${nf.format(m.count)} elements, ${nf.format(m.cols)} per row`);
}

function paint(run) {
  draw();
  // A compressing proxy removes Content-Length, so progress counts the elements on the map.
  const pct = map ? map.count / map.n : 0;
  $('fill').style.width = pct * 100 + '%';
  $('f-got').textContent = bytes(run.got);
  $('f-ttfb').textContent = run.tHead ? ms(run.tHead - run.t0) : '–';
  const t = performance.now() - run.t0;
  $('f-time').textContent = ms(t);
  const secs = (performance.now() - run.tHead) / 1000;
  $('f-rate').textContent = run.got > 1e5 && secs > 0 ? bytes(run.got / secs) + '/s' : '–';
  if (map) for (let c = 0; c < 4; c++) $('c' + c).textContent = nf.format(map.counts[c]);
  if (!run.done && map) status(pct < 1 ? `${Math.floor(pct * 100)}% of the map drawn` : 'Map complete. Reading the rest of the response.', '');
}

function finish(run) {
  const m = map;
  const closed = run.last === 93; // ']': the body was not cut off
  const complete = m.count === m.n;
  const scope = m.n < m.limit
    ? `The first ${nf.format(m.n)} of ${nf.format(m.limit)} elements`
    : `All ${nf.format(m.n)} elements`;
  const ok = closed && complete && !m.wrong && !m.counts[4];
  $('fill').classList.toggle('done', ok);
  if (!closed) {
    status(`Received ${nf.format(run.got)} bytes, but the response ends before the closing bracket.`, 'bad');
  } else if (!ok) {
    status(`Received ${nf.format(run.got)} bytes. ${nf.format(m.wrong + (m.n - m.count))} elements do not match the rule.`, 'bad');
  } else {
    status(`Received ${nf.format(run.got)} bytes. ${scope} match the rule.`, 'ok');
  }
}

function valueAt(i) {
  switch (map.cats[i]) {
    case 0: return String(i + 1);
    case 1: return map.s1;
    case 2: return map.s2;
    case 3: return map.s1 + map.s2;
    default: return 'unexpected value';
  }
}

canvas.addEventListener('pointermove', (e) => {
  if (!map) return;
  const r = canvas.getBoundingClientRect();
  const x = Math.floor(((e.clientX - r.left) / r.width) * map.cols);
  const y = Math.floor(((e.clientY - r.top) / r.height) * map.rows);
  const i = y * map.cols + x;
  const probe = $('probe');
  if (x < 0 || x >= map.cols || i < 0 || i >= map.count) { probe.hidden = true; return; }
  const f = frame.getBoundingClientRect();
  probe.textContent = `#${nf.format(i + 1)}  ${valueAt(i)}`;
  probe.style.left = e.clientX - f.left + 'px';
  probe.style.top = e.clientY - f.top + 'px';
  probe.hidden = false;
});
canvas.addEventListener('pointerleave', () => { $('probe').hidden = true; });

let colsPending = false;
colsInput.addEventListener('input', () => {
  if (!map || colsPending) return;
  colsPending = true;
  requestAnimationFrame(() => { colsPending = false; setCols(clampCols(Number(colsInput.value))); });
});

$('snap').addEventListener('click', () => {
  if (!map || !map.period) return;
  const P = map.period;
  setCols(clampCols(P * Math.max(1, Math.round(map.cols / P))));
});

new ResizeObserver(fit).observe(frame);
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { if (map) { map.drawn = 0; draw(); } });

// ---- panels ----

function showExchange(method, qs, res) {
  const wire = $('wire');
  wire.replaceChildren();
  const line = (text, cls) => {
    const el = document.createElement('span');
    if (cls) el.className = cls;
    el.textContent = text;
    wire.append(el, '\n');
  };
  const m = document.createElement('span');
  m.className = 'm';
  m.textContent = method;
  wire.append(m, method === 'GET' ? ` /fizzbuzz?${qs} HTTP/1.1\n` : ' /fizzbuzz HTTP/1.1\n');
  line(`Host: ${location.host}`, 'dim');
  if (method === 'QUERY') {
    line(`Content-Type: ${FORM_TYPE}`, 'dim');
    line(`Content-Length: ${new TextEncoder().encode(qs).length}`, 'dim');
    line('');
    line(qs);
  }
  line('');
  if (!res) { line('…', 'dim'); return; }
  line(`HTTP/1.1 ${res.status} ${REASONS[res.status] || res.statusText}`);
  for (const h of ['Content-Type', 'Content-Length', 'Content-Encoding', 'Accept-Query', 'Allow', 'Retry-After']) {
    const v = res.headers.get(h);
    if (v) line(`${h}: ${v}`, 'dim');
  }
}

function showRaw(chunks, p, truncated) {
  const raw = $('raw');
  raw.replaceChildren();
  const text = new TextDecoder().decode(concat(chunks));
  // The preview can end inside an element, so the code colors only whole tokens.
  const re = /"(?:[^"\\]|\\.)*"|[^"]+/g;
  let t;
  while ((t = re.exec(text))) {
    const tok = t[0];
    let cls = '';
    if (tok[0] === '"') {
      try {
        const s = JSON.parse(tok);
        cls = s === p.str1 + p.str2 ? 'ab' : s === p.str1 ? 'a' : s === p.str2 ? 'b' : '';
      } catch { /* A partial token stays plain. */ }
    }
    const el = document.createElement('span');
    if (cls) el.className = cls;
    el.textContent = tok;
    raw.append(el);
  }
  if (truncated) raw.append('…');
}

function concat(chunks) {
  const out = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let o = 0;
  for (const c of chunks) { out.set(c, o); o += c.length; }
  return out;
}

let topRequest = null;
async function refreshStats() {
  const out = $('stats');
  const load = $('stats-load');
  try {
    const res = await fetch('/stats');
    const body = await res.json();
    if (!res.ok) { out.textContent = body.error; load.hidden = true; return; }
    if (!body.hits) { out.textContent = 'No requests counted yet.'; load.hidden = true; return; }
    topRequest = body.params;
    out.replaceChildren();
    const b = (text) => { const el = document.createElement('b'); el.textContent = text; return el; };
    const p = body.params;
    out.append(b(`${p.int1}, ${p.int2}`), ' up to ', b(nf.format(p.limit)), ' as ',
      b(JSON.stringify(p.str1)), ' / ', b(JSON.stringify(p.str2)), ', ', b(nf.format(body.hits)),
      body.hits === 1 ? ' request' : ' requests');
    load.hidden = false;
  } catch {
    out.textContent = 'Statistics are not available. Send a request to try again.';
    load.hidden = true;
  }
}

$('stats-load').addEventListener('click', () => {
  if (!topRequest) return;
  fill(Object.fromEntries(Object.entries(topRequest).map(([k, v]) => [k, String(v)])), 'GET');
  send();
});

$('copy').addEventListener('click', async (e) => {
  if (!lastCurl) return;
  try {
    await navigator.clipboard.writeText(lastCurl);
    e.target.textContent = 'Copied';
  } catch {
    e.target.textContent = 'Copy failed';
  }
  setTimeout(() => { e.target.textContent = 'Copy as curl'; }, 1500);
});

function methodHint() {
  $('method-hint').textContent = form.elements.method.value === 'QUERY'
    ? 'The same parameters, form-encoded in the request body. Like GET, QUERY (RFC 10008) is safe and idempotent.'
    : 'Parameters in the URL query string.';
}
form.addEventListener('change', (e) => { if (e.target.name === 'method') methodHint(); });

form.addEventListener('submit', (e) => {
  e.preventDefault();
  if (current) { current.ctrl.abort(); return; }
  send();
});

for (const btn of document.querySelectorAll('.presets button')) {
  btn.addEventListener('click', () => {
    const [int1, int2, limit, str1, str2] = btn.dataset.p.split(',');
    fill({ int1, int2, limit, str1, str2 });
    send();
  });
}

function bytes(n) {
  if (n < 1e3) return Math.round(n) + ' B';
  if (n < 1e6) return (n / 1e3).toFixed(1) + ' KB';
  if (n < 1e9) return (n / 1e6).toFixed(1) + ' MB';
  return (n / 1e9).toFixed(2) + ' GB';
}

function ms(t) {
  return t < 1000 ? t.toFixed(t < 10 ? 1 : 0) + ' ms' : (t / 1000).toFixed(2) + ' s';
}

// ---- start ----

const initial = new URLSearchParams(location.search);
fill(Object.fromEntries(FIELDS.filter((k) => initial.has(k)).map((k) => [k, initial.get(k)])),
  initial.get('method') === 'QUERY' ? 'QUERY' : 'GET');
send();
