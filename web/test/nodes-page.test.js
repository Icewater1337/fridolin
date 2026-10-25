import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { capSummary, gb, nodeNamed, osName, parseTags, programOnNode, ruleText, updateText } from '../assets/lib/nodes.js';
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

async function openPage(t, first) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { nodes: first, enabled: true };
  const calls = serve(t, (path, init) => {
    if (path === '/api/nodes') return { enabled: bridge.enabled, listen: '127.0.0.1:8766', nodes: bridge.nodes };
    if (path === '/api/nodes/join-code') return { code: 'fj-abc', expires_at: '2026-10-20T10:10:00Z', command: 'fridolin node join fj-abc --core mini:8766' };
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
  assert.equal(capSummary({ backup: {} }), 'backup');
  assert.equal(capSummary({ backup: { folders: ['D:\\B'] } }), 'backup (1 folder)');
  assert.equal(capSummary({}), 'nothing');
  assert.equal(capSummary(null), 'nothing');
  assert.equal(ruleText({ name: 'git', args: ['status', 'log'] }), 'git status | log');
  assert.equal(ruleText({ name: 'go', args: null }), 'go (any arguments)');
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
  assert.equal(nodeNamed([n], 'MINI'), n);
  assert.equal(nodeNamed([n], 'abc'), n);
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
