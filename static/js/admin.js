// Admin pages: keep typed text across reloads, auto-refresh and session expiry.
//
// A textarea or input with data-draft="key" is saved to sessionStorage as the
// admin types and filled back in when the page loads, so a refresh, or a
// session that expired mid-review (the POST is lost and the login page sends
// the admin back here), does not lose it. The page clears a draft once the
// server confirms it was sent: a ?sent=key in the URL.
(function () {
  'use strict';
  var PREFIX = 'admin-draft:';

  function store() {
    try { return window.sessionStorage; } catch (e) { return null; }
  }

  function clearSent() {
    var s = store();
    if (!s) return;
    var sent = new URLSearchParams(location.search).get('sent');
    if (sent) s.removeItem(PREFIX + sent);
  }

  function wire(el) {
    var s = store();
    if (!s) return;
    var key = PREFIX + el.getAttribute('data-draft');
    var saved = s.getItem(key);
    if (saved && !el.value) {
      el.value = saved;
      el.classList.add('draft-restored');
      el.setAttribute('title', 'Restored from before the page reloaded');
    }
    el.addEventListener('input', function () {
      if (el.value) s.setItem(key, el.value); else s.removeItem(key);
    });
  }

  function init() {
    clearSent();
    document.querySelectorAll('[data-draft]').forEach(wire);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();

  window.adminDrafts = { wire: wire };
})();
