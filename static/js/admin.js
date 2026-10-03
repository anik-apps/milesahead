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
// previewOpen: a preview is showing; helpOpen: the shortcut list is showing;
// menuOpen: a row's actions menu is open, and has the keyboard to itself.
// Nothing fires while the admin is typing in a box, or with a modifier key
// held (so browser shortcuts keep working); Escape only leaves the box.
window.adminShortcuts = (function () {
  'use strict';
  function typing(t) {
    if (!t) return false;
    var tag = t.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || !!t.isContentEditable;
  }
  function action(e, previewOpen, helpOpen, menuOpen) {
    if (menuOpen) return null;
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
      case 'o': case 'Enter':
        // Enter on a button or link (Undo, Show all) must do what it says.
        if (e.key === 'Enter' && e.target && (e.target.tagName === 'BUTTON' ||
            (e.target.tagName === 'A' && !(e.target.classList && e.target.classList.contains('q-row'))))) return null;
        return previewOpen ? null : 'open';
      case 'a': return previewOpen ? 'approve' : null;
      case 'x': return previewOpen ? 'reject' : null;
      case 'r': return previewOpen ? 'rewrite' : null;
      case '?': return 'help';
    }
    return null;
  }
  return { action: action, typing: typing };
})();

// Rewrite feedback that asks for a different picture. A rewrite only changes
// the text, so a box with data-image-hint="id" shows the element id (a note
// pointing at Find a new image) while what is typed mentions an image. It
// never blocks sending. mentionsImage is kept free of the DOM so it can be
// tested on its own (tests/js/admin.test.mjs).
window.adminImageHint = (function () {
  'use strict';
  var WORDS = /\b(?:images?|photos?|photographs?|pictures?|pics?)\b/i;
  function mentionsImage(text) { return WORDS.test(String(text || '')); }

  function wire(box) {
    var note = document.getElementById(box.getAttribute('data-image-hint'));
    if (!note) return;
    var sync = function () { note.hidden = !mentionsImage(box.value); };
    box.addEventListener('input', sync);
    sync(); // a restored draft
  }

  // data-jump-image="id": focus the image search box id, scrolled into view.
  function jump(id) {
    var box = document.getElementById(id);
    if (!box) return;
    if (box.scrollIntoView) box.scrollIntoView({ block: 'center', behavior: 'smooth' });
    box.focus({ preventScroll: true });
  }

  function init() {
    document.querySelectorAll('[data-image-hint]').forEach(wire);
    document.addEventListener('click', function (e) {
      var t = e.target && e.target.closest && e.target.closest('[data-jump-image]');
      if (t) { e.preventDefault(); jump(t.getAttribute('data-jump-image')); }
    });
  }
  // After the drafts script above has filled the boxes back in.
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();

  return { mentionsImage: mentionsImage, jump: jump };
})();

// Row actions menus (the review queue and Published): a button with
// aria-haspopup="menu" and aria-controls naming a role="menu" list of
// role="menuitem" links and buttons. Opening focuses the first item (the
// last with ArrowUp); ArrowUp/ArrowDown, Home and End move between items;
// Escape closes and puts focus back on the button; Tab and a click outside
// close. One menu is open at a time. The functions take {btn, list} so the
// tests can drive them with stand-ins (tests/js/admin.test.mjs).
window.adminMenus = (function () {
  'use strict';
  var current = null; // the open {btn, list}, or null

  function items(m) { return Array.prototype.slice.call(m.list.querySelectorAll('[role="menuitem"]')); }
  function isOpen(m) { return !!current && current.list === m.list; }

  function open(m, where) {
    if (current && !isOpen(m)) close(current, false);
    m.list.hidden = false;
    m.btn.setAttribute('aria-expanded', 'true');
    current = m;
    var it = items(m);
    if (it.length) it[where === 'last' ? it.length - 1 : 0].focus();
  }

  function close(m, refocus) {
    m = m || current;
    if (!m) return;
    m.list.hidden = true;
    m.btn.setAttribute('aria-expanded', 'false');
    if (isOpen(m)) current = null;
    if (refocus) m.btn.focus();
  }

  function toggle(m) { if (isOpen(m)) close(m, true); else open(m); }

  // A key pressed while menu m is open, focus inside it. Returns true when
  // the menu used the key (the caller then stops the browser's default).
  function key(m, e) {
    var it = items(m);
    if (!it.length) return false;
    var i = it.indexOf(e.target);
    switch (e.key) {
      case 'Escape': close(m, true); return true;
      case 'Tab': close(m, false); return false; // focus moves on as usual
      case 'ArrowDown': it[i < 0 ? 0 : (i + 1) % it.length].focus(); return true;
      case 'ArrowUp': it[i <= 0 ? it.length - 1 : i - 1].focus(); return true;
      case 'Home': it[0].focus(); return true;
      case 'End': it[it.length - 1].focus(); return true;
      case ' ':
        // Space activates a menu item; links only do that for Enter.
        if (e.target && e.target.tagName === 'A') { e.target.click(); return true; }
        return false;
    }
    return false;
  }

  return { open: open, close: close, toggle: toggle, isOpen: isOpen, key: key, current: function () { return current; } };
})();

// Wire the menus, the panels they reveal, and the Sort selects.
(function () {
  'use strict';
  var M = window.adminMenus;

  function menuFor(btn) {
    var list = document.getElementById(btn.getAttribute('aria-controls'));
    return list ? { btn: btn, list: list } : null;
  }

  // Open upwards when there is no room below (rows near the bottom).
  function place(m) {
    if (!M.isOpen(m) || !m.list.getBoundingClientRect) return;
    m.list.classList.remove('is-up');
    var r = m.list.getBoundingClientRect();
    var b = m.btn.getBoundingClientRect();
    if (r.bottom > window.innerHeight && b.top > r.height + 8) m.list.classList.add('is-up');
  }

  document.addEventListener('click', function (e) {
    var t = e.target;
    if (!t || !t.closest) return;
    var btn = t.closest('.row-menu-btn');
    if (btn) {
      var m = menuFor(btn);
      if (m) { M.toggle(m); place(m); }
      return;
    }
    var cur = M.current();
    if (!cur) return;
    if (cur.list.contains(t)) {
      // An item: let it act first (submit, the reject confirm, opening the
      // preview), then close, returning focus to the button unless the
      // item moved it somewhere else.
      setTimeout(function () {
        var a = document.activeElement;
        M.close(cur, !a || a === document.body || cur.list.contains(a));
      }, 0);
      return;
    }
    M.close(cur, false);
  });

  document.addEventListener('keydown', function (e) {
    var cur = M.current();
    if (cur) {
      if (e.target && cur.list.contains(e.target)) {
        if (M.key(cur, e)) e.preventDefault();
      } else if (e.key === 'Escape') {
        M.close(cur, true);
        e.preventDefault();
      }
      return;
    }
    var btn = e.target && e.target.closest && e.target.closest('.row-menu-btn');
    if (btn && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      var m = menuFor(btn);
      if (m) { M.open(m, e.key === 'ArrowUp' ? 'last' : 'first'); place(m); e.preventDefault(); }
    }
  });

  // data-reveal="id" shows a hidden panel (Published: the unpublish-and-
  // rewrite form) and focuses its box; data-hide="id" hides it again and
  // returns focus to the row's menu button.
  document.addEventListener('click', function (e) {
    var t = e.target;
    if (!t || !t.closest) return;
    var show = t.closest('[data-reveal]');
    var hide = show ? null : t.closest('[data-hide]');
    if (!show && !hide) return;
    var panel = document.getElementById(show ? show.getAttribute('data-reveal') : hide.getAttribute('data-hide'));
    if (!panel) return;
    panel.hidden = !show;
    if (show) {
      var box = panel.querySelector('textarea, input:not([type="hidden"])');
      if (box) box.focus();
    } else {
      var btn = panel.parentNode && panel.parentNode.querySelector('.row-menu-btn');
      if (btn) btn.focus();
    }
  });

  function init() {
    // A panel whose box got its draft back (see adminDrafts) opens again.
    document.querySelectorAll('form[hidden] textarea[data-draft]').forEach(function (t) {
      if (t.value) t.form.hidden = false;
    });
    // Sort menus apply as soon as they change.
    document.querySelectorAll('select[data-autosubmit]').forEach(function (sel) {
      sel.addEventListener('change', function () {
        if (sel.form.requestSubmit) sel.form.requestSubmit(); else sel.form.submit();
      });
    });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
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
    if (link) {
      e.preventDefault();
      open(link.getAttribute('data-preview'));
      // "Request rewrite..." in a row's menu: straight to the box;
      // "Find a new image..." to the image search box.
      var focus = link.getAttribute('data-focus');
      if (focus === 'rewrite' && current) {
        var box = current.querySelector('.actionbar-rewrite textarea');
        if (box) box.focus();
      } else if (focus === 'new-image' && current) {
        var hint = current.querySelector('.image-form input[name="hint"]');
        if (hint) window.adminImageHint.jump(hint.id);
      }
      return;
    }
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
    // A key a row's menu already used (Escape closing it) is not a shortcut.
    if (e.defaultPrevented) return;
    var action = window.adminShortcuts.action(e, !!current, !!(help && !help.hidden), !!window.adminMenus.current());
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
  // in), offer a refresh, or refresh by itself when no preview or menu is
  // open and nothing is being typed. Drafts survive either way (data-draft
  // above).
  // The page to reload: the dashboard as the admin is viewing it (keeping
  // ?all=1 and ?sort=), without the one-off message, toast or preview of an
  // action.
  function refreshURL() {
    var keep = new URLSearchParams();
    var now = new URLSearchParams(location.search);
    ['all', 'sort'].forEach(function (k) { var v = now.get(k); if (v) keep.set(k, v); });
    var q = keep.toString();
    return location.pathname + (q ? '?' + q : '');
  }
  var refreshLink = dash.querySelector('.q-refresh-link');
  if (refreshLink) refreshLink.setAttribute('href', refreshURL());

  var version = dash.getAttribute('data-state');
  var banner = dash.querySelector('.q-refresh');
  if (version && window.fetch) {
    setInterval(function () {
      if (document.hidden) return;
      fetch('/admin/dashboard/state', { credentials: 'same-origin', headers: { 'Accept': 'application/json' } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (!s || !s.version || s.version === version) return;
          if (!current && !window.adminMenus.current() && !typing(document.activeElement)) { location.href = refreshURL(); return; }
          if (banner) banner.hidden = false;
        })
        .catch(function () {});
    }, 20000);
  }
})();

// The card editor: unsaved-change warning, JSON checks, and sections that
// collapse, reorder and can be removed with an undo until the card is saved.
(function () {
  'use strict';
  var form = document.getElementById('card-form');
  if (!form) return;

  var dirty = false;
  var saving = false;
  var note = document.querySelector('.editor-dirty');
  function markDirty() {
    if (dirty) return;
    dirty = true;
    if (note) note.hidden = false;
  }
  form.addEventListener('input', markDirty);
  form.addEventListener('change', markDirty);
  form.addEventListener('submit', function (e) {
    var resolving = e.submitter && e.submitter.hasAttribute('data-resolve');
    if (resolving && dirty && !window.confirm('Resolving reloads the editor and drops your unsaved changes. Resolve anyway?')) {
      e.preventDefault();
      return;
    }
    saving = true;
  });
  window.addEventListener('beforeunload', function (e) {
    if (dirty && !saving) { e.preventDefault(); e.returnValue = ''; }
  });

  // JSON boxes: say whether the text parses, and pretty-print on request.
  function checkJSON(t) {
    var status = t.closest('.json-field').querySelector('.json-status');
    if (!status) return;
    var v = t.value.trim();
    if (!v) { status.textContent = 'empty'; status.className = 'json-status chip chip-mute'; return; }
    try {
      JSON.parse(v);
      status.textContent = 'valid JSON';
      status.className = 'json-status chip chip-ok';
    } catch (err) {
      status.textContent = 'not valid JSON: ' + err.message;
      status.className = 'json-status chip chip-bad';
    }
  }
  form.querySelectorAll('textarea[data-json]').forEach(function (t) {
    t.addEventListener('input', function () { checkJSON(t); });
    checkJSON(t);
  });
  form.querySelectorAll('.json-format').forEach(function (b) {
    b.addEventListener('click', function () {
      var t = document.getElementById(b.getAttribute('data-for'));
      try {
        t.value = JSON.stringify(JSON.parse(t.value), null, 2);
        markDirty();
      } catch (err) { /* checkJSON already says why */ }
      checkJSON(t);
    });
  });

  // Sections.
  var container = document.getElementById('sections-container');
  function syncSummary(item) {
    var title = item.querySelector('input[name="section_title[]"]');
    var key = item.querySelector('input[name="section_key[]"]');
    var name = item.querySelector('.section-name');
    var code = item.querySelector('.section-key');
    if (name && title) name.textContent = title.value || 'Untitled section';
    if (code && key) code.textContent = key.value;
  }
  function setRemoved(item, removed) {
    item.classList.toggle('is-removed', removed);
    item.querySelectorAll('input, textarea').forEach(function (el) { el.disabled = removed; });
    item.querySelector('.section-removed').hidden = !removed;
    item.querySelector('.section-body').hidden = removed;
    if (removed) item.open = true;
    markDirty();
  }
  if (container) {
    container.addEventListener('input', function (e) {
      var item = e.target.closest('.section-item');
      if (item) syncSummary(item);
    });
    container.addEventListener('click', function (e) {
      var item = e.target.closest('.section-item');
      if (!item) return;
      var move = e.target.closest('[data-move]');
      if (move) {
        e.preventDefault();
        var dir = parseInt(move.getAttribute('data-move'), 10);
        var sib = dir < 0 ? item.previousElementSibling : item.nextElementSibling;
        if (sib) {
          if (dir < 0) container.insertBefore(item, sib); else container.insertBefore(sib, item);
          markDirty();
          move.focus();
        }
        return;
      }
      if (e.target.closest('[data-remove]')) { e.preventDefault(); setRemoved(item, true); return; }
      if (e.target.closest('[data-restore]')) { e.preventDefault(); setRemoved(item, false); }
    });
    var add = document.getElementById('add-section');
    var tpl = document.getElementById('section-template');
    if (add && tpl) {
      add.addEventListener('click', function () {
        var node = tpl.content.firstElementChild.cloneNode(true);
        container.appendChild(node);
        markDirty();
        var key = node.querySelector('input[name="section_key[]"]');
        if (key) key.focus();
      });
    }
  }
})();

// A search box with data-filter="selector" hides the matching rows whose
// data-card-search text does not contain what was typed.
(function () {
  'use strict';
  document.querySelectorAll('input[data-filter]').forEach(function (input) {
    var sel = input.getAttribute('data-filter');
    input.addEventListener('input', function () {
      var q = input.value.trim().toLowerCase();
      document.querySelectorAll(sel).forEach(function (row) {
        row.hidden = q !== '' && row.getAttribute('data-card-search').toLowerCase().indexOf(q) < 0;
      });
      document.querySelectorAll('.cards-group').forEach(function (g) {
        g.hidden = q !== '' && !g.querySelector(sel + ':not([hidden])');
      });
    });
  });
})();
