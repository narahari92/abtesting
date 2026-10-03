// Run with: node --test web/*.test.js
// Parity with the Go reference via the shared golden fixture, plus the
// localStorage rules of the snippet, using small fakes for browser APIs.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const ab = require('./ab.js');
const { fnv1a32, bucket, choose, evaluate, run, forcedVariants, validVisitor } = ab._internal;

const golden = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'internal', 'assign', 'testdata', 'golden.json'), 'utf8'));

test('fnv1a32 matches the fixture vectors', () => {
  assert.equal(fnv1a32(''), 0x811c9dc5);
  for (const c of golden.fnv1a32) assert.equal(fnv1a32(c.input), c.hash, `fnv1a32(${JSON.stringify(c.input)})`);
});

test('every golden case matches the Go reference', () => {
  assert.ok(golden.cases.length >= 200);
  for (const c of golden.cases) {
    const b = bucket(c.seed, c.visitor_id, c.hash_version);
    assert.equal(b, c.bucket, `bucket for ${c.seed}/${c.visitor_id}/v${c.hash_version}`);
    assert.equal(choose(b, c.ranges), c.variant, `variant for ${c.seed}/${c.visitor_id}`);
  }
});

test('visitor id validation mirrors Go', () => {
  for (const v of ['a', 'visitor-8841', ' ', '~', 'x'.repeat(128)]) assert.ok(validVisitor(v), v);
  for (const v of ['', 'x'.repeat(129), 'tab\there', 'café', '\x7f', 42, null]) assert.ok(!validVisitor(v), String(v));
});

const payload = {
  site: 'demo', version: 7,
  experiments: [
    { key: 'hero-cta', seed: '3f9a1c7e5b2d4a6f8c0e1d3b5a7f9c2e', hash_version: 1, ranges: [[0, 5000], [5000, 10000]],
      variants: [{ key: 'control', content: { headline: 'Default', n: 1 } }, { key: 'b', content: { headline: 'Variant B', n: 2 } }] },
    { key: 'future', seed: 'abc', hash_version: 2, ranges: [[0, 10000]], variants: [{ key: 'control', content: {} }] },
    { key: 'held', seed: 'abc', hash_version: 1, ranges: [[0, 0]], variants: [{ key: 'control', content: {} }] },
  ],
};

test('evaluate: hashes, holds back unknown versions and coverage gaps, honours ?ab_force', () => {
  const a = evaluate(payload, 'visitor-8841', {});
  assert.deepEqual(Object.keys(a), ['hero-cta']);
  const idx = choose(bucket('3f9a1c7e5b2d4a6f8c0e1d3b5a7f9c2e', 'visitor-8841', 1), payload.experiments[0].ranges);
  assert.equal(a['hero-cta'].variant, payload.experiments[0].variants[idx].key);

  const forced = evaluate(payload, 'visitor-8841', forcedVariants('?x=1&ab_force=hero-cta%3Acontrol,held:control'));
  assert.equal(forced['hero-cta'].variant, 'control');
  assert.equal(forced['held'].variant, 'control', 'force admits a held-back visitor for QA');
  assert.equal(evaluate(payload, 'visitor-8841', forcedVariants('?ab_force=hero-cta:nope'))['hero-cta'].variant, a['hero-cta'].variant, 'unknown forced variant falls back to the hash');
  assert.deepEqual(evaluate(null, 'v', {}), {});
  assert.deepEqual(evaluate({ experiments: [{ key: 'x' }] }, 'v', {}), {});
  assert.deepEqual(evaluate(payload, '', {}), {});
});

// ---- fakes ----

function fakeEnv({ cached, cachedAgeMs, fetchImpl, readyState = 'complete', search = '' } = {}) {
  const store = new Map();
  let now = 1_000_000_000;
  const site = 'demo';
  if (cached) store.set('ab:payload:' + site, JSON.stringify({ t: now - cachedAgeMs, p: cached }));
  const elements = [
    { attrs: { 'data-ab': 'hero-cta:headline' }, textContent: 'Default headline', getAttribute(k) { return this.attrs[k]; } },
    { attrs: { 'data-ab': 'hero-cta:n' }, textContent: 'number stays', getAttribute(k) { return this.attrs[k]; } },
    { attrs: { 'data-ab': 'missing:headline' }, textContent: 'untouched', getAttribute(k) { return this.attrs[k]; } },
  ];
  const listeners = [];
  const doc = {
    cookie: '',
    readyState,
    documentElement: { attrs: {}, setAttribute(k, v) { this.attrs[k] = v; } },
    querySelectorAll: () => elements,
    addEventListener: (_n, fn) => listeners.push(fn),
  };
  const fetches = [];
  const beacons = [];
  const timers = [];
  const env = {
    storage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) },
    document: doc,
    location: { search },
    crypto: undefined,
    AbortController: undefined,
    fetch: (url, opts) => { if (opts && opts.method === 'POST') { beacons.push({ url, body: JSON.parse(opts.body), via: 'fetch' }); return Promise.resolve({ ok: true, status: 202 }); } fetches.push(url); return fetchImpl ? fetchImpl(url, opts) : Promise.reject(new Error('no network')); },
    sendBeacon: (url, body) => { beacons.push({ url, body: JSON.parse(body), via: 'sendBeacon' }); return true; },
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    clearTimeout: () => {},
    now: () => now,
    scriptOrigin: 'https://cdn.example',
  };
  return { env, store, doc, elements, fetches, beacons, timers, listeners, fireDOMContentLoaded: () => listeners.forEach((fn) => fn()), fireTimers: () => timers.splice(0).forEach((t) => t.fn()) };
}
const ok = (body) => () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
const tick = () => new Promise((r) => setImmediate(r));

test('fresh cache: one payload per view, no network', async () => {
  const f = fakeEnv({ cached: payload, cachedAgeMs: 10_000, fetchImpl: ok(payload) });
  const r = await run({ site: 'demo' }, f.env);
  await tick();
  assert.equal(r.source, 'cache');
  assert.equal(f.fetches.length, 0);
  assert.equal(r.payloadVersion, 7);
  assert.ok(r.assignments['hero-cta']);
  assert.ok(validVisitor(r.visitorId));
  assert.match(f.doc.cookie, /^_abv=/);
  assert.equal(f.doc.documentElement.attrs['data-ab-ready'], '');
  assert.equal(f.elements[0].textContent, r.assignments['hero-cta'].content.headline);
  assert.equal(f.elements[1].textContent, 'number stays', 'non-string content is not applied');
  assert.equal(f.elements[2].textContent, 'untouched');
  assert.equal(JSON.parse(f.store.get('ab:assignments:demo'))['hero-cta'].variant, r.assignments['hero-cta'].variant);
});

test('stale cache (60 s < age < 4 h): used as-is, refreshed in background for the next view', async () => {
  const newer = { ...payload, version: 8, experiments: [] };
  const f = fakeEnv({ cached: payload, cachedAgeMs: 5 * 60_000, fetchImpl: ok(newer) });
  const r = await run({ site: 'demo' }, f.env);
  await tick(); await tick();
  assert.equal(r.source, 'cache');
  assert.equal(r.payloadVersion, 7, 'this view evaluated the old payload');
  assert.ok(r.assignments['hero-cta'], 'DOM and result come from the old payload');
  assert.equal(f.fetches.length, 1, 'exactly one background fetch');
  assert.equal(JSON.parse(f.store.get('ab:payload:demo')).p.version, 8, 'next view gets the new payload');
  assert.equal(f.elements[0].textContent, r.assignments['hero-cta'].content.headline, 'background refresh did not touch the DOM');
});

test('cache older than 4 h: fetch synchronously, use the fresh payload', async () => {
  const newer = { ...payload, version: 9 };
  const f = fakeEnv({ cached: payload, cachedAgeMs: 5 * 3_600_000, fetchImpl: ok(newer) });
  const r = await run({ site: 'demo' }, f.env);
  assert.equal(r.source, 'network');
  assert.equal(r.payloadVersion, 9);
  assert.equal(f.fetches.length, 1);
  assert.equal(JSON.parse(f.store.get('ab:payload:demo')).t, f.env.now());
});

test('cache older than 4 h and fetch fails: stale copy beats a broken page', async () => {
  const f = fakeEnv({ cached: payload, cachedAgeMs: 5 * 3_600_000 });
  const r = await run({ site: 'demo' }, f.env);
  assert.equal(r.source, 'stale');
  assert.ok(r.assignments['hero-cta']);
  assert.equal(f.doc.documentElement.attrs['data-ab-ready'], '');
});

test('first visit and fetch fails: defaults revealed, no assignments, no throw', async () => {
  const f = fakeEnv({});
  const r = await run({ site: 'demo' }, f.env);
  assert.equal(r.source, 'none');
  assert.deepEqual(r.assignments, {});
  assert.equal(f.elements[0].textContent, 'Default headline');
  assert.equal(f.doc.documentElement.attrs['data-ab-ready'], '');
});

test('first visit and fetch hangs: timeout reveals defaults', async () => {
  const f = fakeEnv({ fetchImpl: () => new Promise(() => {}) });
  const p = run({ site: 'demo', timeoutMs: 300 }, f.env);
  f.fireTimers(); // the fetch timeout and the safety net
  const r = await p;
  assert.deepEqual(r.assignments, {});
  assert.equal(f.doc.documentElement.attrs['data-ab-ready'], '');
});

test('first visit: fetches from the configured base and stores the payload', async () => {
  const f = fakeEnv({ fetchImpl: ok(payload) });
  const r = await run({ site: 'demo', baseUrl: 'https://cdn.example/' }, f.env);
  assert.equal(f.fetches[0], 'https://cdn.example/v1/sites/demo/payload.json');
  assert.equal(r.source, 'network');
  assert.ok(f.store.has('ab:payload:demo'));
});

// Loads ab.js the way the server serves it, with the base URL placeholder
// substituted, and returns the module.
function loadServed(baseUrl) {
  const src = fs.readFileSync(path.join(__dirname, 'ab.js'), 'utf8').replace('__AB_BASE_URL__', baseUrl);
  const mod = { exports: {} };
  new Function('module', 'exports', src)(mod, mod.exports);
  return mod.exports;
}

test('payload host precedence: cfg.baseUrl, baked-in host, script origin, same origin', async () => {
  const cases = [
    { baked: '', cfg: {}, scriptOrigin: 'https://cdn.example', want: 'https://cdn.example/v1/sites/demo/payload.json' },
    { baked: '', cfg: {}, scriptOrigin: '', want: '/v1/sites/demo/payload.json' },
    { baked: 'https://baked.example/', cfg: {}, scriptOrigin: 'https://cdn.example', want: 'https://baked.example/v1/sites/demo/payload.json' },
    { baked: 'https://baked.example', cfg: { baseUrl: 'https://cfg.example' }, scriptOrigin: 'https://cdn.example', want: 'https://cfg.example/v1/sites/demo/payload.json' },
  ];
  for (const c of cases) {
    const served = loadServed(c.baked);
    const f = fakeEnv({ fetchImpl: ok(payload) });
    f.env.scriptOrigin = c.scriptOrigin;
    await served._internal.run({ site: 'demo', ...c.cfg }, f.env);
    assert.equal(f.fetches[0], c.want, JSON.stringify(c));
  }
});

test('unknown hash_version in payload: held back, nothing applied', async () => {
  const only = { site: 'demo', version: 1, experiments: [{ ...payload.experiments[0], hash_version: 3 }] };
  const f = fakeEnv({ fetchImpl: ok(only) });
  const r = await run({ site: 'demo' }, f.env);
  assert.deepEqual(r.assignments, {});
  assert.equal(f.elements[0].textContent, 'Default headline');
});

test('DOM still loading: apply and reveal wait for DOMContentLoaded', async () => {
  const f = fakeEnv({ cached: payload, cachedAgeMs: 1000, readyState: 'loading' });
  const r = await run({ site: 'demo' }, f.env);
  assert.ok(r.assignments['hero-cta']);
  assert.equal(f.doc.documentElement.attrs['data-ab-ready'], undefined, 'not revealed before DOM is parsed');
  f.fireDOMContentLoaded();
  assert.equal(f.doc.documentElement.attrs['data-ab-ready'], '');
  assert.equal(f.elements[0].textContent, r.assignments['hero-cta'].content.headline);
});

test('cookie is reused, visitorId option wins, and bad config never throws', async () => {
  const f = fakeEnv({ cached: payload, cachedAgeMs: 1000 });
  f.doc.cookie = 'other=1; _abv=visitor-8841';
  const r = await run({ site: 'demo' }, f.env);
  assert.equal(r.visitorId, 'visitor-8841');
  const r2 = await run({ site: 'demo', visitorId: 'user-42' }, fakeEnv({ cached: payload, cachedAgeMs: 1000 }).env);
  assert.equal(r2.visitorId, 'user-42');
  for (const cfg of [undefined, {}, { site: 7 }]) {
    const g = fakeEnv({});
    const r3 = await run(cfg, g.env);
    assert.deepEqual(r3.assignments, {});
    assert.equal(g.doc.documentElement.attrs['data-ab-ready'], '');
  }
});

test('storage that throws is tolerated', async () => {
  const f = fakeEnv({ fetchImpl: ok(payload) });
  f.env.storage = { getItem() { throw new Error('denied'); }, setItem() { throw new Error('denied'); } };
  const r = await run({ site: 'demo' }, f.env);
  assert.equal(r.source, 'network');
  assert.ok(r.assignments['hero-cta']);
});

test('exposure: one beacon per (visitor, experiment, variant), after apply, deduped across views', async () => {
  const f = fakeEnv({ cached: payload, cachedAgeMs: 1000 });
  const r = await run({ site: 'demo', visitorId: 'user:alice' }, f.env);
  await tick();
  assert.equal(f.beacons.length, 1, 'one assigned experiment, one beacon');
  assert.equal(f.beacons[0].via, 'sendBeacon');
  assert.equal(f.beacons[0].url, 'https://cdn.example/v1/events/exposure');
  assert.deepEqual(f.beacons[0].body, { site: 'demo', v: 'user:alice', experiment: 'hero-cta', variant: r.assignments['hero-cta'].variant });
  assert.equal(f.elements[0].textContent, r.assignments['hero-cta'].content.headline, 'exposure fires after content is applied');
  // Same browser, next page view: nothing new to report.
  await run({ site: 'demo', visitorId: 'user:alice' }, f.env);
  await tick();
  assert.equal(f.beacons.length, 1);
  // Another visitor in the same browser (login switch) is a new exposure.
  await run({ site: 'demo', visitorId: 'user:bob' }, f.env);
  await tick();
  assert.equal(f.beacons.length, 2);
  assert.equal(f.beacons[1].body.v, 'user:bob');
});

test('exposure: nothing for held-back, failed load, or timeout-dropped assignments', async () => {
  const f = fakeEnv({});                                    // first visit, fetch fails
  await run({ site: 'demo' }, f.env);
  await tick();
  assert.equal(f.beacons.length, 0);
  const held = { site: 'demo', version: 1, experiments: [{ ...payload.experiments[0], ranges: [[0, 0], [0, 0]] }] };
  const g = fakeEnv({ fetchImpl: ok(held) });
  await run({ site: 'demo' }, g.env);
  await tick();
  assert.equal(g.beacons.length, 0, 'held back visitors are not exposed');
  const h = fakeEnv({ fetchImpl: () => new Promise(() => {}) });
  const p = run({ site: 'demo', timeoutMs: 300 }, h.env);
  h.fireTimers();
  await p; await tick();
  assert.equal(h.beacons.length, 0);
});

test('exposure: DOM still loading waits for DOMContentLoaded; sendBeacon missing falls back to fetch keepalive', async () => {
  const f = fakeEnv({ cached: payload, cachedAgeMs: 1000, readyState: 'loading' });
  f.env.sendBeacon = undefined;
  await run({ site: 'demo', visitorId: 'user:carol' }, f.env);
  await tick();
  assert.equal(f.beacons.length, 0, 'not before the DOM is ready');
  f.fireDOMContentLoaded();
  await tick();
  assert.equal(f.beacons.length, 1);
  assert.equal(f.beacons[0].via, 'fetch');
});

test('convert: one beacon per assigned experiment, value optional, variant never sent, bad goals ignored', async () => {
  const two = { ...payload, experiments: [payload.experiments[0], { ...payload.experiments[0], key: 'second', seed: 'other' }] };
  const f = fakeEnv({ fetchImpl: ok(two) });
  const r = await run({ site: 'demo', visitorId: 'user:alice' }, f.env);
  await tick();
  const exposures = f.beacons.length;
  assert.equal(exposures, 2);
  assert.equal(r.convert('signup'), 2);
  assert.equal(r.convert('checkout', { value: 49 }), 2);
  const conv = f.beacons.slice(exposures);
  assert.equal(conv.length, 4);
  assert.ok(conv.every((b) => b.url === 'https://cdn.example/v1/events/conversion'));
  assert.deepEqual(conv[0].body, { site: 'demo', v: 'user:alice', experiment: 'hero-cta', goal: 'signup' });
  assert.deepEqual(conv[3].body, { site: 'demo', v: 'user:alice', experiment: 'second', goal: 'checkout', value: 49 });
  assert.ok(conv.every((b) => !('variant' in b.body)));
  assert.equal(r.convert('Sign Up!'), 0);
  assert.equal(r.convert(''), 0);
  assert.equal(r.convert('x', { value: 'ten' }), 2, 'non-numeric value is dropped, beacon still sent');
  assert.ok(!('value' in f.beacons[f.beacons.length - 1].body));
});

test('convert: nothing when the visitor has no assignments', async () => {
  const f = fakeEnv({});
  const r = await run({ site: 'demo' }, f.env);
  assert.equal(r.convert('signup'), 0);
  assert.equal(f.beacons.length, 0);
});

test('public surface', () => {
  assert.equal(typeof ab.init, 'function');
  assert.equal(typeof ab.convert, 'function');
  assert.ok(ab.ready instanceof Promise);
});
