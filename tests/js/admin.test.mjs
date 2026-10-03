// Tests for static/js/admin.js that need no browser: the review queue's
// keyboard shortcuts. Run with: node --test tests/js/
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

// Load admin.js against a bare page: no dashboard, no drafts, no toasts.
function load() {
  return loadWindow().adminShortcuts;
}

function loadWindow() {
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
  return window;
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

test('no shortcut fires while a row menu is open', () => {
  const s = load();
  for (const k of ['a', 'x', 'r', 'j', 'k', 'o', 'Enter', '?', 'Escape', 'ArrowRight']) {
    assert.equal(s.action(key(k), false, false, true), null, `${k} with the list's menu open`);
    assert.equal(s.action(key(k), true, false, true), null, `${k} with a preview and a menu open`);
  }
  // Closed again, the list's keys work.
  assert.equal(s.action(key('j'), false, false, false), 'next');
});

// A menu button and its list, made of stand-ins that record focus.
function fakeMenu(n, tag = 'BUTTON') {
  const state = { active: null };
  const attrs = { 'aria-expanded': 'false' };
  const btn = {
    name: 'button',
    focus() { state.active = btn; },
    setAttribute(k, v) { attrs[k] = v; },
    getAttribute(k) { return attrs[k]; },
  };
  const items = Array.from({ length: n }, (_, i) => {
    const it = { name: 'item' + i, tagName: tag, clicks: 0 };
    it.focus = () => { state.active = it; };
    it.click = () => { it.clicks++; };
    return it;
  });
  const list = { hidden: true, querySelectorAll: () => items };
  return { m: { btn, list }, items, state, attrs };
}

test('a row menu opens on the first item and closes back to its button', () => {
  const M = loadWindow().adminMenus;
  const { m, items, state, attrs } = fakeMenu(3);
  assert.equal(M.current(), null);

  M.toggle(m);
  assert.equal(m.list.hidden, false);
  assert.equal(attrs['aria-expanded'], 'true');
  assert.equal(state.active, items[0]);
  assert.equal(M.isOpen(m), true);
  assert.equal(M.current(), m);

  M.toggle(m);
  assert.equal(m.list.hidden, true);
  assert.equal(attrs['aria-expanded'], 'false');
  assert.equal(state.active, m.btn, 'focus goes back to the button');
  assert.equal(M.current(), null);

  M.open(m, 'last');
  assert.equal(state.active, items[2], 'ArrowUp on the button opens on the last item');
});

test('arrow keys, Home and End move through the menu and wrap', () => {
  const M = loadWindow().adminMenus;
  const { m, items, state } = fakeMenu(3);
  M.open(m);
  const press = (k) => M.key(m, { key: k, target: state.active });
  assert.equal(press('ArrowDown'), true);
  assert.equal(state.active, items[1]);
  press('ArrowDown'); press('ArrowDown');
  assert.equal(state.active, items[0], 'wraps to the top');
  press('ArrowUp');
  assert.equal(state.active, items[2], 'wraps to the bottom');
  press('Home');
  assert.equal(state.active, items[0]);
  press('End');
  assert.equal(state.active, items[2]);
  // Other keys are left alone (and are not shortcuts while it is open).
  assert.equal(press('j'), false);
  assert.equal(M.isOpen(m), true);
});

test('Escape closes the menu and returns focus; Tab closes and lets focus move', () => {
  const M = loadWindow().adminMenus;
  const { m, state } = fakeMenu(2);
  M.open(m);
  assert.equal(M.key(m, { key: 'Escape', target: state.active }), true);
  assert.equal(m.list.hidden, true);
  assert.equal(state.active, m.btn);
  assert.equal(M.current(), null);

  M.open(m);
  const before = state.active;
  assert.equal(M.key(m, { key: 'Tab', target: state.active }), false, 'Tab keeps its default');
  assert.equal(m.list.hidden, true);
  assert.equal(state.active, before, 'focus is not pulled back to the button');
});

test('opening one menu closes another', () => {
  const M = loadWindow().adminMenus;
  const a = fakeMenu(2);
  const b = fakeMenu(2);
  M.open(a.m);
  M.open(b.m);
  assert.equal(a.m.list.hidden, true);
  assert.equal(a.attrs['aria-expanded'], 'false');
  assert.equal(M.current(), b.m);
});

test('Space activates a link in the menu', () => {
  const M = loadWindow().adminMenus;
  const { m, items, state } = fakeMenu(2, 'A');
  M.open(m);
  assert.equal(M.key(m, { key: ' ', target: state.active }), true);
  assert.equal(items[0].clicks, 1);
});

test('rewrite feedback that asks for another picture is spotted', () => {
  const { mentionsImage } = loadWindow().adminImageHint;
  for (const text of [
    'Use a different image',
    'The PHOTO is wrong',
    'pictures of the lounge, please',
    'swap the pic',
    'Pics look dated',
    'needs a better photograph.',
    'image: show the hotel',
  ]) {
    assert.equal(mentionsImage(text), true, text);
  }
  for (const text of [
    '',
    null,
    'Tighten the second paragraph',
    'imagine the reader is new to points', // not the word "image"
    'an epic deal',                         // "pic" inside a word
    'photography tips belong elsewhere',     // a different word
    'Mention the topic earlier',
  ]) {
    assert.equal(mentionsImage(text), false, String(text));
  }
});
