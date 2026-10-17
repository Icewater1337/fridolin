import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { createShell, pageFailed } from '../assets/app/shell.js';
import { setStatus, setIgnoredCount } from '../assets/app/store.js';

function shellInPage() {
  installDom();
  let restarts = 0;
  let reloads = 0;
  const shell = createShell({ onRestart: () => { restarts++; }, onReload: () => { reloads++; } });
  const app = document.createElement('div');
  app.append(...shell.nodes);
  document.body.append(app);
  return { shell, app, restarts: () => restarts, reloads: () => reloads };
}

test('the restart banner shows what the bridge says is waiting for a restart', () => {
  const { app, restarts } = shellInPage();
  const banner = () => app.querySelector('.banner.warn');
  setStatus({ restart_required: ['log_level', 'http_listen'] });
  assert.equal(banner().hidden, false);
  assert.match(banner().textContent, /Restart the bridge to apply: log_level, http_listen/);
  banner().querySelector('button').click();
  assert.equal(restarts(), 1);
  setStatus({ restart_required: [] });
  assert.equal(banner().hidden, true, 'gone once nothing waits, e.g. after the restart');
});

test('the sidebar follows the status', () => {
  const { app } = shellInPage();
  const link = (label) => app.querySelectorAll('.nav-link').find((a) => a.textContent.startsWith(label));
  setStatus({ dev_mode: true, version: '1.2.3', whatsapp: { mode: 'disabled' }, issues: [{ kind: 'drift' }] });
  assert.equal(link('Dev').hidden, false);
  assert.equal(app.querySelector('.version').textContent, 'v1.2.3');
  assert.equal(app.querySelector('.sidebar-status').textContent, 'Dev mode (no WhatsApp)');
  assert.equal(link('Brains').querySelector('.nav-badge').textContent, '1');
  setStatus({ dev_mode: false, whatsapp: { mode: 'connected' }, issues: [] });
  assert.equal(link('Dev').hidden, true);
  assert.equal(app.querySelector('.sidebar-status').textContent, 'WhatsApp connected');
  assert.equal(link('Brains').querySelector('.nav-badge').hidden, true);
  setIgnoredCount(2);
  assert.equal(link('Chats').querySelector('.nav-badge').textContent, '2');
  assert.equal(link('Chats').querySelector('.nav-badge').title, '2 chats waiting for access');
});

test('setActive highlights one entry', () => {
  const { shell, app } = shellInPage();
  shell.setActive('brains');
  assert.deepEqual(app.querySelectorAll('[aria-current]').map((a) => a.getAttribute('href')), ['#/brains']);
  shell.setActive('');
  assert.equal(app.querySelectorAll('[aria-current]').length, 0);
});

test('a new bridge version asks for a reload', () => {
  setStatus(null);
  const { app, reloads } = shellInPage();
  const banner = () => app.querySelector('.banner.info');
  setStatus({ version: '1.2.3' });
  assert.equal(banner().hidden, true, 'the version the page started with');
  setStatus({ version: '1.2.3', uptime_s: 5 });
  assert.equal(banner().hidden, true);
  setStatus({ version: '1.3.0' });
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
