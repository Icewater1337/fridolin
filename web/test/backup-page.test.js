import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import page from '../assets/pages/backup.js';

const connected = (root = 'fridolin-backup') => ({
  rclone: { found: true, version: 'rclone v1.71' },
  connected: true,
  settings: { enabled: true, daily_at: '03:30', root, keep_versions_days: 30 },
  include_bridge_config: true,
  brains: [{ id: 'work', name: 'Work', dir: '/Users/you/work', enabled: true }],
});
const disconnected = () => ({ rclone: { found: true }, connected: false, connect: null });

test('a schedule edit goes with the connection, and the schedule comes back with it', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { status: connected() };
  serve(t, (path) => (path.startsWith('/api/backup/status') ? bridge.status : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const poll = async (status) => { bridge.status = status; await resource('/api/backup/status').refresh(); await settle(); };
  const folderIn = () => root.querySelectorAll('form input').find((i) => i.getAttribute('placeholder') === 'fridolin-backup');

  type(folderIn(), 'elsewhere');
  assert.equal(ctx.dirty(), true);
  const input = folderIn();
  await poll(connected());
  assert.equal(folderIn(), input, 'a refresh keeps the form');
  assert.equal(input.value, 'elsewhere');

  await poll(disconnected());
  assert.equal(folderIn(), undefined, 'no schedule while Drive is not connected');
  assert.equal(ctx.dirty(), false, 'so nothing on screen is unsaved');

  await poll(connected());
  assert.ok(folderIn(), 'the schedule is back after reconnecting');
  assert.equal(folderIn().value, 'fridolin-backup');
  assert.equal(ctx.dirty(), false);
});

test('a refresh keeps the brain chosen for a restore', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { status: connected() };
  serve(t, (path) => {
    if (path.startsWith('/api/backup/status')) return bridge.status;
    if (path.startsWith('/api/backup/versions')) return { versions: [] };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const restore = () => root.querySelectorAll('form').at(-1);
  const sel = restore().querySelector('select');
  sel.value = 'work';
  const target = restore().querySelectorAll('input').find((i) => i.getAttribute('placeholder') === '~/restored/brain-name');
  type(target, '~/restored/mine');

  const more = connected();
  more.brains.push({ id: 'home', name: 'Home', dir: '/Users/you/home', enabled: false });
  bridge.status = more;
  await resource('/api/backup/status').refresh();
  await settle();
  assert.equal(restore().querySelector('select'), sel);
  assert.equal(sel.value, 'work');
  assert.ok(sel.querySelectorAll('option').some((o) => o.value === 'home'), 'the new brain is offered');
  assert.equal(target.value, '~/restored/mine');
});

test('a running backup says how far it is in a progress bar', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const status = { ...connected(), job: { started: '2026-10-18T10:00:00Z', done: ['work', 'bridge'], current: 'home', queue: ['notes'] } };
  serve(t, (path) => (path.startsWith('/api/backup/status') ? status : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const bar = root.querySelector('.job-card progress');
  assert.ok(bar, 'a <progress>, which screen readers announce');
  assert.equal(bar.value, 50);
  assert.equal(bar.getAttribute('max'), '100');
  assert.equal(bar.getAttribute('aria-label'), 'Backup progress');
  assert.equal(bar.getAttribute('aria-valuetext'), '2 of 4 done');
});

test('a backup.rclone_path that does not work is named, not offered for install', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const error = 'backup.rclone_path /Users/you/bin/rclone: exec: "/Users/you/bin/rclone": stat /Users/you/bin/rclone: no such file or directory';
  serve(t, (path) => (path.startsWith('/api/backup/status')
    ? { rclone: { found: false, install_hint: 'brew install rclone', error }, connected: false }
    : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const text = root.textContent;
  assert.ok(text.includes('The configured rclone does not work'), text);
  assert.ok(text.includes(error), 'the error is shown');
  assert.ok(!text.includes('brew install rclone'), 'no install hint for a path that is set');
});

test('a restore shows while it runs and how it went once it ended', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const running = { ...connected(), job: { kind: 'restore', brain: 'work', target: '/Users/you/restored/work', started: '2026-10-18T09:00:00Z', queue: [], done: [] } };
  const bridge = { status: running };
  serve(t, (path) => (path.startsWith('/api/backup/status') ? bridge.status : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  assert.ok(root.textContent.includes('Restoring Work'), root.textContent);
  assert.ok(root.textContent.includes('/Users/you/restored/work'), 'the target is named');

  bridge.status = {
    ...connected(),
    last_restore: { brain: 'work', version: 'current', target: '/Users/you/restored/work', ok: false, error: 'directory not found', finished: '2026-10-18T09:01:00Z', files: 0, bytes: 0 },
  };
  await resource('/api/backup/status').refresh();
  await settle();
  const text = root.textContent;
  assert.ok(text.includes('Last restore failed'), text);
  assert.ok(text.includes('directory not found'), 'the error is shown');
  assert.ok(!text.includes('Restoring Work'), 'the progress card is gone');
});
