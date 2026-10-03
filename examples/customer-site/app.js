// Customer-side page code. Everything here is what a real customer would
// write; the only contract with the service is window.ab.
(function () {
  'use strict';

  // Conversion: one call, the snippet attributes it to every experiment the
  // visitor was assigned to on this page.
  window.track = function (goal, value) {
    try { window.ab.convert(goal, value != null ? { value: value } : undefined); } catch (e) { /* never break the page */ }
    toast('Conversion recorded: ' + goal);
  };

  function toast(msg) {
    var el = document.getElementById('toast');
    if (!el) return;
    el.textContent = msg;
    el.classList.add('show');
    clearTimeout(el._t);
    el._t = setTimeout(function () { el.classList.remove('show'); }, 2000);
  }

  function logout(e) {
    if (e) e.preventDefault();
    acmeSession.logout();
    location.href = 'login.html';
  }

  // Header account slot: "name · Log out". Pages without a session never
  // get this far; session.js redirects them to login.html.
  function renderAccount() {
    var slot = document.getElementById('account');
    var s = window.acmeSession && acmeSession.current();
    if (!slot || !s) return;
    slot.innerHTML = '<span style="color:#1f2430;margin-left:1.25rem">' + s.user.replace(/[<>&]/g, '') + '</span> <a href="#" id="logout" style="margin-left:.5rem">Log out</a>';
    document.getElementById('logout').onclick = logout;
  }

  // Debug panel: shows what the snippet decided. Remove on a real site.
  function debugPanel(result) {
    var pre = document.getElementById('ab-debug-out');
    if (!pre) return;
    var payloadRequests = performance.getEntriesByType('resource').filter(function (r) { return r.name.indexOf('/payload.json') >= 0; }).length;
    var s = window.acmeSession && acmeSession.current();
    pre.textContent = JSON.stringify({
      service: window.AB_SERVICE_URL,
      site: window.AB_SITE_KEY,
      user: s ? s.user : null,
      visitorId: result.visitorId,
      payloadVersion: result.payloadVersion,
      payloadSource: result.source,
      payloadRequestsThisView: payloadRequests,
      assignments: result.assignments
    }, null, 2);
  }

  window.ab.ready.then(debugPanel);

  document.addEventListener('DOMContentLoaded', function () {
    renderAccount();
    var lo = document.getElementById('ab-logout');
    if (lo) lo.onclick = logout;
    var refresh = document.getElementById('ab-refresh');
    if (refresh) refresh.onclick = function () {
      try { localStorage.removeItem('ab:payload:' + window.AB_SITE_KEY); } catch (e) { /* ignore */ }
      location.reload();
    };
  });
})();
