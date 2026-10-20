import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import page from '../assets/pages/vault.js';

const GB = 1e9;
const running = () => ({
  enabled: true,
  state: 'running',
  root: '/data',
  hot_bytes: 30 * GB, budget_bytes: 50 * GB, target_bytes: 45 * GB,
  cold_bytes: 120 * GB,
  files: { hot: 4, cold: 7, both: 12 },
  last_upload: '2026-10-20T10:00:00Z',
  last_sync: { at: '2026-10-20T10:00:00Z', uploaded: 3, uploaded_bytes: 100, failed: 0 },
  last_evict: { at: '2026-10-20T09:00:00Z', evicted: 2, freed_bytes: 10 },
  reachable: { at: '2026-10-20T10:05:00Z', ok: true },
  evictions: [
    { path: 'media/main/rec.m4a', size: 2000, reason: 'transcribed-media', at: '2026-10-20T09:00:00Z' },
    { path: 'media/main/old.m4a', size: 3000, reason: 'oldest', at: '2026-10-19T09:00:00Z' },
  ],
  snapshots: {
    enabled: true, repo: 'b2:fridolin-snapshots:restic',
    last: { at: '2026-10-20T10:00:00Z', kind: 'hourly', id: 'abcd1234', files: 10, bytes_added: 5 },
    last_hourly: { at: '2026-10-20T10:00:00Z', kind: 'hourly', id: 'abcd1234', files: 10, bytes_added: 5 },
    last_nightly: { at: '2026-10-20T02:00:00Z', kind: 'nightly', id: 'ef567890', files: 10, bytes_added: 50 },
    last_check: { at: '2026-10-19T03:00:00Z', read_data_subset: '5%', ok: true },
    last_prune: { at: '2026-10-19T03:10:00Z' },
    drive: { enabled: true, repo: 'rclone:brain-gdrive:fridolin-restic', completed_once: false, last_copy: { at: '2026-10-20T03:00:00Z' } },
  },
  litestream: { enabled: true, running: true, pid: 42, since: '2026-10-20T08:00:00Z', restarts: 1, dbs: ['bridge.db', 'vault.db'] },
  last_drill: { at: '2026-10-18T04:00:00Z', ok: true, snapshot: 'abcd1234', detail: 'restored 10 files', checks: [{ name: 'brain folder main', ok: true, detail: 'brain.json found' }] },
});

async function open(t, route) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, route);
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  return { root, calls };
}

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

test('a running vault shows every section', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? running() : {}));
  const titles = root.querySelectorAll('h2.section-title').map((e) => e.textContent);
  assert.deepEqual(titles, ['Hot tier', 'Snapshots', 'Litestream', 'Restore drill', 'Evictions']);
  const text = root.textContent;
  assert.ok(text.includes('Running'), 'the state pill');
  assert.ok(text.includes('/data'), 'the hot tier folder');
  assert.ok(text.includes('4 only here · 12 in both tiers · 7 only in B2'), text);
  assert.ok(text.includes('120 GB'), 'the cold size');
  assert.ok(text.includes('Reachable'), 'B2 is reachable');
  assert.ok(text.includes('abcd1234') && text.includes('ef567890'), 'the hourly and nightly snapshots');
  assert.ok(text.includes('read 5% of the data'), 'the check subset');
  assert.ok(text.includes('First copy not done yet'), 'the Drive copy status');
  assert.ok(text.includes('bridge.db') && text.includes('vault.db'), 'the Litestream databases');
  assert.ok(text.includes('brain folder main') && text.includes('brain.json found'), 'the drill checks');
  assert.ok(!text.includes('Pruning is on hold'), 'no hold');
  assert.ok(!button(root, 'Rebuild'), 'rebuild is for the command line');
  const rows = root.querySelectorAll('.vault-table tbody tr');
  assert.equal(rows.length, 2);
  assert.ok(rows[0].textContent.includes('media/main/rec.m4a') && rows[0].textContent.includes('transcribed-media'), 'newest first');
});

test('the meter shows the hot tier against the budget, with the target marked', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? running() : {}));
  const meter = root.querySelector('meter');
  assert.ok(meter, 'a <meter>, which screen readers announce');
  assert.equal(meter.value, 30 * GB);
  assert.equal(meter.getAttribute('max'), String(50 * GB));
  assert.equal(meter.getAttribute('high'), String(45 * GB));
  assert.equal(meter.getAttribute('aria-valuetext'), '30.0 GB of 50.0 GB');
  assert.ok(root.textContent.includes('30.0 GB of 50.0 GB · 60%'), root.textContent);
  assert.equal(root.querySelector('.vault-target').style.left, '90%');
  assert.ok(root.textContent.includes('45.0 GB'), 'the target in the legend');
});

test('an off vault shows only how to turn it on', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? { enabled: false, state: 'off' } : {}));
  const text = root.textContent;
  assert.ok(text.includes('The vault is off'), text);
  assert.ok(text.includes('vault.enabled'), text);
  assert.equal(root.querySelectorAll('h2.section-title').length, 0, 'no sections');
  assert.equal(root.querySelectorAll('button').length, 0, 'no actions');
});

test('a vault in error says why', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status')
    ? { ...running(), state: 'error', error: 'vault: B2 keys missing (B2_APPLICATION_KEY_ID)' }
    : {}));
  const alert = root.querySelector('.callout.bad');
  assert.ok(alert, 'a callout');
  assert.ok(alert.textContent.includes('B2 keys missing'), alert.textContent);
  assert.ok(root.textContent.includes('Error'), 'the state pill');
});

test('the actions post to their routes', async (t) => {
  const { root, calls } = await open(t, (path) => (path.startsWith('/api/vault/status') ? running() : { status: 202, body: { status: 'started' } }));
  for (const label of ['Upload now', 'Evict now', 'Snapshot now', 'Check snapshots', 'Run restore drill']) {
    button(root, label).click();
    await settle();
  }
  const posts = calls.filter((c) => c.method === 'POST');
  assert.deepEqual(posts.map((c) => [c.path, c.body]), [
    ['/api/vault/sync', undefined],
    ['/api/vault/evict', undefined],
    ['/api/vault/snapshot', { kind: 'nightly' }],
    ['/api/vault/check', {}],
    ['/api/vault/restore-drill', undefined],
  ]);
  assert.equal(posts[0].headers['X-Bridge-Dashboard'], '1');
});

test('a job that runs disables the actions and is named', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? { ...running(), job: 'snapshot' } : {}));
  assert.ok(root.textContent.includes('Snapshot running…'), root.textContent);
  for (const label of ['Upload now', 'Evict now', 'Snapshot now', 'Check snapshots', 'Run restore drill']) {
    assert.equal(button(root, label).disabled, true, label);
  }
});

test('a 409 from a job already running becomes a toast', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? running() : { status: 409, body: { error: 'a sync is running' } }));
  button(root, 'Upload now').click();
  await settle();
  assert.ok(document.body.textContent.includes('a sync is running'), 'the error is shown');
});

test('a deletion hold is lifted only after a confirmation', async (t) => {
  const held = running();
  held.snapshots.deletion_hold = { since: '2026-10-20T09:00:00Z', snapshot: 'abcd1234', prev_files: 900, files: 10 };
  const { root, calls } = await open(t, (path) => (path.startsWith('/api/vault/status') ? held : {}));
  assert.ok(root.textContent.includes('Pruning is on hold'), root.textContent);
  assert.ok(root.textContent.includes('down from 900'), root.textContent);

  button(root, 'Allow pruning…').click();
  await settle();
  let dialog = document.querySelector('dialog');
  assert.ok(dialog, 'a confirm dialog');
  button(dialog, 'Cancel').click();
  await settle();
  assert.equal(calls.filter((c) => c.method === 'POST').length, 0, 'cancel posts nothing');

  button(root, 'Allow pruning…').click();
  await settle();
  dialog = document.querySelector('dialog');
  button(dialog, 'Allow pruning').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.method === 'POST').map((c) => [c.path, c.body]), [['/api/vault/prune/allow', {}]]);
});

test('a refresh keeps what did not change', async (t) => {
  const bridge = { status: running() };
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? bridge.status : {}));
  const snapCard = root.querySelectorAll('section.section')[1].querySelector('.card');
  const rows = root.querySelectorAll('.vault-table tbody tr');

  const next = running();
  next.hot_bytes = 40 * GB;
  next.evictions.unshift({ path: 'media/main/new.m4a', size: 1000, reason: 'transcribed-media', at: '2026-10-20T10:30:00Z' });
  bridge.status = next;
  await resource('/api/vault/status').refresh();
  await settle();

  assert.equal(root.querySelector('meter').value, 40 * GB, 'the meter follows');
  assert.equal(root.querySelectorAll('section.section')[1].querySelector('.card'), snapCard, 'the snapshots are not built again');
  const now = root.querySelectorAll('.vault-table tbody tr');
  assert.equal(now.length, 3);
  assert.ok(now[0].textContent.includes('new.m4a'), 'the new eviction comes first');
  assert.equal(now[1], rows[0], 'the rows already shown stay');
  assert.equal(now[2], rows[1]);
});
