// Mock session for the example site. Any password is accepted; what matters
// is that every page runs with a stable user id, which it hands to the
// snippet as the visitor id. Loaded in <head> before ab.init.
(function () {
  'use strict';
  var KEY = 'acme:session';
  var onLoginPage = /login\.html$/.test(location.pathname);

  function current() {
    try { var s = JSON.parse(localStorage.getItem(KEY)); return s && s.user ? s : null; } catch (e) { return null; }
  }
  function login(user) {
    // The visitor id must be printable ASCII, 1..128 chars. Keep usernames simple.
    if (!/^[a-z0-9][a-z0-9._-]{0,62}$/i.test(user)) return false;
    localStorage.setItem(KEY, JSON.stringify({ user: user.toLowerCase(), at: Date.now() }));
    return true;
  }
  // Logout invalidates everything cached for this user: the session and the
  // snippet's payload cache for this site. (Tracking keeps no browser state.)
  function logout() {
    localStorage.removeItem(KEY);
    try { localStorage.removeItem('ab:payload:' + window.AB_SITE_KEY); } catch (e) { /* ignore */ }
  }

  window.acmeSession = { current: current, login: login, logout: logout };

  var s = current();
  if (!s && !onLoginPage) {
    // Not logged in: nothing on this site is public. Stop parsing and go to the login page.
    var page = location.pathname.replace(/^.*\//, '') || 'index.html';
    location.replace('login.html?next=' + encodeURIComponent(page));
  } else if (s && onLoginPage) {
    location.replace('index.html');
  }
  // Hashed on the account id, so the same user gets the same variant on every
  // device. Pages only call ab.init when this is set, so a redirecting page
  // fetches nothing.
  window.AB_VISITOR_ID = s ? 'user:' + s.user : undefined;
})();
