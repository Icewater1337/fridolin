import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { capSummary, gb, osName, parseTags, ruleText } from '../assets/lib/nodes.js';
import page from '../assets/pages/nodes.js';

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
});
