import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { capSummary, gb, nodeRef, nodesNamed, osName, parseTags, programOn, programOnNode, ruleText, updateText } from '../assets/lib/nodes.js';
import page from '../assets/pages/nodes.js';

/** @import { Node as FleetNode } from '../types/api.js' */

const declared = {
  exec: { allow: [{ name: 'hostname', args: [] }, { name: 'git', args: ['status', 'log'] }, { name: 'go', args: null }] },
  fs: { roots: [{ path: '/Users/you/Shared', write: true }, { path: '/Users/you/Docs' }] },
  host: true,
};

const node = (over) => ({
  id: '8233270f8a24', name: 'laptop', os: 'windows', arch: 'amd64', version: 'v1', tags: ['windows'],
  device: 'pending', commands: 'pending', declared, approved: {}, effective: {},
  joined_at: '2026-10-20T10:00:00Z', last_seen: '2026-10-20T10:00:00Z', online: true,
  resources: { cpus: 8, mem_total: 16 * 2 ** 30, mem_available: 4 * 2 ** 30, disk_free: 200 * 2 ** 30 },
  ...over,
});

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

/**
 * Opens the page on a bridge whose nodes are first. route answers what
 * the bridge's stand-in does not (undefined: it does); joinCode is what
 * a new join code answers.
 * @param {import('node:test').TestContext} t
 * @param {object[]} first
 * @param {(path: string, init: import('./serve.js').RouteInit) => unknown} [route]
 * @param {object} [joinCode]
 */
async function openPage(t, first, route = () => undefined, joinCode = { code: 'fj-abc', expires_at: '2026-10-20T10:10:00Z', command: 'fridolin node join fj-abc --core mini:8766' }) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { nodes: first, enabled: true };
  const calls = serve(t, (path, init) => {
    const own = route(path, init);
    if (own !== undefined) return own;
    if (path === '/api/nodes') return { enabled: bridge.enabled, listen: '127.0.0.1:8766', nodes: bridge.nodes };
    if (path === '/api/nodes/join-code') return joinCode;
    if (init.method === 'POST' && path.endsWith('/approve')) { bridge.nodes = [node({ device: 'approved' })]; return { node: bridge.nodes[0] }; }
    if (init.method === 'POST' && path.endsWith('/approve-commands')) {
      bridge.nodes = [node({ device: 'approved', commands: 'approved', approved: declared, effective: declared })];
      return { node: bridge.nodes[0] };
    }
    if (init.method === 'POST' && path.endsWith('/revoke')) { bridge.nodes = [node({ device: 'revoked', online: false })]; return { node: bridge.nodes[0] }; }
    if (init.method === 'PUT' && path.endsWith('/tags')) { bridge.nodes = [node({ tags: JSON.parse(init.body).tags })]; return { node: bridge.nodes[0] }; }
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  return { root, calls, bridge };
}

test('a waiting node says how it was started and what approving lets it do, as WhatsApp does', async (t) => {
  const allows = ['report its system details (OS, disk, memory)', 'read and change the files in /Users/you/Shared'];
  const { root } = await openPage(t, [node({ joined_via: 'chat', allows })]);
  assert.match(root.textContent, /Waiting for you to approve this device/);
  assert.match(root.textContent, /Started with a join code you asked for with !join\. Was this you\?/);
  assert.match(root.textContent, /If you approve, it can:report its system details \(OS, disk, memory\)read and change the files in \/Users\/you\/Shared/);
  assert.doesNotMatch(root.textContent, /Unexpected/);
});

test('a node whose join code came from neither !join nor the dashboard is flagged', async (t) => {
  const { root } = await openPage(t, [node({ allows: [] })]);
  assert.match(root.textContent, /Unexpected: its join code came neither from your !join nor from the dashboard\./);
});

test('a node goes through both approvals and can be revoked', async (t) => {
  const { root, calls } = await openPage(t, [node()]);
  assert.match(root.textContent, /laptop/);
  assert.match(root.textContent, /device waiting/);
  assert.match(root.textContent, /hostname \(no arguments\)/, 'what it offers is shown before it is approved');
  assert.match(root.textContent, /\/Users\/you\/Shared \(read and write\)/);
  assert.equal(button(root, 'Approve what it offers'), undefined, 'the commands wait for the device');

  button(root, 'Approve device').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/nodes/8233270f8a24/approve'));
  assert.match(root.textContent, /commands waiting/);
  assert.match(root.textContent, /Waiting for you to approve what it offers/);
  button(root, 'Approve what it offers').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/nodes/8233270f8a24/approve-commands'));
  assert.match(root.textContent, /May runexec \(hostname, git, go\), fs \(2 roots, 1 writable\), host/);

  button(root, 'Revoke').click();
  await settle();
  document.querySelectorAll('dialog button').find((b) => b.textContent === 'Revoke').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/nodes/8233270f8a24/revoke'));
  assert.match(root.textContent, /revoked/);
  assert.equal(button(root, 'Revoke'), undefined, 'a revoked node has nothing left to do');
});

test('a heartbeat refreshes the figures and keeps the card and its buttons', async (t) => {
  const { root, bridge } = await openPage(t, [node({ device: 'approved', commands: 'approved' })]);
  const revoke = button(root, 'Revoke');
  assert.match(root.textContent, /4\.0 GB free of 16 GB/);
  bridge.nodes = [node({ device: 'approved', commands: 'approved', resources: { cpus: 8, mem_total: 16 * 2 ** 30, mem_available: 6 * 2 ** 30 } })];
  const { nodesResource } = await import('../assets/app/resources.js');
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /6\.0 GB free of 16 GB/, 'the figures follow');
  assert.equal(button(root, 'Revoke'), revoke, 'the button is the same node');
  bridge.nodes = [node({ device: 'approved', commands: 'approved', online: false, last_seen: '2026-10-20T09:00:00Z' })];
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /offline/);
  assert.equal(button(root, 'Revoke'), revoke);
});

test('a node whose FrIDE answers links its UI', async (t) => {
  const fride = { url: 'http://127.0.0.1:7777', repos: ['api', 'web'] };
  const { root } = await openPage(t, [node({ device: 'approved', commands: 'approved', declared: { ...declared, fride }, approved: { ...declared, fride }, effective: { ...declared, fride } })]);
  assert.match(root.textContent, /May runexec \(hostname, git, go\), fs \(2 roots, 1 writable\), host, fride/);
  const link = root.querySelectorAll('a').find((a) => a.textContent === 'http://127.0.0.1:7777');
  assert.ok(link, 'a link to FrIDE');
  assert.equal(link.getAttribute('href'), 'http://127.0.0.1:7777');
  assert.match(root.textContent, /FrIDE: http:\/\/127\.0\.0\.1:7777 · api, web/);
});

// fride_host: sbx on the node, its memory and its instances. Their
// figures move with every report, which repaints them alone.
test('a node that hosts FrIDE instances shows sbx, its memory and its instances', async (t) => {
  const host = (over = {}) => ({
    version: '0.9.1', ready: true, total_memory_mb: 65536, available_memory_mb: 40960, reserve_mb: 6144,
    instances: [
      { name: 'work', state: 'running', memory_mb: 6144, url: 'http://127.0.0.1:17850', repos: ['app'] },
      { name: 'fridolin', state: 'failed', memory_mb: 3072, problem: 'its FrIDE did not answer' },
    ],
    ...over,
  });
  const hosting = (fh) => node({ device: 'approved', commands: 'approved', declared: { ...declared, fride_host: fh }, approved: { ...declared, fride_host: host() }, effective: { ...declared, fride_host: fh } });
  const { root, bridge } = await openPage(t, [hosting(host())]);
  assert.match(root.textContent, /May runexec \(hostname, git, go\), fs \(2 roots, 1 writable\), host, fride-host \(2 instances\)/);
  assert.match(root.textContent, /FrIDE instances: sbx 0\.9\.1 · ready · memory 64 GB, 40 GB available, 6 GB kept for the machine: 34 GB for instances/);
  assert.match(root.textContent, /work running 6 GB/);
  assert.match(root.textContent, /fridolin failed 3 GB · its FrIDE did not answer/);
  assert.equal(root.querySelectorAll('button').filter((b) => b.textContent === 'Stop').length, 1, 'only the running one stops');

  const revoke = button(root, 'Revoke');
  bridge.nodes = [hosting(host({ available_memory_mb: 30720 }))];
  const { nodesResource } = await import('../assets/app/resources.js');
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /30 GB available/, 'the figures follow');
  assert.equal(button(root, 'Revoke'), revoke, 'the card stays');

  bridge.nodes = [hosting(host({ no_login: 'no Claude token is stored for FrIDE\'s agents' }))];
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /fride-host \(no Claude login\)/);
  assert.match(root.textContent, /No Claude login for its agents, so the core gives them no work: no Claude token is stored for FrIDE's agents/);

  bridge.nodes = [hosting(host({ ready: false, problem: 'sbx is not signed in', instances: [] }))];
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /fride-host \(not ready\)/);
  assert.match(root.textContent, /sbx 0\.9\.1 · not ready: sbx is not signed in/);
  assert.match(root.textContent, /No instances yet/);
});

test('a running FrIDE instance is stopped from its node, asking first while it has an open thread', async (t) => {
  const running = { version: '0.9.1', ready: true, total_memory_mb: 32768, available_memory_mb: 16384, reserve_mb: 6144, instances: [{ name: 'work', state: 'running', memory_mb: 6144 }] };
  const stopped = { ...running, instances: [{ name: 'work', state: 'stopped', memory_mb: 6144 }] };
  const hosting = (fh, over = {}) => node({ device: 'approved', commands: 'approved', declared: { fride_host: fh }, approved: { fride_host: fh }, effective: { fride_host: fh }, ...over });
  /** @type {{nodes?: object[]}} */
  const after = {};
  const { root, calls, bridge } = await openPage(t, [hosting(running)], (path, init) => {
    if (path !== '/api/fride/instances/work/stop') return undefined;
    if (!JSON.parse(init.body).force) return { status: 409, body: { error: 'work has an open thread (#12).' } };
    bridge.nodes = after.nodes;
    return { ok: true };
  });
  after.nodes = [hosting(stopped)];

  button(root, 'Stop').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.match(dialog.textContent, /Stop work anyway\?/);
  assert.match(dialog.textContent, /work has an open thread \(#12\)\. Stopping it cuts off the work in progress/);
  dialog.querySelectorAll('button').find((b) => b.textContent === 'Cancel').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.path === '/api/fride/instances/work/stop').map((c) => c.body), [{}], 'cancelled: no force');

  button(root, 'Stop').click();
  await settle();
  document.querySelectorAll('dialog button').find((b) => b.textContent === 'Stop anyway').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.path === '/api/fride/instances/work/stop').map((c) => c.body), [{}, {}, { force: true }]);
  assert.ok(document.querySelectorAll('.toast').some((el) => el.textContent.includes('Stopped work')));
  assert.match(root.textContent, /work stopped/);
  assert.equal(button(root, 'Stop'), undefined);

  // Offline, or with fride_host not approved, nothing is stopped from here.
  bridge.nodes = [hosting(running, { online: false })];
  const { nodesResource } = await import('../assets/app/resources.js');
  await nodesResource.refresh();
  await settle();
  assert.equal(button(root, 'Stop'), undefined, 'not while it is offline');
  bridge.nodes = [hosting(running, { commands: 'pending', effective: {} })];
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /work running/, 'what it offers shows before it is approved');
  assert.equal(button(root, 'Stop'), undefined, 'not before fride_host is approved');
});

test('the join code dialog shows the line to paste', async (t) => {
  const { root, calls } = await openPage(t, []);
  assert.match(root.textContent, /No nodes yet/);
  button(root, 'New join code').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.ok(dialog, 'a dialog');
  assert.match(dialog.textContent, /fridolin node join fj-abc --core mini:8766/);
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/nodes/join-code'));
});

test('the join code dialog offers the native line first, then the box, then the plain join', async (t) => {
  const { root } = await openPage(t, [], () => undefined, {
    code: 'fj-abc', expires_at: '2026-10-20T10:10:00Z', core: 'mini:8766',
    command: 'fridolin node join fj-abc --core mini:8766',
    command_powershell: '& "$env:ProgramFiles\\fridolin\\fridolin.exe" node join fj-abc --core mini:8766',
    native_command: 'curl -fsSL https://example.com/f/container/node.sh | sh -s -- --core mini:8766 --code fj-abc',
    native_command_powershell: '& ([scriptblock]::Create((irm https://example.com/f/container/node.ps1))) -Core mini:8766 -Code fj-abc',
    box_command: 'curl -fsSL https://example.com/f/container/box.sh | sh -s -- --registry ghcr.io/owner --version v1 --core mini:8766 --code fj-abc',
    box_command_powershell: '& ([scriptblock]::Create((irm https://example.com/f/container/box.ps1))) -Registry ghcr.io/owner -Version v1 -Core mini:8766 -Code fj-abc',
  });
  button(root, 'New join code').click();
  await settle();
  const text = document.querySelector('dialog').textContent;
  const at = (s) => {
    const i = text.indexOf(s);
    assert.ok(i >= 0, `the dialog shows ${s}`);
    return i;
  };
  assert.ok(at('Natively') < at('node.sh') && at('node.sh') < at('node.ps1'), 'the native lines first');
  assert.ok(at('node.ps1') < at('In a box') && at('In a box') < at('box.sh') && at('box.sh') < at('box.ps1'), 'then the box');
  assert.ok(at('box.ps1') < at('With fridolin installed already') && at('With fridolin installed already') < at('fridolin node join fj-abc'), 'then the plain join');
  assert.match(text, /can run FrIDE instances, keep backups and become the core/);
  assert.match(text, /where Docker Sandboxes can't run/);
  assert.match(text, /-Root PATH:rw/);
});

test('tags are edited in a dialog', async (t) => {
  const { root, calls } = await openPage(t, [node({ device: 'approved', commands: 'approved' })]);
  button(root, 'Tags').click();
  await settle();
  const input = document.querySelector('dialog input');
  input.value = 'GPU, docker  memory:32gb';
  document.querySelectorAll('dialog button').find((b) => b.textContent === 'Save').click();
  await settle();
  const put = calls.find((c) => c.method === 'PUT');
  assert.deepEqual(put.body, { tags: ['gpu', 'docker', 'memory:32gb'] });
});

test('nodes off says so and makes no codes', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  serve(t, () => ({ enabled: false, listen: '', nodes: [] }));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  // The resource is shared with the tests above; this one asks it again.
  const { nodesResource } = await import('../assets/app/resources.js');
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /Nodes are off/);
  assert.equal(button(root, 'New join code').disabled, true);
});

test('the capability words match the bridge\'s', () => {
  assert.equal(capSummary(declared), 'exec (hostname, git, go), fs (2 roots, 1 writable), host');
  assert.equal(capSummary({ host: true, fride: { url: 'http://127.0.0.1:7777' } }), 'host, fride');
  const fh = { version: '1', ready: true, total_memory_mb: 0, available_memory_mb: 0, reserve_mb: 0 };
  assert.equal(capSummary({ fride_host: fh }), 'fride-host (0 instances)');
  assert.equal(capSummary({ fride_host: { ...fh, instances: [{ name: 'work', state: 'running', memory_mb: 0 }] } }), 'fride-host (1 instance)');
  assert.equal(capSummary({ fride_host: { ...fh, ready: false, instances: [{ name: 'work', state: 'running', memory_mb: 0 }] } }), 'fride-host (not ready)');
  assert.equal(capSummary({ fride_host: { ...fh, no_login: 'no Claude token is stored' } }), 'fride-host (no Claude login)');
  assert.equal(capSummary({ fride_host: { ...fh, ready: false, no_login: 'no Claude token is stored' } }), 'fride-host (not ready)');
  assert.equal(capSummary({ backup: {} }), 'backup');
  assert.equal(capSummary({ backup: { folders: ['D:\\B'] } }), 'backup (1 folder)');
  assert.equal(capSummary({}), 'nothing');
  assert.equal(capSummary(null), 'nothing');
  assert.equal(ruleText({ name: 'git', args: ['status', 'log'] }), 'git status | log');
  assert.equal(ruleText({ name: 'go', args: null }), 'go (any arguments)');
  assert.equal(ruleText({ name: 'brew', args: ['upgrade', 'update'], approve: true }), 'brew upgrade | update · asks you each time');
  assert.equal(ruleText({ name: 'shutdown', args: ['-r', 'now'], exact: true, approve: true }), 'shutdown -r now (exactly) · asks you each time');
  assert.equal(ruleText({ name: 'softwareupdate', args: [], approve: true }), 'softwareupdate (no arguments) · asks you each time');
  assert.equal(osName('darwin'), 'macOS');
  assert.equal(osName('plan9'), 'plan9');
  assert.equal(gb(16 * 2 ** 30), '16 GB');
  assert.equal(gb(1.5 * 2 ** 30), '1.5 GB');
  assert.equal(gb(0), '');
  assert.deepEqual(parseTags(' GPU,gpu  docker,'), ['gpu', 'docker']);
  assert.equal(capSummary({ host: true, update: {} }), 'host, update');
  assert.equal(updateText({ state: 'failed', to: 'v2', error: 'no luck' }), 'the update to v2 failed: no luck');
  assert.equal(updateText(null), null);
});

test('a node older than the core is updated with a button, and may update by itself', async (t) => {
  const upd = { update: {} };
  const older = node({ device: 'approved', commands: 'approved', declared: { ...declared, ...upd }, approved: { ...declared, ...upd }, effective: { ...declared, ...upd }, update_to: 'v2' });
  const { root, calls, bridge } = await openPage(t, [older]);
  assert.match(root.textContent, /May runexec \(hostname, git, go\), fs \(2 roots, 1 writable\), host, update/);
  button(root, 'Update to v2').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/nodes/8233270f8a24/update'));
  bridge.nodes = [{ ...older, update_to: undefined, version: 'v2', update: { state: 'ok', from: 'v1', to: 'v2', auto: false } }];
  const { nodesResource } = await import('../assets/app/resources.js');
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /updated from v1 to v2/);
  assert.equal(button(root, 'Update to v2'), undefined, 'nothing to update to any more');
  const sw = root.querySelectorAll('input').find((i) => i.getAttribute('role') === 'switch');
  assert.ok(sw, 'the auto switch');
  sw.checked = true;
  sw.dispatchEvent(new Event('change'));
  await settle();
  const put = calls.find((c) => c.method === 'PUT' && c.path === '/api/nodes/8233270f8a24/update-policy');
  assert.deepEqual(put.body, { auto: true });
});

test('a node that cannot be updated says why', async (t) => {
  const { root } = await openPage(t, [node({ device: 'approved', commands: 'approved', update_note: 'its fridolin v1 cannot be updated from the core: update it by hand once' })]);
  assert.match(root.textContent, /update it by hand once/);
  assert.equal(root.querySelectorAll('button').find((b) => b.textContent.startsWith('Update to')), undefined);
});

test('a node with terminal approved and online has a Terminal button', async (t) => {
  const work = { roots: ['/Users/you/Projects'], providers: ['claude'], sandbox: 'sandbox-exec' };
  const ws = { ...declared, agent: work, terminal: work };
  const { root, bridge } = await openPage(t, [node({ device: 'approved', commands: 'approved', declared: ws, approved: ws, effective: ws })]);
  const { nodesResource } = await import('../assets/app/resources.js');
  await nodesResource.refresh();
  await settle();
  assert.match(root.textContent, /Agent sessions: \/Users\/you\/Projects · claude · sandbox sandbox-exec/);
  assert.match(root.textContent, /agent \(1 folder\), terminal \(1 folder\)/);
  assert.ok(button(root, 'Terminal'), 'the Terminal button is there');

  bridge.nodes = [node({ device: 'approved', commands: 'approved', declared: ws, approved: ws, effective: ws, online: false })];
  await nodesResource.refresh();
  await settle();
  assert.equal(button(root, 'Terminal'), undefined, 'not while it is offline');
});

test('core-host shows the device key and whether the owner confirmed it, and asks nothing', async (t) => {
  const coreHost = { confirmed: false, fingerprint: 'a1b2 c3d4 e5f6 0718 293a 4b5c 6d7e 8f90', send_to: [], approval: 'Moving the core there lets that machine hold all of the secrets.' };
  const decl = { ...declared, core_host: { hash: 'ab', send_to: [] } };
  const { root, calls } = await openPage(t, [node({ device: 'approved', declared: decl, core_host: coreHost })]);
  assert.match(root.textContent, /Core host: device key a1b2 c3d4/);
  assert.match(root.textContent, /never held the core: the first !movecore vote in WhatsApp confirms it/);
  assert.doesNotMatch(root.textContent, /core-host waits for your approval/);
  button(root, 'Approve what it offers').click();
  await settle();
  assert.equal(document.querySelector('dialog'), null, 'approving asks nothing about core-host');
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/nodes/8233270f8a24/approve-commands'));
});

test('a confirmed core host says so', async (t) => {
  const coreHost = { confirmed: true, fingerprint: 'a1b2 c3d4 e5f6 0718 293a 4b5c 6d7e 8f90', send_to: ['0123456789ab'], approval: '' };
  const decl = { ...declared, core_host: { hash: 'ab', send_to: ['0123456789ab'] } };
  const { root } = await openPage(t, [node({ device: 'approved', declared: decl, core_host: coreHost })]);
  assert.match(root.textContent, /confirmed: it may hold the secrets · pins 0123456789ab/);
});

test('programs from the core: the folders, what was added for which agents, and what was refused', async (t) => {
  const exec = {
    allow: [{ name: 'uptime', args: [] }, { name: '/Users/you/tools/daily.sh', args: ['--quiet'], exact: true, core: true }],
    accept_from_core: ['/Users/you/tools'],
  };
  const { root } = await openPage(t, [node({
    device: 'approved', commands: 'approved', declared: { exec }, approved: { exec }, effective: { exec },
    programs: {
      at: '2026-10-20T10:00:00Z',
      sent: [],
      accepted: [{ name: '/Users/you/tools/daily.sh', args: ['--quiet'], agents: ['main/daily'] }],
      refused: [{ name: '/Users/you/tools/escape.sh', args: [], why: 'it is /tmp/x, which is in no folder this node accepts programs from' }],
    },
  })]);
  const text = root.textContent;
  assert.match(text, /Programs from the core: \/Users\/you\/tools/);
  assert.match(text, /\/Users\/you\/tools\/daily\.sh --quiet \(exactly\) for main\/daily/);
  assert.match(text, /Refused: .*escape\.sh — it is \/tmp\/x/);
  assert.match(text, /Commands: uptime \(no arguments\)Programs from the core/, 'node.json’s own rules stay apart');
});

test('how a program stands on a node, before its agent is saved', () => {
  // Only what programOnNode reads of a GET /api/nodes entry.
  /** @type {(o: object) => FleetNode} */
  const node = (o) => /** @type {FleetNode} */ (/** @type {unknown} */ (o));
  const exec = { allow: [{ name: '/opt/bin/backup', args: ['--all'] }, { name: '/Users/you/tools/a.sh', args: [], exact: true, core: true }], accept_from_core: ['/Users/you/tools'] };
  const n = node({ id: 'abc', name: 'mini', declared: { exec }, effective: { exec } });
  assert.deepEqual(nodesNamed([n], 'MINI'), [n]);
  assert.deepEqual(nodesNamed([n], ' abc '), [n]);
  assert.deepEqual(nodesNamed([n], 'max'), []);
  assert.equal(programOnNode(null, ['/x']).state, 'no-node');
  assert.equal(programOnNode(n, ['/opt/bin/backup', '--all']).state, 'allowed');
  assert.equal(programOnNode(n, ['/Users/you/tools/a.sh']).state, 'allowed');
  assert.equal(programOnNode(n, ['/Users/you/tools/b.sh', 'x']).state, 'waiting');
  assert.match(programOnNode(n, ['/Users/you/tools/b.sh']).text, /exactly these arguments/);
  assert.equal(programOnNode(n, ['/Users/you/toolsx/b.sh']).state, 'not-accepted');
  assert.equal(programOnNode(n, ['/opt/bin/backup', '--none']).state, 'not-accepted');
  const pending = node({ ...n, effective: { exec: { allow: [] } } });
  assert.match(programOnNode(pending, ['/Users/you/tools/b.sh']).text, /waits for your approval/);
  const none = node({ id: 'x', name: 'box', declared: {}, effective: {} });
  assert.match(programOnNode(none, ['/Users/you/tools/b.sh']).text, /accepts no programs from the core/);
  assert.equal(ruleText({ name: '/a.sh', args: ['x'], exact: true }), '/a.sh x (exactly)');
  assert.equal(capSummary({ exec: { allow: [], accept_from_core: ['/Users/you/tools'] } }), 'exec from the core (/Users/you/tools)');
});

test('a command agent names its node as the bridge finds it (#251)', () => {
  /** @type {(o: object) => FleetNode} */
  const node = (o) => /** @type {FleetNode} */ (/** @type {unknown} */ (o));
  const exec = { allow: [], accept_from_core: ['/Users/you/tools'] };
  const old = node({ id: 'aaa', name: 'laptop', device: 'revoked', declared: { exec }, effective: { exec } });
  const laptop = node({ id: 'bbb', name: 'Laptop', device: 'approved', declared: { exec }, effective: { exec } });
  const desk1 = node({ id: 'ccc', name: 'desktop', device: 'approved', declared: { exec }, effective: { exec } });
  const desk2 = node({ id: 'ddd', name: 'Desktop', device: 'pending', declared: { exec }, effective: { exec } });
  const gone = node({ id: 'eee', name: 'old', device: 'revoked', declared: { exec }, effective: { exec } });
  const fleet = [old, laptop, desk1, desk2, gone];
  // A revoked node steps aside for the one that has its name now, and
  // answers to it alone; its id still names it.
  assert.deepEqual(nodesNamed(fleet, 'LAPTOP'), [laptop]);
  assert.deepEqual(nodesNamed(fleet, 'old'), [gone]);
  assert.deepEqual(nodesNamed(fleet, 'aaa'), [old]);
  assert.equal(programOn(fleet, 'laptop', ['/Users/you/tools/b.sh']).state, 'waiting');
  // A name two nodes have names neither, and the text lists both.
  assert.deepEqual(nodesNamed(fleet, 'desktop'), [desk1, desk2]);
  const p = programOn(fleet, ' desktop ', ['/Users/you/tools/b.sh']);
  assert.equal(p.state, 'no-node');
  assert.equal(p.text, '2 nodes are called desktop: desktop (ccc), Desktop (ddd). Name one by its id.');
  assert.equal(programOn(fleet, 'ddd', ['/Users/you/tools/b.sh']).state, 'waiting');
  assert.equal(programOn(fleet, 'nope', ['/x']).state, 'no-node');
  // The wizard offers each by a ref that names it alone.
  assert.deepEqual(fleet.map((n) => nodeRef(fleet, n)), ['aaa', 'Laptop', 'ccc', 'ddd', 'old']);
});
