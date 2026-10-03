// Customer dashboard: vanilla JS over /v1/admin/*. The site API key is the
// only credential; it lives in sessionStorage for this tab.
(function () {
  'use strict';
  var KEY = 'ab:dashboard:key';
  var $ = function (id) { return document.getElementById(id); };
  var state = { site: null, experiments: [], selected: null, results: null, autoRefresh: null, view: 'experiments' };

  // ---- API ----
  function api(method, path, body) {
    var key = sessionStorage.getItem(KEY) || '';
    var opts = { method: method, headers: { Authorization: 'Bearer ' + key } };
    if (body !== undefined) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
    return fetch('/v1/admin' + path, opts).then(function (res) {
      if (res.status === 204) return {};
      return res.text().then(function (txt) {
        var data = {};
        try { data = txt ? JSON.parse(txt) : {}; } catch (e) { data = { error: txt }; }
        if (!res.ok) {
          var err = new Error(data.error || (res.status + ' ' + res.statusText));
          err.status = res.status;
          throw err;
        }
        return data;
      });
    });
  }

  function showBanner(msg) {
    var b = $('banner');
    if (!msg) { b.hidden = true; b.textContent = ''; return; }
    b.textContent = msg; b.hidden = false;
    clearTimeout(b._t); b._t = setTimeout(function () { b.hidden = true; }, 8000);
  }
  function fail(err) {
    if (err && err.status === 401) { signOut('Your API key was rejected. Sign in again.'); return; }
    showBanner(err && err.message ? err.message : String(err));
  }

  // ---- auth ----
  function signIn(key) {
    sessionStorage.setItem(KEY, key.trim());
    return api('GET', '/site').then(function (d) {
      state.site = d.site;
      $('login').hidden = true; $('app').hidden = false; $('login-error').textContent = '';
      renderSite();
      return loadExperiments();
    }).catch(function (err) {
      sessionStorage.removeItem(KEY);
      $('login-error').textContent = err.message;
      $('login').hidden = false; $('app').hidden = true;
    });
  }
  function signOut(msg) {
    sessionStorage.removeItem(KEY);
    stopAutoRefresh();
    state = { site: null, experiments: [], selected: null, results: null, autoRefresh: null, view: 'experiments' };
    $('app').hidden = true; $('login').hidden = false; $('login-key').value = '';
    $('login-error').textContent = msg || '';
  }

  // ---- site ----
  function renderSite() {
    var s = state.site;
    $('site-name').textContent = s.name || s.key;
    $('site-key').textContent = s.key;
    $('site-version').textContent = s.payload_version;
    $('payload-link').href = '/v1/sites/' + encodeURIComponent(s.key) + '/payload.json';
    $('settings-name').value = s.name || '';
    $('settings-origins').value = (s.allowed_origins || []).join('\n');
    $('settings-events-rps').textContent = s.events_rps_limit;
    $('settings-assign-rps').textContent = s.assign_rps_limit;
    $('settings-status').textContent = s.status;
  }
  function refreshSite() { return api('GET', '/site').then(function (d) { state.site = d.site; renderSite(); }); }

  // ---- experiments ----
  function loadExperiments() {
    return api('GET', '/experiments').then(function (d) {
      state.experiments = d.experiments || [];
      renderList();
      if (state.selected) {
        var still = state.experiments.filter(function (e) { return e.key === state.selected.key; })[0];
        if (still) { state.selected = still; renderDetail(); }
      }
    }).catch(fail);
  }
  function renderList() {
    var ul = $('experiment-list'); ul.innerHTML = '';
    if (!state.experiments.length) { ul.innerHTML = '<li class="muted">No experiments yet.</li>'; return; }
    state.experiments.forEach(function (e) {
      var li = document.createElement('li');
      li.className = state.selected && state.selected.key === e.key ? 'active' : '';
      li.innerHTML = '<span><strong>' + esc(e.key) + '</strong><br><span class="muted">' + esc(e.url_path || '/') + (e.name ? ' · ' + esc(e.name) : '') + '</span></span><span class="badge ' + e.status + '">' + e.status + '</span>';
      li.onclick = function () { select(e.key); };
      ul.appendChild(li);
    });
  }
  function select(key) {
    state.selected = state.experiments.filter(function (e) { return e.key === key; })[0] || null;
    state.results = null;
    renderList(); renderDetail();
    if (state.selected) loadResults();
  }

  function pct(bp) { return (bp / 100).toFixed(bp % 100 ? 2 : 0) + ' %'; }
  function esc(s) { return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; }); }

  function renderDetail() {
    var e = state.selected, el = $('detail');
    if (!e) { el.innerHTML = '<p class="muted">Select an experiment, or create one.</p>'; return; }
    var actions = [];
    var s = e.status;
    if (s === 'draft') actions.push(btn('Edit', 'secondary', function () { openEditor(e); }), btn('Start', '', function () { setStatus(e, 'running'); }), btn('Archive', 'danger', function () { confirmArchive(e); }));
    if (s === 'running') actions.push(btn('Pause', 'secondary', function () { setStatus(e, 'paused'); }), btn('Raise coverage', 'secondary', function () { raiseCoverage(e); }), btn('Archive', 'danger', function () { confirmArchive(e); }));
    if (s === 'paused') actions.push(btn('Resume', '', function () { setStatus(e, 'running'); }), btn('Raise coverage', 'secondary', function () { raiseCoverage(e); }), btn('Archive', 'danger', function () { confirmArchive(e); }));
    var rows = e.variants.map(function (v, i) {
      var range = e.ranges && e.ranges[i] ? e.ranges[i][0] + '–' + e.ranges[i][1] : '';
      return '<tr><td><strong>' + esc(v.key) + '</strong>' + (v.is_control ? ' <span class="badge">control</span>' : '') + (v.source === 'llm' ? ' <span class="badge">AI draft</span>' : '') + (v.approved ? '' : ' <span class="badge bad">unapproved</span>') + '</td>' +
        '<td class="num">' + pct(v.weight_bp) + '</td><td class="num"><code>' + range + '</code></td><td><pre class="content">' + esc(JSON.stringify(v.content || {}, null, 1)) + '</pre></td></tr>';
    }).join('');
    el.innerHTML =
      '<div class="row between"><div><h2 style="margin:0">' + esc(e.key) + ' <span class="badge ' + s + '">' + s + '</span>' + (e.servable ? ' <span class="badge ok">in payload</span>' : '') + '</h2>' +
      '<div class="muted">' + esc(e.name || '') + (e.description ? ' · ' + esc(e.description) : '') + '</div></div><div class="row" id="actions"></div></div>' +
      '<dl class="kv"><dt>Page</dt><dd><code>' + esc(e.url_path || '/') + '</code> · evaluated and exposed only on this path' + (s !== 'archived' ? ' <button class="btn small secondary" id="move-page">Change</button>' : '') + '</dd>' +
      '<dt>Coverage</dt><dd>' + pct(e.coverage_bp) + ' of visitors admitted' + (e.coverage_bp < 10000 ? ', the rest see defaults' : '') + '</dd>' +
      '<dt>Seed</dt><dd><code>' + esc(e.seed) + '</code> · hash v' + e.hash_version + '</dd>' +
      '<dt>Snippet</dt><dd><code>&lt;h1 data-ab="' + esc(e.key) + ':headline"&gt;</code> for content fields, or <code>ab.ready.then(r =&gt; r.assignments["' + esc(e.key) + '"])</code></dd></dl>' +
      '<table><thead><tr><th>Variant</th><th class="num">Weight</th><th class="num">Buckets</th><th>Content</th></tr></thead><tbody>' + rows + '</tbody></table>' +
      '<div class="results-head"><h3 style="margin:0">Results</h3><div class="row"><label style="margin:0" class="row">Goal <select id="goal-select"><option value="">all goals</option></select></label>' +
      '<label style="margin:0" class="row"><input type="checkbox" id="auto-refresh" style="width:auto"> auto-refresh 10 s</label><button class="btn small secondary" id="refresh-results">Refresh</button></div></div>' +
      '<div id="results"><p class="muted">Loading…</p></div>';
    var a = $('actions'); actions.forEach(function (b) { a.appendChild(b); });
    var mv = $('move-page');
    if (mv) mv.onclick = function () {
      var v = prompt('Page URL path this experiment runs on (e.g. / or /pricing.html). Changing it changes who enters the experiment, never which variant anyone gets.', e.url_path || '/');
      if (v == null) return;
      api('PATCH', '/experiments/' + encodeURIComponent(e.key), { url_path: v.trim() }).then(refreshSite).then(loadExperiments).catch(fail);
    };
    $('refresh-results').onclick = function () { loadResults(); };
    $('goal-select').onchange = function () { loadResults(); };
    $('auto-refresh').checked = !!state.autoRefresh;
    $('auto-refresh').onchange = function () { this.checked ? startAutoRefresh() : stopAutoRefresh(); };
    if (state.results) renderResults();
  }
  function btn(label, cls, fn) { var b = document.createElement('button'); b.className = 'btn small ' + cls; b.textContent = label; b.onclick = fn; return b; }

  function setStatus(e, status) {
    api('PATCH', '/experiments/' + encodeURIComponent(e.key), { status: status }).then(function () { return refreshSite(); }).then(loadExperiments).catch(fail);
  }
  function confirmArchive(e) {
    if (confirm('Archive "' + e.key + '"? It leaves the payload and becomes immutable; conversions already exposed keep being recorded.')) setStatus(e, 'archived');
  }
  function raiseCoverage(e) {
    var v = prompt('New coverage % (currently ' + pct(e.coverage_bp) + '). It can only go up while running.', String(e.coverage_bp / 100));
    if (v == null) return;
    var bp = Math.round(parseFloat(v) * 100);
    if (!(bp >= 0 && bp <= 10000)) { showBanner('Coverage must be between 0 and 100.'); return; }
    api('PATCH', '/experiments/' + encodeURIComponent(e.key), { coverage_bp: bp }).then(refreshSite).then(loadExperiments).catch(fail);
  }

  // ---- results ----
  function loadResults() {
    var e = state.selected; if (!e) return;
    var goal = $('goal-select') ? $('goal-select').value : '';
    api('GET', '/experiments/' + encodeURIComponent(e.key) + '/results' + (goal ? '?goal=' + encodeURIComponent(goal) : '')).then(function (r) {
      if (!state.selected || state.selected.key !== e.key) return;
      state.results = r; renderResults();
    }).catch(fail);
  }
  function fmtPct(x, digits) { return (x * 100).toFixed(digits == null ? 2 : digits) + ' %'; }
  function renderResults() {
    var r = state.results, el = $('results'); if (!r || !el) return;
    var sel = $('goal-select');
    if (sel && sel.options.length <= 1) { (r.goals || []).forEach(function (g) { var o = document.createElement('option'); o.value = g; o.textContent = g; sel.appendChild(o); }); sel.value = r.goal || ''; }
    var rows = r.variants.map(function (v) {
      var goals = Object.keys(v.by_goal || {}).sort().map(function (g) { return esc(g) + ': ' + v.by_goal[g] + (v.value_by_goal && v.value_by_goal[g] ? ' (value ' + v.value_by_goal[g] + ')' : ''); }).join('<br>');
      var share = r.total_exposures ? fmtPct(v.exposures / r.total_exposures, 1) : '';
      return '<tr><td><strong>' + esc(v.key) + '</strong>' + (v.is_control ? ' <span class="badge">control</span>' : '') + '</td>' +
        '<td class="num">' + v.exposures + (share ? '<br><span class="muted" style="font-size:.8rem">' + share + ' of traffic</span>' : '') + '</td>' +
        '<td class="num">' + v.conversions + (goals ? '<br><span class="muted" style="font-size:.8rem">' + goals + '</span>' : '') + '</td>' +
        '<td class="num">' + fmtPct(v.rate) + '</td></tr>';
    }).join('');
    el.innerHTML = '<div class="row"><span>' + r.total_exposures + ' exposures, ' + r.total_conversions + ' conversions' + (r.goal ? ', goal <code>' + esc(r.goal) + '</code>' : '') + '</span>' + (r.unattributed_conversions ? '<span class="badge">' + r.unattributed_conversions + ' unattributed</span>' : '') + '<span class="muted">as of ' + new Date(r.generated_at).toLocaleTimeString() + '</span></div>' +
      '<table><thead><tr><th>Variant</th><th class="num">Exposures</th><th class="num">Conversions</th><th class="num">Conversion rate</th></tr></thead><tbody>' + rows + '</tbody></table>' +
      (r.notes.length ? '<ul class="warnings">' + r.notes.map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('') + '</ul>' : '');
  }
  function startAutoRefresh() { stopAutoRefresh(); state.autoRefresh = setInterval(function () { loadResults(); refreshSite(); }, 10000); }
  function stopAutoRefresh() { if (state.autoRefresh) clearInterval(state.autoRefresh); state.autoRefresh = null; }

  // ---- editor (create, or edit a draft) ----
  var editing = null;
  function variantRow(v) {
    var tr = document.createElement('tr');
    tr.innerHTML = '<td><input class="v-key" pattern="[a-z0-9][a-z0-9\\-]{0,63}" required value="' + esc(v.key || '') + '"></td>' +
      '<td><input class="v-weight" type="number" min="0" max="100" step="0.01" required value="' + (v.weight_bp != null ? v.weight_bp / 100 : '') + '"></td>' +
      '<td><input class="v-control" type="radio" name="control" style="width:auto"' + (v.is_control ? ' checked' : '') + '></td>' +
      '<td><textarea class="v-content">' + esc(JSON.stringify(v.content || {}, null, 0)) + '</textarea></td>' +
      '<td><button type="button" class="btn small danger v-remove">×</button></td>';
    tr.querySelector('.v-remove').onclick = function () { tr.remove(); };
    return tr;
  }
  function openEditor(e) {
    editing = e || null;
    $('editor-title').textContent = e ? 'Edit draft ' + e.key : 'New experiment';
    $('ed-key').value = e ? e.key : ''; $('ed-key').disabled = !!e;
    $('ed-url-path').value = e ? e.url_path || '/' : '/';
    $('ed-name').value = e ? e.name || '' : ''; $('ed-description').value = e ? e.description || '' : '';
    $('ed-coverage').value = e ? e.coverage_bp / 100 : 100;
    var tb = $('ed-variants'); tb.innerHTML = '';
    var variants = e ? e.variants : [{ key: 'control', weight_bp: 5000, is_control: true, content: { headline: 'Default headline' } }, { key: 'b', weight_bp: 5000, content: { headline: 'Alternative headline' } }];
    variants.forEach(function (v) { tb.appendChild(variantRow(v)); });
    $('editor-error').textContent = '';
    $('editor').showModal();
  }
  function readEditor() {
    var variants = [], err = null;
    Array.prototype.forEach.call($('ed-variants').querySelectorAll('tr'), function (tr) {
      var content = {};
      var raw = tr.querySelector('.v-content').value.trim() || '{}';
      try { content = JSON.parse(raw); } catch (e) { err = 'Variant content must be a JSON object: ' + e.message; }
      if (content === null || typeof content !== 'object' || Array.isArray(content)) err = 'Variant content must be a JSON object.';
      variants.push({ key: tr.querySelector('.v-key').value.trim(), weight_bp: Math.round(parseFloat(tr.querySelector('.v-weight').value || '0') * 100), is_control: tr.querySelector('.v-control').checked, content: content });
    });
    var sum = variants.reduce(function (a, v) { return a + v.weight_bp; }, 0);
    if (!err && sum !== 10000) err = 'Variant weights total ' + (sum / 100) + ' %, must be exactly 100 %.';
    if (!err && variants.filter(function (v) { return v.is_control; }).length !== 1) err = 'Pick exactly one control variant.';
    return { err: err, body: { key: $('ed-key').value.trim(), url_path: $('ed-url-path').value.trim(), name: $('ed-name').value, description: $('ed-description').value, coverage_bp: Math.round(parseFloat($('ed-coverage').value || '0') * 100), variants: variants } };
  }
  $('ed-add-variant').onclick = function () { $('ed-variants').appendChild(variantRow({ key: '', weight_bp: 0, content: {} })); };
  $('editor-cancel').onclick = function () { $('editor').close(); };
  $('editor-form').onsubmit = function (ev) {
    ev.preventDefault();
    var r = readEditor();
    if (r.err) { $('editor-error').textContent = r.err; return; }
    var p = editing
      ? api('PATCH', '/experiments/' + encodeURIComponent(editing.key), { name: r.body.name, description: r.body.description, url_path: r.body.url_path, coverage_bp: r.body.coverage_bp, variants: r.body.variants })
      : api('POST', '/experiments', r.body);
    p.then(function (d) { $('editor').close(); return refreshSite().then(loadExperiments).then(function () { select(d.experiment.key); }); })
     .catch(function (err) { $('editor-error').textContent = err.message; });
  };
  $('new-experiment').onclick = function () { openEditor(null); };

  // ---- settings ----
  $('settings-form').onsubmit = function (ev) {
    ev.preventDefault();
    var origins = $('settings-origins').value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean);
    api('PATCH', '/site', { name: $('settings-name').value, allowed_origins: origins }).then(function (d) { state.site = d.site; renderSite(); showBanner(''); alert('Saved.'); }).catch(fail);
  };

  // ---- navigation ----
  function showView(v) {
    state.view = v;
    $('view-experiments').hidden = v !== 'experiments'; $('view-settings').hidden = v !== 'settings';
    Array.prototype.forEach.call(document.querySelectorAll('header nav a[data-view]'), function (a) { a.classList.toggle('active', a.dataset.view === v); });
    if (v === 'settings') refreshSite().catch(fail);
  }
  Array.prototype.forEach.call(document.querySelectorAll('header nav a[data-view]'), function (a) { a.onclick = function (ev) { ev.preventDefault(); showView(a.dataset.view); }; });
  $('logout').onclick = function (ev) { ev.preventDefault(); signOut(); };
  $('login-form').onsubmit = function (ev) { ev.preventDefault(); signIn($('login-key').value); };

  // ---- boot ----
  var saved = sessionStorage.getItem(KEY);
  if (saved) signIn(saved); else $('login').hidden = false;
})();
