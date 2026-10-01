// Tests for static/js/admin.js that need no browser: the review queue's
// keyboard shortcuts. Run with: node --test tests/js/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

// Load admin.js against a bare page: no dashboard, no drafts, no toasts.
function load() {
  const noop = () => {};
  const document = {
    readyState: 'complete',
    addEventListener: noop,
    querySelector: () => null,
    querySelectorAll: () => [],
    getElementById: () => null,
  };
  const window = { sessionStorage: null, confirm: () => true };
  const ctx = { window, document, location: { search: '', hash: '' }, URLSearchParams, setTimeout, console };
  vm.createContext(ctx);
  vm.runInContext(readFileSync(new URL('../../static/js/admin.js', import.meta.url), 'utf8'), ctx);
  return window.adminShortcuts;
}

const key = (k, target = { tagName: 'BODY' }, mods = {}) => ({ key: k, target, ...mods });

test('shortcuts act on the preview and the list', () => {
  const s = load();
  assert.equal(s.action(key('a'), true, false), 'approve');
  assert.equal(s.action(key('x'), true, false), 'reject');
  assert.equal(s.action(key('r'), true, false), 'rewrite');
  assert.equal(s.action(key('j'), true, false), 'next');
  assert.equal(s.action(key('k'), false, false), 'prev');
  assert.equal(s.action(key('Enter'), false, false), 'open');
  assert.equal(s.action(key('Escape'), true, false), 'close');
  assert.equal(s.action(key('?'), false, false), 'help');
});

test('decisions need an open preview', () => {
  const s = load();
  for (const k of ['a', 'x', 'r', 'ArrowRight']) {
    assert.equal(s.action(key(k), false, false), null, k);
  }
});

test('no shortcut fires while typing in a box', () => {
  const s = load();
  for (const target of [{ tagName: 'TEXTAREA' }, { tagName: 'INPUT' }, { tagName: 'SELECT' }, { tagName: 'DIV', isContentEditable: true }]) {
    for (const k of ['a', 'x', 'r', 'j', 'k', '?', 'Enter', 'o']) {
      assert.equal(s.action(key(k, target), true, false), null, `${k} in ${target.tagName}`);
    }
    // Escape leaves the box instead of closing the preview.
    assert.equal(s.action(key('Escape', target), true, false), 'blur');
  }
});

test('modifier keys are left to the browser', () => {
  const s = load();
  assert.equal(s.action(key('a', undefined, { metaKey: true }), true, false), null);
  assert.equal(s.action(key('r', undefined, { ctrlKey: true }), true, false), null);
  assert.equal(s.action(key('x', undefined, { altKey: true }), true, false), null);
});

test('Escape closes the shortcut list before the preview', () => {
  const s = load();
  assert.equal(s.action(key('Escape'), true, true), 'hide-help');
});

test('Enter on a button or link does what the button says', () => {
  const s = load();
  assert.equal(s.action(key('Enter', { tagName: 'BUTTON' }), false, false), null);
  assert.equal(s.action(key('Enter', { tagName: 'A', classList: { contains: () => false } }), false, false), null);
  assert.equal(s.action(key('Enter', { tagName: 'A', classList: { contains: (c) => c === 'q-row' } }), false, false), 'open');
});
