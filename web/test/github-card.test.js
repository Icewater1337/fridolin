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

/**
 * A switch toggle() made: its label, with the checkbox as .input.
 * @typedef {ReturnType<typeof import('../assets/components/fields.js').toggle>} Switch
 */

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

/**
 * What GET /api/git-identities answers, with the GitHub identity's over.
 * @param {object} [github]
 */
const identities = (github = {}) => ({
  identities: [
    { id: 'github', kind: 'github', host: 'github.com', name: '', email: '', set: true, ...github },
    { id: 'gitlab', kind: 'gitlab', host: '', name: '', email: '', set: false },
  ],
  nodes: [],
});

const toasts = () => document.querySelectorAll('.toast').map((el) => el.textContent);
// The callouts shown: a live form keeps its "changed elsewhere" one hidden.
const shownCallouts = (root) => root.querySelectorAll('.callout').filter((c) => !c.hidden);
/** @type {(root: any, label: string) => HTMLInputElement | undefined} */
const inputFor = (root, label) => {
  const l = root.querySelectorAll('label').find((x) => x.textContent.startsWith(label));
  return l ? /** @type {HTMLInputElement} */ (document.getElementById(l.getAttribute('for'))) : undefined;
};

async function openCard(t, first, ids = identities()) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const assigned = [];
  Object.assign(globalThis, { window: { scrollTo() {}, location: { assign: (u) => assigned.push(u) } } });
  t.after(() => { delete globalThis.window; });
  const bridge = { status: first, ids: structuredClone(ids) };
  const calls = serve(t, (path, init) => {
    if (path === '/api/git-identities') return structuredClone(bridge.ids);
    if (path === '/api/github/identity' && init.method === 'PUT') {
      const body = JSON.parse(init.body);
      Object.assign(bridge.ids.identities[0], body);
      return body;
    }
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
  const card = githubCard(ctx);
  root.append(card);
  await resource('/api/github').refresh();
  await resource('/api/git-identities').refresh();
  await settle();
  const pollIdentities = async (next) => { bridge.ids = structuredClone(next); await resource('/api/git-identities').refresh(); await settle(); };
  return { root, card, calls, assigned, pollIdentities };
}

test('Connect GitHub asks for the app the owner chose and goes to the hand-off page', async (t) => {
  const { root, calls, assigned } = await openCard(t, { connected: false, installations: [] });
  assert.match(root.textContent, /Not connected/);
  /** @type {Switch} */
  const sw = root.querySelector('label.switch');
  const prs = sw.input;
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

test('a connected app offers the commit identity for FrIDE, kept across polls, and saves it', async (t) => {
  const { root, card, calls, pollIdentities } = await openCard(t, connected);
  const part = root.querySelectorAll('.connection-part').find((p) => p.textContent.startsWith('Commit identity for FrIDE'));
  assert.ok(part, 'the part is there while the app is connected');
  assert.match(part.textContent, /installation token\. It lasts an hour, and the node renews it before it ends\. Pushing needs the app’s Contents permission set to Read and write\./);
  assert.deepEqual(shownCallouts(part), [], 'no note, no warning');
  assert.equal(card.dirty(), false);

  type(inputFor(root, 'Commit name'), 'Jane Doe');
  button(root, 'Save').click();
  await settle();
  assert.ok(toasts().includes('Give the commit name and email together, or neither'));
  assert.equal(calls.filter((c) => c.path === '/api/github/identity').length, 0, 'nothing went out');

  type(inputFor(root, 'Commit email'), ' you@example.com ');
  assert.equal(card.dirty(), true, 'what is typed counts as unsaved');
  await pollIdentities({ ...identities(), nodes: [{ id: 'n1', name: 'laptop', online: true, allowed: ['github'], got: [], supported: true }] });
  assert.equal(inputFor(root, 'Commit name').value, 'Jane Doe', 'a poll keeps what is typed');
  button(root, 'Save').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.path === '/api/github/identity').map((c) => [c.method, c.body]), [['PUT', { name: 'Jane Doe', email: 'you@example.com' }]]);
  assert.ok(toasts().includes('Commit identity for FrIDE saved'));
  assert.equal(card.dirty(), false);
  assert.equal(inputFor(root, 'Commit email').value, 'you@example.com');

  type(inputFor(root, 'Commit name'), '');
  type(inputFor(root, 'Commit email'), '');
  button(root, 'Save').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.path === '/api/github/identity').at(-1).body, { name: '', email: '' }, 'neither leaves it to git');
  assert.ok(toasts().includes('Commit identity removed: git’s own counts'));
});

test('a note says why FrIDE got no GitHub token', async (t) => {
  const note = 'the app may only read code: set its Contents permission to Read and write';
  const { root, pollIdentities } = await openCard(t, connected, identities({ note, name: 'Jane Doe', email: 'you@example.com' }));
  const [warn, ...more] = shownCallouts(root);
  assert.equal(more.length, 0);
  assert.ok(warn.classList.contains('warn'));
  assert.match(warn.textContent, /^FrIDE gets no GitHub token/);
  assert.ok(warn.textContent.endsWith(note));
  assert.equal(inputFor(root, 'Commit name').value, 'Jane Doe');

  await pollIdentities(identities({ name: 'Jane Doe', email: 'you@example.com' }));
  assert.deepEqual(shownCallouts(root), [], 'gone once FrIDE got one');
});

test('a typed token gets the commit identity too', async (t) => {
  const { root } = await openCard(t, { connected: false, installations: [], typed_token: true });
  assert.match(root.textContent, /Commit identity for FrIDE/);
  assert.match(root.textContent, /get the typed token, FRIDOLIN_GITHUB_TOKEN, while no app is connected/);
  assert.ok(button(root, 'Save'));
});

test('without GitHub there is no commit identity to set', async (t) => {
  const { root, card } = await openCard(t, { connected: false, installations: [] });
  assert.doesNotMatch(root.textContent, /Commit identity for FrIDE/);
  assert.equal(button(root, 'Save'), undefined);
  assert.equal(card.dirty(), false);
});
