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

test('a brain\'s backup switch saves through its own route', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, (path) => (path.startsWith('/api/backup/status') ? connected() : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const sw = root.querySelector('tr input[role=switch]');
  assert.ok(sw, 'the brain row has a switch');
  sw.checked = false;
  sw.dispatchEvent(new Event('change'));
  await settle();
  const saves = calls.filter((c) => c.method === 'PUT');
  assert.deepEqual(saves.map((c) => [c.path, c.body]), [['/api/brains/work/backup', { enabled: false }]]);
});

test('a refresh keeps the brain chosen for a restore', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { status: connected() };
  serve(t, (path) => {
    if (path.startsWith('/api/backup/status')) return bridge.status;
    if (path.endsWith('/backup/versions')) return { versions: [] };
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
  // Without rclone_path, rclone is not taken from just anywhere on PATH.
  assert.ok(text.includes('the rclone in the helper folders of this computer’s PATH'), text);
});

test('a restore shows while it runs and how it went once it ended', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const running = { ...connected(), job: { kind: 'restore', brain: 'work', target: '/Users/you/restored/work', started: '2026-10-18T09:00:00Z', queue: [], done: [] } };
  /** @type {{status: object}} */
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

test('version folders named in UTC or in local time both show as dates', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  serve(t, (path) => {
    if (path.startsWith('/api/backup/status')) return connected();
    if (path.endsWith('/backup/versions')) return { versions: ['20261018-093000Z', '20261001-120000', 'manual-copy'] };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const restore = root.querySelectorAll('form').at(-1);
  const sel = restore.querySelector('select');
  sel.value = 'work';
  sel.dispatchEvent(new Event('change'));
  await settle();
  const labels = restore.querySelectorAll('label.version-opt').map((l) => l.textContent);
  const dated = labels.filter((l) => l.includes('files replaced or deleted in that run'));
  assert.equal(dated.length, 2, labels.join(' | '));
  assert.ok(!labels.some((l) => l.includes('20261018-093000Z')), 'the UTC name is shown as a date');
  assert.ok(labels.some((l) => l.includes('manual-copy')), 'other names as they are');
});

test('the Mirror the deletion dialog does not say that most of the files are gone', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  // A wiped memories/ is refused although most of the brain is still there.
  const status = connected();
  status.brains[0].last = {
    ok: false, refused: true, finished: '2026-10-18T03:30:00Z', seconds: 2,
    error: 'refusing to mirror a mass deletion: 12 of the 12 backed-up files in memories/ are gone from the brain folder, so the backup in Drive was left as it was.',
  };
  serve(t, (path) => (path.startsWith('/api/backup/status') ? status : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  root.querySelectorAll('button').find((b) => b.textContent === 'Mirror the deletion…').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.ok(dialog, 'a confirm dialog');
  assert.ok(dialog.textContent.includes('or most of one of its folders'), dialog.textContent);
  assert.ok(!dialog.textContent.includes('Most of the files'), dialog.textContent);
});

test('the Disconnect dialog says where to take away rclone\'s access at Google', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  serve(t, (path) => (path.startsWith('/api/backup/status') ? connected() : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  root.querySelectorAll('button').find((b) => b.textContent === 'Disconnect…').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.ok(dialog, 'a confirm dialog');
  assert.ok(dialog.textContent.includes('myaccount.google.com/permissions'), dialog.textContent);
});

// Renders the page against status, with window.location.assign recorded.
async function renderWith(t, status) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const assigned = [];
  Object.assign(globalThis, { window: { scrollTo() {}, location: { assign: (u) => assigned.push(u) } } });
  t.after(() => { delete globalThis.window; });
  const calls = serve(t, (path) => {
    if (path.startsWith('/api/backup/status')) return status;
    if (path === '/api/backup/connect') return { status: 202, body: { status: 'connecting', auth_url: 'https://accounts.google.com/o/oauth2/v2/auth?state=s' } };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  return { root, calls, assigned };
}

test('without a Google client, connecting asks for one first', async (t) => {
  const { root } = await renderWith(t, { ...disconnected(), client: null, redirect_uri: 'http://127.0.0.1:8765/' });
  const connect = root.querySelectorAll('button').find((b) => b.textContent === 'Connect Google Drive');
  assert.ok(connect && connect.disabled, 'Connect waits for a client');
  assert.ok(root.textContent.includes('enable the Google Drive API'), root.textContent);
  assert.ok(root.querySelector('textarea'), 'a box to paste a client for Drive');
});

test('Connect Google Drive sends the browser to Google with your own client', async (t) => {
  const { root, calls, assigned } = await renderWith(t, { ...disconnected(), client: { source: 'google', type: 'installed', client_id_hint: '1234-…' } });
  assert.ok(root.textContent.includes('your client under Settings → Google sign-in'), root.textContent);
  root.querySelectorAll('button').find((b) => b.textContent === 'Connect Google Drive').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.method === 'POST').map((c) => [c.path, c.body]), [['/api/backup/connect', undefined]]);
  assert.deepEqual(assigned, ['https://accounts.google.com/o/oauth2/v2/auth?state=s']);
});

test('a link through rclone\'s shared client offers the switch, which keeps it until the new one works', async (t) => {
  const status = { ...connected(), shared_client: true, client: { source: 'google', type: 'web', client_id_hint: '1234-…' }, redirect_uri: 'http://127.0.0.1:8765/' };
  const { root, calls, assigned } = await renderWith(t, status);
  assert.ok(root.textContent.includes('rclone’s shared Google client'), root.textContent);
  assert.ok(root.textContent.includes('http://127.0.0.1:8765/'), 'a Web client names the redirect to add');
  root.querySelectorAll('button').find((b) => b.textContent === 'Switch to your own Google client').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.method === 'POST').map((c) => [c.path, c.body]), [['/api/backup/connect', { reconnect: true }]]);
  assert.equal(assigned.length, 1);
});

test('a link with your own client shows its folder and no switch', async (t) => {
  const { root } = await renderWith(t, { ...connected(), shared_client: false, drive_folder: 'Fridolin', client: { source: 'google', type: 'installed', client_id_hint: '1234-…' } });
  assert.ok(root.textContent.includes('Fridolin/fridolin-backup'), root.textContent);
  assert.ok(!root.textContent.includes('shared Google client'));
});
