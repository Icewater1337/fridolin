import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/site.js';

const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleOnly fridolin-site';
const ADDR = 'git@github.com:you/my-site.git';

// GET /api/site on a fresh bridge, with gh installed.
const fresh = (deployKey = KEY) => ({
  accounts: { admins: 0, total: 0 },
  deploy: null,
  google: { redirect_uri: 'http://127.0.0.1:8780/auth/callback', source: 'none' },
  repo: { connected: false, github: '' },
  runtime: { cloudflared: false, gh: true, node: '/usr/local/bin/node' },
  settings: { enabled: false, title: 'My site', listen: '127.0.0.1:8780', domain: '', repo: { url: '', branch: 'main', subdir: '' }, roles: ['admin', 'member'], approved: '', history: [], tunnel: false, builders: [], proposals: [] },
  site: { healthy: false, local_url: 'http://127.0.0.1:8780/', process: { state: 'stopped', restarts: 0, log: null }, sandbox: { on: true, note: 'module code reaches only what its module.json declares' } },
  tunnel: { process: { state: 'stopped', restarts: 0, log: null }, token_saved: false },
  ...(deployKey ? { deploy_key: deployKey } : {}),
});

const connected = () => {
  const st = fresh();
  st.repo = { connected: true, github: 'you/my-site' };
  st.settings.repo.url = ADDR;
  return st;
};

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const toasts = (kind) => document.querySelectorAll(`.toast.${kind}`).map((el) => el.textContent);

async function openPage(t, status, { github = { ok: true, repo: 'you/my-site' } } = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, (path, init) => {
    if (path === '/api/site/deploy-key/github') return github;
    if (path === '/api/site/deploy-key' && init.method === 'POST') return { deploy_key: KEY };
    if (path === '/api/site') return status;
    if (path === '/api/site/users') return { users: [], roles: ['admin', 'member'] };
    if (path === '/api/site/builders') return { brains: [] };
    if (path === '/api/site/secrets') return { modules: [] };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const addressIn = () => root.querySelectorAll('input').find((i) => i.getAttribute('placeholder') === 'git@github.com:you/my-site.git');
  const ghCalls = () => calls.filter((c) => c.path === '/api/site/deploy-key/github');
  return { root, calls, addressIn, ghCalls };
}

test('before Connect, gh adds the key to the address typed for Connect', async (t) => {
  const { root, calls, addressIn, ghCalls } = await openPage(t, fresh());
  type(addressIn(), `  ${ADDR} `);
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls().map((c) => [c.method, c.body]), [['POST', { url: ADDR }]]);
  assert.deepEqual(toasts('ok'), ['Deploy key added to you/my-site on GitHub']);
  assert.ok(!calls.some((c) => c.path === '/api/site/repo'), 'adding the key does not connect');
  assert.equal(addressIn().value, `  ${ADDR} `, 'the form keeps the address for Connect');
});

test('with no address typed, the button says what is missing and asks the bridge nothing', async (t) => {
  const { root, addressIn, ghCalls } = await openPage(t, fresh());
  type(addressIn(), '   ');
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.equal(ghCalls().length, 0);
  assert.equal(toasts('warn').length, 1);
  assert.match(toasts('warn')[0], /Enter the repository’s SSH address first.*git@github\.com:you\/my-site\.git/);
  assert.equal(document.activeElement, addressIn(), 'the address field gets the focus');
});

test('an address the bridge refuses shows its reason', async (t) => {
  const refused = { status: 400, body: { error: 'the repository is not on GitHub; add the key in its settings by hand' } };
  const { root, addressIn, ghCalls } = await openPage(t, fresh(), { github: refused });
  type(addressIn(), 'git@example.com:you/my-site.git');
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls().map((c) => c.body), [{ url: 'git@example.com:you/my-site.git' }]);
  assert.ok(toasts('error').some((m) => /not on GitHub/.test(m)), `toasts: ${toasts('error')}`);
  assert.deepEqual(toasts('ok'), []);
});

test('making the deploy key keeps the address typed, and gh then uses it', async (t) => {
  const { root, addressIn, ghCalls } = await openPage(t, fresh(null));
  assert.equal(button(root, 'Add it to GitHub with gh'), undefined, 'no key to add yet');
  const input = addressIn();
  type(input, ADDR);
  button(root, 'Create a deploy key').click();
  await settle();
  assert.equal(addressIn(), input, 'the form was not rebuilt');
  assert.equal(input.value, ADDR);
  assert.match(root.textContent, /AAAAC3NzaC1lZDI1NTE5AAAAIExampleOnly/);
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls().map((c) => c.body), [{ url: ADDR }]);
});

test('once connected, gh adds the key to the connected repository as before', async (t) => {
  const { root, ghCalls } = await openPage(t, connected());
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls().map((c) => [c.method, c.body]), [['POST', undefined]]);
  assert.deepEqual(toasts('ok'), ['Deploy key added to you/my-site on GitHub']);
});

test('without gh there is no gh button, before or after Connect', async (t) => {
  for (const st of [fresh(), connected()]) {
    st.runtime.gh = false;
    const { root } = await openPage(t, st);
    assert.equal(button(root, 'Add it to GitHub with gh'), undefined);
    t.mock.timers.reset();
  }
});
