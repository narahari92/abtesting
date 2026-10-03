/*! ab.js: variant evaluator snippet. Vanilla JS, no dependencies, never throws. */
(function (root, factory) {
  var ab = factory(root);
  if (typeof module === 'object' && module.exports) {
    module.exports = ab;
  } else {
    root.ab = ab;
    ab._autoInit();
  }
})(typeof self !== 'undefined' ? self : this, function (root) {
  'use strict';

  var BUCKETS = 10000;
  var HASH_VERSION = 1;
  var STALE_MS = 60 * 1000;            // older: use as-is, refresh in background for the next view
  var MAX_AGE_MS = 4 * 60 * 60 * 1000; // older: fetch synchronously, fall back to the stale copy
  var TIMEOUT_MS = 300;
  var COOKIE = '_abv';
  // Replaced by the server when it serves /v1/ab.js. Left as-is (same origin) in tests.
  var BASE_URL = '__AB_BASE_URL__';

  // ---- pure evaluator (mirrors internal/assign in Go) ----

  function fnv1a32(str) {
    var h = 0x811c9dc5;
    for (var i = 0; i < str.length; i++) {
      h = Math.imul(h ^ str.charCodeAt(i), 16777619);
    }
    return h >>> 0;
  }

  function validVisitor(v) {
    if (typeof v !== 'string' || v.length < 1 || v.length > 128) return false;
    for (var i = 0; i < v.length; i++) {
      var c = v.charCodeAt(i);
      if (c < 0x20 || c > 0x7e) return false;
    }
    return true;
  }

  // Integer in [0, BUCKETS), or -1 when the visitor must be held back.
  function bucket(seed, visitor, hashVersion) {
    if (hashVersion !== HASH_VERSION || typeof seed !== 'string' || !validVisitor(visitor)) return -1;
    return fnv1a32(String(fnv1a32(seed + visitor))) % BUCKETS;
  }

  function choose(b, ranges) {
    if (b < 0 || !Array.isArray(ranges)) return -1;
    for (var i = 0; i < ranges.length; i++) {
      if (b >= ranges[i][0] && b < ranges[i][1]) return i;
    }
    return -1;
  }

  // ?ab_force=exp:variant,exp2:variant2  (QA override)
  function forcedVariants(search) {
    var out = {};
    var m = /[?&]ab_force=([^&#]*)/.exec(search || '');
    if (!m) return out;
    var pairs = decodeURIComponent(m[1]).split(',');
    for (var i = 0; i < pairs.length; i++) {
      var idx = pairs[i].indexOf(':');
      if (idx > 0) out[pairs[i].slice(0, idx)] = pairs[i].slice(idx + 1);
    }
    return out;
  }

  // Evaluates every experiment in the payload for one visitor.
  // Returns { "<experiment>": { variant: "<key>", content: {...} } }.
  function evaluate(payload, visitor, forced) {
    var out = {};
    var exps = (payload && Array.isArray(payload.experiments)) ? payload.experiments : [];
    for (var i = 0; i < exps.length; i++) {
      var e = exps[i];
      if (!e || typeof e.key !== 'string' || !Array.isArray(e.variants)) continue;
      var idx = -1;
      if (forced && forced[e.key]) {
        for (var j = 0; j < e.variants.length; j++) {
          if (e.variants[j] && e.variants[j].key === forced[e.key]) idx = j;
        }
      }
      if (idx < 0) idx = choose(bucket(e.seed, visitor, e.hash_version), e.ranges);
      if (idx < 0 || idx >= e.variants.length || !e.variants[idx]) continue;
      var content = e.variants[idx].content;
      out[e.key] = { variant: e.variants[idx].key, content: (content && typeof content === 'object') ? content : {} };
    }
    return out;
  }

  // ---- payload cache: one payload per page view ----

  function readCache(storage, site) {
    try {
      var raw = storage.getItem('ab:payload:' + site);
      if (!raw) return null;
      var c = JSON.parse(raw);
      return (c && typeof c.t === 'number' && c.p && typeof c.p === 'object') ? c : null;
    } catch (e) { return null; }
  }

  function writeCache(storage, site, payload, now) {
    try { storage.setItem('ab:payload:' + site, JSON.stringify({ t: now, p: payload })); } catch (e) { /* quota, private mode */ }
  }

  function fetchPayload(env, url, timeoutMs) {
    return new Promise(function (resolve, reject) {
      var done = false;
      var ctrl = typeof env.AbortController === 'function' ? new env.AbortController() : null;
      var timer = env.setTimeout(function () {
        if (done) return;
        done = true;
        if (ctrl) { try { ctrl.abort(); } catch (e) { /* ignore */ } }
        reject(new Error('timeout'));
      }, timeoutMs);
      var p;
      try {
        p = env.fetch(url, { signal: ctrl ? ctrl.signal : undefined, credentials: 'omit', mode: 'cors' });
      } catch (e) { p = Promise.reject(e); }
      Promise.resolve(p).then(function (res) {
        if (!res || !res.ok) throw new Error('status ' + (res && res.status));
        return res.json();
      }).then(function (json) {
        if (done) return;
        done = true;
        env.clearTimeout(timer);
        resolve(json);
      }, function (err) {
        if (done) return;
        done = true;
        env.clearTimeout(timer);
        reject(err);
      });
    });
  }

  // Resolves { payload, source } where source is cache | network | stale | none.
  function loadPayload(env, site, url, opts) {
    var now = env.now();
    var cached = readCache(env.storage, site);
    var age = cached ? now - cached.t : Infinity;
    if (cached && age >= 0 && age < opts.maxAgeMs) {
      if (age > opts.staleMs) {
        // Background refresh for the next page view. Never touches this view.
        fetchPayload(env, url, opts.timeoutMs).then(function (p) {
          writeCache(env.storage, site, p, env.now());
        }, function () { /* keep the stale copy */ });
      }
      return Promise.resolve({ payload: cached.p, source: 'cache' });
    }
    return fetchPayload(env, url, opts.timeoutMs).then(function (p) {
      writeCache(env.storage, site, p, env.now());
      return { payload: p, source: 'network' };
    }, function () {
      if (cached) return { payload: cached.p, source: 'stale' };
      return { payload: null, source: 'none' };
    });
  }

  // ---- tracking beacons: fire-and-forget, never awaited, never thrown ----

  // Sends a JSON body as text/plain so the request is a CORS simple request
  // (no preflight). sendBeacon survives page unload; fetch keepalive is the
  // fallback for browsers without it.
  function beacon(env, url, body) {
    var data = JSON.stringify(body);
    try {
      if (typeof env.sendBeacon === 'function' && env.sendBeacon(url, data)) return true;
    } catch (e) { /* fall through */ }
    try {
      var p = env.fetch(url, { method: 'POST', body: data, keepalive: true, mode: 'cors', credentials: 'omit' });
      if (p && typeof p.then === 'function') p.then(null, function () {});
      return true;
    } catch (e) { return false; }
  }

  // Exposure dedupe: one beacon per (visitor, experiment, variant) per
  // browser, remembered in localStorage. The server dedupes again by
  // primary key, so losing this set only costs a harmless repeat.
  function exposedSet(storage, site) {
    try {
      var raw = storage.getItem('ab:exposed:' + site);
      var arr = raw ? JSON.parse(raw) : [];
      return Array.isArray(arr) ? arr : [];
    } catch (e) { return []; }
  }

  function recordExposures(env, state, assignments) {
    var seen = exposedSet(env.storage, state.site);
    var changed = false;
    for (var key in assignments) {
      if (!Object.prototype.hasOwnProperty.call(assignments, key)) continue;
      var tag = state.visitorId + '|' + key + '|' + assignments[key].variant;
      if (seen.indexOf(tag) >= 0) continue;
      if (beacon(env, state.base + '/v1/events/exposure', { site: state.site, v: state.visitorId, experiment: key, variant: assignments[key].variant })) {
        seen.push(tag);
        changed = true;
      }
    }
    if (changed) {
      if (seen.length > 500) seen = seen.slice(seen.length - 500);
      try { env.storage.setItem('ab:exposed:' + state.site, JSON.stringify(seen)); } catch (e) { /* ignore */ }
    }
  }

  // One conversion beacon per experiment the visitor was assigned to. The
  // variant is not sent: the server joins conversions to exposures.
  function recordConversion(env, state, assignments, goal, opts) {
    if (typeof goal !== 'string' || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(goal)) return 0;
    var value = opts && typeof opts.value === 'number' && isFinite(opts.value) ? opts.value : undefined;
    var sent = 0;
    for (var key in assignments) {
      if (!Object.prototype.hasOwnProperty.call(assignments, key)) continue;
      var body = { site: state.site, v: state.visitorId, experiment: key, goal: goal };
      if (value !== undefined) body.value = value;
      if (beacon(env, state.base + '/v1/events/conversion', body)) sent++;
    }
    return sent;
  }

  // ---- visitor identity: first-party cookie on the customer's domain ----

  function readCookie(doc, name) {
    var m = new RegExp('(?:^|; )' + name + '=([^;]*)').exec(doc.cookie || '');
    try { return m ? decodeURIComponent(m[1]) : null; } catch (e) { return null; }
  }

  function uuid(env) {
    var c = env.crypto;
    if (c && typeof c.randomUUID === 'function') return c.randomUUID();
    var b = new Array(16);
    if (c && typeof c.getRandomValues === 'function') {
      var arr = new Uint8Array(16);
      c.getRandomValues(arr);
      for (var i = 0; i < 16; i++) b[i] = arr[i];
    } else {
      for (var j = 0; j < 16; j++) b[j] = Math.floor(Math.random() * 256);
    }
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    var hex = '';
    for (var k = 0; k < 16; k++) {
      hex += (b[k] < 16 ? '0' : '') + b[k].toString(16);
      if (k === 3 || k === 5 || k === 7 || k === 9) hex += '-';
    }
    return hex;
  }

  function getVisitor(env, cfg) {
    if (cfg.visitorId && validVisitor(cfg.visitorId)) return cfg.visitorId;
    var v = readCookie(env.document, COOKIE);
    if (!v || !validVisitor(v)) v = uuid(env);
    try {
      env.document.cookie = COOKIE + '=' + encodeURIComponent(v) + '; Max-Age=31536000; Path=/; SameSite=Lax';
    } catch (e) { /* cookies disabled: the id lives for this page only */ }
    return v;
  }

  // ---- DOM: content-driven integration ----

  // <h1 data-ab="hero-cta:headline">Default</h1> gets content.headline if it is a string.
  function apply(doc, assignments) {
    var els = doc.querySelectorAll('[data-ab]');
    for (var i = 0; i < els.length; i++) {
      var spec = els[i].getAttribute('data-ab') || '';
      var idx = spec.indexOf(':');
      if (idx <= 0) continue;
      var a = assignments[spec.slice(0, idx)];
      if (!a) continue;
      var val = a.content[spec.slice(idx + 1)];
      if (typeof val === 'string') els[i].textContent = val;
    }
  }

  // Anti-flicker contract: pages hide [data-ab] until <html data-ab-ready> appears.
  function reveal(doc) {
    try { doc.documentElement.setAttribute('data-ab-ready', ''); } catch (e) { /* ignore */ }
  }

  function whenDOMReady(env, fn) {
    var d = env.document;
    if (d.readyState === 'loading' && typeof d.addEventListener === 'function') {
      d.addEventListener('DOMContentLoaded', fn, { once: true });
    } else {
      fn();
    }
  }

  // Payload host precedence: explicit cfg.baseUrl, then the host the server
  // baked into this file, then the origin this script was loaded from (URL
  // install), then same-origin (inline install on the service's own pages).
  function defaultBase(env) {
    if (BASE_URL && BASE_URL.indexOf('__') !== 0) return BASE_URL;
    return env.scriptOrigin || '';
  }

  // ---- orchestration ----

  function run(cfg, env) {
    var result = { assignments: {}, visitorId: null, payloadVersion: null, source: 'none' };
    var state = { site: null, visitorId: null, base: '' };
    var finished = false;

    // convert is attached to the result so the public ab.convert can reach
    // this run's site, visitor and assignments.
    result.convert = function (goal, opts) {
      try { return recordConversion(env, state, result.assignments, goal, opts); } catch (e) { return 0; }
    };

    function finish() {
      if (!finished) {
        finished = true;
        whenDOMReady(env, function () {
          try { apply(env.document, result.assignments); } catch (e) { /* ignore */ }
          reveal(env.document);
          // Exposure fires only now, when the decision is actually in use:
          // content applied, and ready about to resolve for key-driven code.
          try { if (state.site) recordExposures(env, state, result.assignments); } catch (e) { /* ignore */ }
        });
      }
      return result;
    }

    try {
      if (!cfg || typeof cfg.site !== 'string' || !cfg.site) return Promise.resolve(finish());
      var opts = {
        staleMs: cfg.staleMs > 0 ? cfg.staleMs : STALE_MS,
        maxAgeMs: cfg.maxAgeMs > 0 ? cfg.maxAgeMs : MAX_AGE_MS,
        timeoutMs: cfg.timeoutMs > 0 ? cfg.timeoutMs : TIMEOUT_MS
      };
      // Safety net: the page is revealed no later than the timeout even if
      // something below hangs. Assignments that arrive after that are dropped
      // so that ready, the DOM and (later) exposures agree.
      env.setTimeout(function () { finish(); }, opts.timeoutMs + 50);

      var visitor = getVisitor(env, cfg);
      result.visitorId = visitor;
      var base = (cfg.baseUrl != null ? String(cfg.baseUrl) : defaultBase(env)).replace(/\/+$/, '');
      state.site = cfg.site;
      state.visitorId = visitor;
      state.base = base;
      var url = base + '/v1/sites/' + encodeURIComponent(cfg.site) + '/payload.json';
      var forced = forcedVariants(env.location ? env.location.search : '');

      return loadPayload(env, cfg.site, url, opts).then(function (r) {
        result.source = r.source;
        if (r.payload && !finished) {
          result.payloadVersion = r.payload.version;
          result.assignments = evaluate(r.payload, visitor, forced);
        }
        try { env.storage.setItem('ab:assignments:' + cfg.site, JSON.stringify(result.assignments)); } catch (e) { /* ignore */ }
        return finish();
      }, function () { return finish(); });
    } catch (e) {
      return Promise.resolve(finish());
    }
  }

  function browserEnv(scriptOrigin) {
    var noop = { getItem: function () { return null; }, setItem: function () {}, removeItem: function () {} };
    var storage = noop;
    try { if (root.localStorage) storage = root.localStorage; } catch (e) { /* access denied */ }
    return {
      storage: storage,
      document: root.document,
      location: root.location,
      crypto: root.crypto,
      AbortController: root.AbortController,
      fetch: function (u, o) { return root.fetch(u, o); },
      sendBeacon: (root.navigator && typeof root.navigator.sendBeacon === 'function')
        ? function (u, b) { return root.navigator.sendBeacon(u, b); } : undefined,
      setTimeout: function (f, ms) { return root.setTimeout(f, ms); },
      clearTimeout: function (t) { return root.clearTimeout(t); },
      now: function () { return Date.now(); },
      scriptOrigin: scriptOrigin
    };
  }

  var readyResolve;
  var ready = new Promise(function (resolve) { readyResolve = resolve; });
  var scriptOrigin = '';
  try {
    var cs = root.document && root.document.currentScript;
    if (cs && cs.src) scriptOrigin = new URL(cs.src).origin;
  } catch (e) { /* ignore */ }

  function init(cfg) {
    var p = run(cfg || {}, browserEnv(scriptOrigin));
    p.then(readyResolve);
    return p;
  }

  // ab.convert(goal, {value}) records a goal for every experiment this
  // visitor was assigned to on this page. Safe to call before init
  // finishes: it waits for ready. Never throws, never returns a rejection.
  function convert(goal, opts) {
    try { ready.then(function (r) { if (r && typeof r.convert === 'function') r.convert(goal, opts); }); } catch (e) { /* ignore */ }
  }

  var ab = {
    init: init,
    convert: convert,
    ready: ready,
    _autoInit: function () {
      try {
        var cs = root.document && root.document.currentScript;
        var site = cs && cs.getAttribute('data-site');
        if (site) init({ site: site, baseUrl: cs.getAttribute('data-base') || undefined });
      } catch (e) { /* ignore */ }
    },
    _internal: { fnv1a32: fnv1a32, bucket: bucket, choose: choose, evaluate: evaluate, run: run, forcedVariants: forcedVariants, validVisitor: validVisitor, loadPayload: loadPayload, beacon: beacon }
  };
  return ab;
});
