import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import { githubCard } from '../assets/components/github-card.js';

const connected = {
  connected: true, where: 'the core’s secrets', typed_token: false,
  app: { id: 4242, slug: 'fridolin-abcd1234', name: 'fridolin-abcd1234', owner: 'you', html_url: 'https://github.com/apps/fridolin-abcd1234' },
  install_url: 'https://github.com/apps/fridolin-abcd1234/installations/new',
  settings_url: 'https://github.com/settings/apps/fridolin-abcd1234',
  delete_url: 'https://github.com/settings/apps/fridolin-abcd1234/advanced',
  installations: [{ id: 77, account: 'you', html_url: 'https://github.com/settings/installations/77', repository_selection: 'selected', repos: ['you/fride', 'you/fridolin'], permissions: {} }],
};

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

async function openCard(t, first) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const assigned = [];
  globalThis.window = { scrollTo() {}, location: { assign: (u) => assigned.push(u) } };
  t.after(() => { delete globalThis.window; });
  const bridge = { status: first };
  const calls = serve(t, (path, init) => {
    if (path === '/api/github/connect') return { url: '/github/connect?state=abc' };
    if (path === '/api/github/test') return { repo: 'you/fride', permissions: { issues: 'read' }, issue: { number: 7, title: 'A test issue', url: 'x' }, revoked: true };
    if (path === '/api/github/disconnect' && init.method === 'POST') { bridge.status = { connected: false, installations: [] }; return { status: 200, body: { status: 'disconnected', delete_url: connected.delete_url } }; }
    if (path.startsWith('/api/github')) return bridge.status;
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  root.append(githubCard(ctx));
  await resource('/api/github').refresh();
  await settle();
  return { root, calls, assigned };
}

test('Connect GitHub asks for the app the owner chose and goes to the hand-off page', async (t) => {
  const { root, calls, assigned } = await openCard(t, { connected: false, installations: [] });
  assert.match(root.textContent, /Not connected/);
  const prs = root.querySelector('label.switch').input;
  prs.checked = true;
  type(root.querySelector('input[type=text]'), ' acme ');
  button(root, 'Connect GitHub').click();
  await settle();
  const call = calls.find((c) => c.path === '/api/github/connect');
  assert.deepEqual(call.body, { pull_requests: true, org: 'acme' });
  assert.deepEqual(assigned, ['/github/connect?state=abc']);
});

test('a connected app shows its repositories, tests a token and disconnects', async (t) => {
  const { root, calls } = await openCard(t, connected);
  assert.match(root.textContent, /Connected/);
  assert.match(root.textContent, /you\/fride, you\/fridolin/);
  assert.ok(root.querySelectorAll('a').some((a) => a.getAttribute('href') === connected.installations[0].html_url && a.textContent === 'Manage on GitHub'));
  assert.equal(root.textContent.includes('PRIVATE KEY'), false);

  button(root, 'Test').click();
  await settle();
  assert.deepEqual(calls.find((c) => c.path === '/api/github/test').body, { repo: 'you/fride' });
  assert.match(document.body.textContent, /#7 “A test issue” of you\/fride/);

  root.querySelectorAll('button').find((b) => b.textContent === 'Disconnect').click();
  await settle();
  const dialog = document.querySelector('dialog');
  button(dialog, 'Disconnect').click();
  await settle();
  assert.ok(calls.some((c) => c.path === '/api/github/disconnect' && c.method === 'POST'));
  assert.ok(root.querySelectorAll('a').some((a) => a.getAttribute('href') === connected.delete_url), 'the link to delete the app on GitHub is shown');
});

test('an app made but installed nowhere asks to install it', async (t) => {
  const { root } = await openCard(t, { ...connected, installations: [] });
  assert.match(root.textContent, /Not installed yet/);
  const install = root.querySelectorAll('a').find((a) => a.textContent === 'Install the app');
  assert.equal(install.getAttribute('href'), connected.install_url);
});
