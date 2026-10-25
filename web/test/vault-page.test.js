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
  assert.deepEqual(titles, ['Hot tier', 'Snapshots', 'Secret folders', 'Litestream', 'B2 transactions', 'Restore drill', 'Evictions', 'Backup targets']);
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

test('the secret folders show their numbers, never a file, and back up on request', async (t) => {
  const withSecrets = () => ({
    ...running(),
    secret_folders: {
      enabled: true, where: 'vault bucket, secret-folders-sealed/', keep_days: 30,
      folders: [{ path: '/home/you/.fridolin-secrets', source: 'sandbox.credentials_dir', label: '.fridolin-secrets', files: 3, bytes: 2048 }],
      last: { at: '2026-10-20T03:00:00Z', version: '20261020T030000Z.tar', reason: 'daily', files: 3, bytes: 2048 },
      last_ok: { at: '2026-10-20T03:00:00Z', version: '20261020T030000Z.tar', reason: 'daily', files: 3, bytes: 2048 },
      versions: [{ version: '20261020T030000Z.tar', at: '2026-10-20T03:00:00Z', bytes: 4096 }],
      prune_hold: { since: '2026-10-20T03:00:00Z', snapshot: '20261020T030000Z.tar', kind: 'secret-folders', prev_files: 30, files: 3 },
    },
  });
  const { root, calls } = await open(t, (path) => (path.startsWith('/api/vault/status') ? withSecrets() : { status: 202, body: { status: 'started' } }));
  const text = root.textContent;
  assert.ok(text.includes('/home/you/.fridolin-secrets') && text.includes('sandbox.credentials_dir') && text.includes('3 files'), text);
  assert.ok(text.includes('20261020T030000Z.tar') && text.includes('1, 30 days kept'), 'the last backup and the versions');
  assert.ok(text.includes('Version 20261020T030000Z.tar holds 3 files, down from 30'), 'the hold');
  button(root, 'Back up now').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/vault/secret-folders/backup'), 'the backup');
});

test('a vault without secret folders says how to set them, and one without their key says it is not backed up', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status')
    ? { ...running(), secret_folders: { enabled: false, error: 'the cold tier\'s inner layer has no key', keep_days: 30, folders: [] } } : {}));
  assert.ok(root.textContent.includes('Not backed up') && root.textContent.includes('inner layer has no key'), root.textContent);
});

test('a vault that made no B2 transactions today says so', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? running() : {}));
  assert.ok(root.textContent.includes('No B2 transactions counted today'), root.textContent);
});

test('the B2 transactions show against the budget, with its warnings', async (t) => {
  const busy = () => ({
    ...running(),
    b2: {
      day: '2026-10-20', budget: 2000, classes: { A: 40, B: 1500, C: 300 }, percent: { B: 75, C: 15 },
      yesterday: { A: 1, B: 600, C: 200 },
      warnings: ['1500 B2 Class B transactions today (UTC), 75% of the daily budget of 2000'],
    },
  });
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? busy() : {}));
  const text = root.textContent;
  assert.ok(text.includes('75%') && text.includes('1500 of 2000'), text);
  assert.ok(text.includes('300 of 2000'), 'class C');
  assert.ok(text.includes('1500 B2 Class B transactions today'), 'the warning');
  assert.ok(text.includes('class B 600, class C 200'), 'the day before');
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

test('an off vault shows how to turn it on, and the node targets, which run without it', async (t) => {
  const { root } = await open(t, (path) => (path.startsWith('/api/vault/status') ? { enabled: false, state: 'off' } : {}));
  const text = root.textContent;
  assert.ok(text.includes('The vault is off'), text);
  assert.ok(text.includes('vault.enabled'), text);
  assert.deepEqual(root.querySelectorAll('h2.section-title').map((e) => e.textContent), ['Backup targets'], 'only the targets');
  assert.deepEqual(root.querySelectorAll('button').map((b) => b.textContent), ['Add target…'], 'no vault actions');
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

test('node targets show as cards with their jobs, and an off vault shows them too', async (t) => {
  const target = {
    name: 'nas', kind: 'node', node: 'a1b2c3d4e5f6', node_name: 'windows-pc', folder: 'D:\\Backups\\fridolin',
    nightly_at: '03:00', hourly: false, include_data_dir: false, retention: { hourly: 24, daily: 14, weekly: 8, monthly: 12 },
    window_hours: 26, online: true, ready: true, brains: ['personal'],
    last_nightly: { at: '2026-10-20T03:00:00Z', kind: 'nightly', id: 'cafe1234', files: 42, bytes_added: 1000 },
    size: 5 * GB, files: 300, free: 900 * GB, size_at: '2026-10-20T03:05:00Z',
  };
  const { root, calls } = await open(t, (path, init) => {
    if (path.startsWith('/api/vault/status')) return { enabled: false, state: 'off', targets: [target] };
    if (init.method === 'POST') return { status: 'started' };
    return {};
  });
  const text = root.textContent;
  assert.ok(text.includes('The vault is off'), 'the off hero');
  assert.ok(text.includes('nas') && text.includes('windows-pc') && text.includes('personal'), text);
  assert.ok(text.includes('cafe1234'), 'the last snapshot');
  assert.ok(text.includes('Ready'), 'the state');
  button(root, 'Back up now').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/vault/targets/nas/snapshot'), 'the job of the target');
  button(root, 'Restore drill').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/vault/targets/nas/restore-drill'));
});

test('a target\'s brains are chosen in brain.json, with their node-only paths', async (t) => {
  const target = { name: 'nas', kind: 'node', node: 'a1b2c3d4e5f6', folder: '/Volumes/B/f', nightly_at: '03:00', retention: {}, online: true, ready: true, brains: [] };
  const brains = [
    { id: 'personal', manifest: { backup: { enabled: true, exclude: ['/work/'] } } },
    { id: 'family', manifest: { backup: { enabled: true, exclude: [], targets: { cloud: false } } } },
  ];
  const { root, calls } = await open(t, (path) => {
    if (path.startsWith('/api/vault/status')) return { enabled: false, state: 'off', targets: [target] };
    if (path === '/api/brains') return brains;
    return { status: 'saved' };
  });
  button(root, 'Brains…').click();
  await settle();
  const dlg = document.querySelector('dialog');
  const boxes = dlg.querySelectorAll('input[type=checkbox]');
  boxes[0].checked = true;
  boxes[0].dispatchEvent(new Event('change'));
  const paths = dlg.querySelectorAll('input').filter((i) => i.getAttribute('type') !== 'checkbox');
  paths[0].value = '/work/, /clients/';
  dlg.querySelectorAll('button').find((b) => b.textContent === 'Save').click();
  await settle();
  const puts = calls.filter((c) => c.method === 'PUT');
  assert.equal(puts.length, 1, 'only the brain that changed');
  assert.equal(puts[0].path, '/api/brains/personal/backup');
  assert.deepEqual(puts[0].body, { targets: { 'node:nas': true }, node_only: ['/work/', '/clients/'] });
});

test('the wizard adds a target in a folder picked on the node', async (t) => {
  const node = { id: 'a1b2c3d4e5f6', name: 'windows-pc', device: 'approved', online: true, effective: { backup: { roots: ['D:\\'] } } };
  const { root, calls } = await open(t, (path, init) => {
    if (path.startsWith('/api/vault/status')) return { enabled: false, state: 'off' };
    if (path === '/api/nodes') return { enabled: true, nodes: [node, { id: 'ffffffffffff', name: 'other', device: 'approved', effective: {} }] };
    if (path.endsWith('/call')) {
      const body = JSON.parse(init.body);
      if (body.method === 'fs.roots') return { result: { sep: '\\', roots: [{ path: 'D:\\', label: 'D:' }] } };
      return { result: { path: body.params.path, folders: [] } };
    }
    return { target: {} };
  });
  button(root, 'Add target…').click();
  await settle();
  const dlg = document.querySelector('dialog');
  assert.ok(dlg.textContent.includes('windows-pc') && !dlg.textContent.includes('other ('), 'only nodes whose backups are approved');
  dlg.querySelectorAll('button.fb-item')[0].click();
  await settle();
  const newName = dlg.querySelectorAll('input').find((i) => i.getAttribute('aria-label') === 'New folder name');
  newName.value = 'fridolin';
  dlg.querySelectorAll('button').find((b) => b.textContent === 'Use new folder').click();
  dlg.querySelectorAll('button').find((b) => b.textContent === 'Add target').click();
  await settle();
  const add = calls.find((c) => c.method === 'POST' && c.path === '/api/vault/targets');
  assert.ok(add, 'posted');
  assert.equal(add.body.node, 'a1b2c3d4e5f6');
  assert.equal(add.body.folder, 'D:\\fridolin');
  assert.equal(add.body.name, 'nas');
  assert.equal(add.body.nightly_at, '03:00');
});
