import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { runThemeScript } from './theme-script.js';
import { createShell, pageFailed } from '../assets/app/shell.js';
import { setStatus, setIgnoredCount } from '../assets/app/store.js';

// The tests give only the fields of the status that the shell reads.
/** @import { Status } from '../types/api.js' */

function shellInPage(t, theme) {
  installDom();
  const storage = runThemeScript(t, theme);
  let restarts = 0;
  let reloads = 0;
  const shell = createShell({ onRestart: () => { restarts++; }, onReload: () => { reloads++; } });
  const app = document.createElement('div');
  app.append(...shell.nodes);
  document.body.append(app);
  return { shell, app, storage, restarts: () => restarts, reloads: () => reloads };
}

test('the restart banner shows what the bridge says is waiting for a restart', (t) => {
  const { app, restarts } = shellInPage(t);
  const banner = () => app.querySelector('.banner.warn');
  setStatus(/** @type {Status} */ ({ restart_required: ['log_level', 'http_listen'] }));
  assert.equal(banner().hidden, false);
  assert.match(banner().textContent, /Restart the bridge to apply: log_level, http_listen/);
  banner().querySelector('button').click();
  assert.equal(restarts(), 1);
  setStatus(/** @type {Status} */ ({ restart_required: [] }));
  assert.equal(banner().hidden, true, 'gone once nothing waits, e.g. after the restart');
});

test('the sidebar follows the status', (t) => {
  const { app } = shellInPage(t);
  const link = (label) => app.querySelectorAll('.nav-link').find((a) => a.textContent.startsWith(label));
  setStatus(/** @type {Status} */ ({ dev_mode: true, version: '1.2.3', whatsapp: { mode: 'disabled' }, issues: [{ kind: 'drift' }] }));
  assert.equal(link('Dev').hidden, false);
  assert.equal(app.querySelector('.version').textContent, 'v1.2.3');
  assert.equal(app.querySelector('.sidebar-status').textContent, 'Dev mode (no WhatsApp)');
  assert.equal(link('Brains').querySelector('.nav-badge').textContent, '1');
  setStatus(/** @type {Status} */ ({ dev_mode: false, whatsapp: { mode: 'connected' }, issues: [] }));
  assert.equal(link('Dev').hidden, true);
  assert.equal(app.querySelector('.sidebar-status').textContent, 'WhatsApp connected');
  assert.equal(link('Brains').querySelector('.nav-badge').hidden, true);
  setIgnoredCount(2);
  assert.equal(link('Chats').querySelector('.nav-badge').textContent, '2');
  assert.equal(link('Chats').querySelector('.nav-badge').title, '2 chats waiting for access');
});

test('setActive highlights one entry', (t) => {
  const { shell, app } = shellInPage(t);
  shell.setActive('brains');
  assert.deepEqual(app.querySelectorAll('[aria-current]').map((a) => a.getAttribute('href')), ['#/brains']);
  shell.setActive('');
  assert.equal(app.querySelectorAll('[aria-current]').length, 0);
});

test('a new bridge version asks for a reload', (t) => {
  setStatus(null);
  const { app, reloads } = shellInPage(t);
  const banner = () => app.querySelector('.banner.info');
  setStatus(/** @type {Status} */ ({ version: '1.2.3' }));
  assert.equal(banner().hidden, true, 'the version the page started with');
  setStatus(/** @type {Status} */ ({ version: '1.2.3', uptime_s: 5 }));
  assert.equal(banner().hidden, true);
  setStatus(/** @type {Status} */ ({ version: '1.3.0' }));
  assert.equal(banner().hidden, false);
  assert.match(banner().textContent, /Fridolin was updated/);
  banner().querySelector('button').click();
  assert.equal(reloads(), 1);
});

test('a page that failed to load offers a reload', () => {
  installDom();
  let reloads = 0;
  const el = pageFailed(new SyntaxError("The requested module '../components/fields.js' does not provide an export named 'saveBar'"), () => { reloads++; });
  document.body.append(el);
  assert.match(el.textContent, /This page failed to load/);
  assert.match(el.textContent, /does not provide an export named 'saveBar'/);
  el.querySelector('button').click();
  assert.equal(reloads, 1);
});

test('focusPage puts the focus on the heading given, the page heading, or the page area until there is one', (t) => {
  const { shell } = shellInPage(t);
  assert.equal(shell.focusPage(), shell.page);
  assert.equal(document.activeElement, shell.page);
  assert.equal(shell.page.getAttribute('tabindex'), '-1', 'the page area takes the focus without joining the Tab order');
  const heading = document.createElement('h1');
  const part = document.createElement('h2');
  shell.page.append(heading, part);
  assert.equal(shell.focusPage(), heading);
  assert.equal(document.activeElement, heading);
  assert.equal(heading.getAttribute('tabindex'), '-1');
  assert.equal(shell.focusPage(part), part);
  assert.equal(document.activeElement, part);
  assert.equal(part.getAttribute('tabindex'), '-1');
});

test('the skip link comes first and moves the focus to the page', (t) => {
  const { shell, app } = shellInPage(t);
  const skip = /** @type {Element} */ (app.firstChild);
  assert.equal(skip.tagName, 'A');
  assert.equal(skip.textContent, 'Skip to content');
  const heading = document.createElement('h1');
  shell.page.append(heading);
  const e = new Event('click', { bubbles: true, cancelable: true });
  skip.dispatchEvent(e);
  assert.equal(e.defaultPrevented, true, 'the address stays the page’s');
  assert.equal(document.activeElement, heading);
});

test('Escape closes the phone menu and gives the focus back to its button', (t) => {
  const { app } = shellInPage(t);
  const sidebar = app.querySelector('.sidebar');
  const menu = app.querySelector('.menu-btn');
  const link = app.querySelector('.nav-link');
  assert.equal(menu.getAttribute('aria-controls'), app.querySelector('nav.nav').id);
  const escape = () => link.dispatchEvent(Object.assign(new Event('keydown', { bubbles: true }), { key: 'Escape' }));
  menu.click();
  assert.equal(sidebar.classList.contains('nav-open'), true);
  assert.equal(menu.getAttribute('aria-expanded'), 'true');
  link.focus();
  escape();
  assert.equal(sidebar.classList.contains('nav-open'), false);
  assert.equal(menu.getAttribute('aria-expanded'), 'false');
  assert.equal(document.activeElement, menu);
  link.focus();
  escape();
  assert.equal(document.activeElement, link, 'a closed menu leaves the focus alone');
});

// theme.js applies the saved theme before the page paints and keeps the
// one key and the one way to apply it; the buttons only ask it.
test('the theme buttons show and change the theme theme.js applied', async (t) => {
  const label = { auto: 'Theme: follows your system (click to change)', light: 'Theme: light (click to change)', dark: 'Theme: dark (click to change)' };
  const cases = [
    { name: 'saved dark', saved: 'dark', order: ['dark', 'auto', 'light', 'dark'] },
    { name: 'nothing saved', saved: null, order: ['auto', 'light', 'dark', 'auto'] },
    // Changed for this page only, and the buttons still say which.
    { name: 'storage blocked', blocked: true, order: ['auto', 'light', 'dark', 'auto'] },
  ];
  for (const c of cases) {
    await t.test(c.name, (t) => {
      const { app, storage } = shellInPage(t, { saved: c.saved, blocked: c.blocked });
      const buttons = app.querySelectorAll('.theme-toggle');
      assert.equal(buttons.length, 2, 'one in the brand row, one in the sidebar foot');
      c.order.forEach((want, i) => {
        if (i) buttons[i % 2].click();
        assert.equal(document.documentElement.getAttribute('data-theme'), want === 'auto' ? null : want);
        assert.deepEqual(buttons.map((b) => b.title), [label[want], label[want]], `step ${i}: both buttons follow`);
        if (!c.blocked && i) assert.equal(storage.get('bridgeTheme'), want, 'saved for the next visit');
      });
    });
  }
});
