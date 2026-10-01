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

// Forms with data-confirm ask first (reject, which cannot be undone).
document.addEventListener('submit', function (e) {
  var msg = e.target.getAttribute && e.target.getAttribute('data-confirm');
  if (msg && !window.confirm(msg)) e.preventDefault();
}, true);

// Toasts dismiss themselves after data-autohide milliseconds, or on x.
(function () {
  'use strict';
  document.querySelectorAll('.toast').forEach(function (t) {
    var close = function () { t.classList.add('toast-hidden'); };
    var btn = t.querySelector('.toast-close');
    if (btn) btn.addEventListener('click', close);
    var ms = parseInt(t.getAttribute('data-autohide'), 10);
    if (ms > 0) setTimeout(close, ms);
  });
})();

// Which shortcut a key press means on the review queue, or null. Kept free
// of the DOM so it can be tested on its own (tests/js/admin.test.mjs).
// previewOpen: a preview is showing; helpOpen: the shortcut list is showing.
// Nothing fires while the admin is typing in a box, or with a modifier key
// held (so browser shortcuts keep working); Escape only leaves the box.
window.adminShortcuts = (function () {
  'use strict';
  function typing(t) {
    if (!t) return false;
    var tag = t.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || !!t.isContentEditable;
  }
  function action(e, previewOpen, helpOpen) {
    if (e.key === 'Escape') {
      if (typing(e.target)) return 'blur';
      if (helpOpen) return 'hide-help';
      return previewOpen ? 'close' : null;
    }
    if (typing(e.target) || e.metaKey || e.ctrlKey || e.altKey) return null;
    switch (e.key) {
      case 'j': return 'next';
      case 'k': return 'prev';
      case 'ArrowRight': return previewOpen ? 'next' : null;
      case 'ArrowLeft': return previewOpen ? 'prev' : null;
      case 'o': case 'Enter': return previewOpen ? null : 'open';
      case 'a': return previewOpen ? 'approve' : null;
      case 'x': return previewOpen ? 'reject' : null;
      case 'r': return previewOpen ? 'rewrite' : null;
      case '?': return 'help';
    }
    return null;
  }
  return { action: action, typing: typing };
})();

// The review queue (dashboard) and its previews.
(function () {
  'use strict';
  var dash = document.querySelector('.admin-dashboard');
  if (!dash) return;

  var current = null; // the open .preview-overlay

  function rows() { return Array.prototype.slice.call(dash.querySelectorAll('.q-row')); }

  function open(id, focusRow) {
    var el = document.getElementById(id);
    if (!el || !el.classList.contains('preview-overlay')) return;
    if (current && current !== el) current.classList.remove('active');
    current = el;
    el.classList.add('active');
    document.body.style.overflow = 'hidden';
    var modal = el.querySelector('.preview-modal');
    if (modal) { modal.scrollTop = 0; modal.setAttribute('tabindex', '-1'); modal.focus({ preventScroll: true }); }
    if (location.hash !== '#' + id) history.replaceState(null, '', '#' + id);
    if (focusRow !== false) {
      var row = dash.querySelector('.q-row[data-preview="' + id + '"]');
      rows().forEach(function (r) { r.classList.toggle('is-selected', r === row); });
    }
  }

  function close() {
    if (!current) return;
    var id = current.id;
    current.classList.remove('active');
    current = null;
    document.body.style.overflow = '';
    var params = new URLSearchParams(location.search);
    params.delete('open');
    var q = params.toString();
    history.replaceState(null, '', location.pathname + (q ? '?' + q : ''));
    var row = dash.querySelector('.q-row[data-preview="' + id + '"]');
    if (row) row.focus();
  }

  dash.addEventListener('click', function (e) {
    var link = e.target.closest('[data-preview]');
    if (link) { e.preventDefault(); open(link.getAttribute('data-preview')); return; }
    if (e.target.closest('.preview-close')) { close(); return; }
    if (e.target.classList.contains('preview-overlay')) close();
  });

  var typing = window.adminShortcuts.typing;

  var help = document.getElementById('kbd-help');
  var helpBtn = dash.querySelector('.kbd-help-btn');
  function toggleHelp(show) {
    if (!help) return;
    var on = show === undefined ? help.hidden : show;
    help.hidden = !on;
    if (helpBtn) helpBtn.setAttribute('aria-expanded', on ? 'true' : 'false');
  }
  if (helpBtn) helpBtn.addEventListener('click', function () { toggleHelp(); });

  function step(dir) {
    if (current) {
      var target = current.getAttribute(dir > 0 ? 'data-next' : 'data-prev');
      if (target) open(target);
      return;
    }
    var list = rows();
    if (!list.length) return;
    var i = list.indexOf(document.activeElement);
    if (i < 0) i = list.findIndex(function (r) { return r.classList.contains('is-selected'); });
    var next = list[Math.max(0, Math.min(list.length - 1, i < 0 ? 0 : i + dir))];
    list.forEach(function (r) { r.classList.toggle('is-selected', r === next); });
    next.focus();
  }

  function submit(selector) {
    if (!current) return;
    // Clicking the button (not form.submit()) runs the submit handlers, so
    // reject still asks first.
    var btn = current.querySelector(selector + ' button[type="submit"]');
    if (btn) btn.click();
  }

  document.addEventListener('keydown', function (e) {
    var action = window.adminShortcuts.action(e, !!current, !!(help && !help.hidden));
    if (!action) return;
    switch (action) {
      case 'blur': e.target.blur(); return;
      case 'hide-help': toggleHelp(false); return;
      case 'close': close(); return;
      case 'next': step(1); break;
      case 'prev': step(-1); break;
      case 'open':
        var sel = document.activeElement && document.activeElement.classList.contains('q-row') ? document.activeElement : dash.querySelector('.q-row.is-selected');
        if (!sel) return;
        open(sel.getAttribute('data-preview'));
        break;
      case 'approve': submit('form[action$="/approve"]'); break;
      case 'reject': submit('form[action$="/reject"]'); break;
      case 'rewrite':
        var box = current.querySelector('.actionbar-rewrite textarea');
        if (box) box.focus();
        break;
      case 'help': toggleHelp(); break;
    }
    e.preventDefault();
  });

  // The rewrite box grows with its text.
  dash.querySelectorAll('.actionbar-rewrite textarea').forEach(function (t) {
    var fit = function () { t.style.height = 'auto'; t.style.height = Math.min(t.scrollHeight, 160) + 'px'; };
    t.addEventListener('input', fit);
    fit();
  });

  // Open the preview named by ?open= (after approve-and-next) or the hash.
  var start = dash.getAttribute('data-open') || (location.hash.indexOf('#preview-') === 0 ? location.hash.slice(1) : '');
  if (start) open(start);
  window.addEventListener('hashchange', function () {
    if (location.hash.indexOf('#preview-') === 0) open(location.hash.slice(1));
    else if (!location.hash) close();
  });

  // Poll the queue; when it changed (a rewrite landed, a new article came
  // in), offer a refresh, or refresh by itself when nothing is open and
  // nothing is being typed. Drafts survive either way (data-draft above).
  var version = dash.getAttribute('data-state');
  var banner = dash.querySelector('.q-refresh');
  if (version && window.fetch) {
    setInterval(function () {
      if (document.hidden) return;
      fetch('/admin/dashboard/state', { credentials: 'same-origin', headers: { 'Accept': 'application/json' } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (!s || !s.version || s.version === version) return;
          if (!current && !typing(document.activeElement)) { location.href = '/admin/dashboard'; return; }
          if (banner) banner.hidden = false;
        })
        .catch(function () {});
    }, 20000);
  }
})();
