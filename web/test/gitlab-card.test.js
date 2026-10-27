import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import { gitlabCard } from '../assets/components/gitlab-card.js';

/** @import { GitIdentities, GitIdentityNode, GitLabConnection } from '../types/api.js' */

const TOKEN = 'test-gitlab-token-0123456789wxyz';
const WHERE = 'this machine’s data folder (core/local-secrets): never in a backup, a snapshot or an export';

/** @returns {GitLabConnection} */
const gitlab = (over = {}) => ({ configured: false, base_url: '', host: '', token_hint: '', name: '', email: '', where: WHERE, ...over });

/** @returns {GitLabConnection} */
const configured = (over = {}) => gitlab({
  configured: true, base_url: 'https://gitlab.example.com', host: 'gitlab.example.com', token_hint: '…c3d4', ...over,
});

/** @returns {GitIdentityNode} */
const node = (over = {}) => ({ id: 'n1', name: 'laptop', online: true, allowed: ['github', 'gitlab'], got: [], supported: false, ...over });

/** @returns {GitIdentities} */
const identities = (nodes = []) => ({
  identities: [
    { id: 'github', kind: 'github', host: 'github.com', name: '', email: '', set: true },
    { id: 'gitlab', kind: 'gitlab', host: 'gitlab.example.com', name: '', email: '', set: true },
  ],
  nodes,
});

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const toasts = () => document.querySelectorAll('.toast').map((el) => el.textContent);
/** @type {(root: any, label: string) => HTMLInputElement | undefined} */
const inputFor = (root, label) => {
  const l = root.querySelectorAll('label').find((x) => x.textContent.startsWith(label));
  return l ? /** @type {HTMLInputElement} */ (document.getElementById(l.getAttribute('for'))) : undefined;
};

/**
 * The card on a page, with a stand-in bridge that holds first and keeps
 * what the card saves.
 * @param {import('node:test').TestContext} t
 * @param {GitLabConnection} first
 * @param {{nodes?: GitIdentityNode[], check?: {ok: boolean, detail?: string, error?: string}}} [options]
 */
async function openCard(t, first, { nodes = [], check = { ok: true, detail: 'GitLab at gitlab.example.com knows the token as you' } } = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { data: structuredClone(first), ids: identities(nodes), check };
  const calls = serve(t, (path, init) => {
    const method = init.method || 'GET';
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (path === '/api/gitlab' && method === 'GET') return structuredClone(bridge.data);
    if (path === '/api/git-identities') return structuredClone(bridge.ids);
    if (path === '/api/gitlab' && method === 'PUT') {
      const c = bridge.data;
      if (!body.token && !c.configured) return { status: 400, body: { error: 'enter the GitLab access token' } };
      if (body.token) Object.assign(c, { configured: true, token_hint: `…${body.token.slice(-4)}` });
      if (body.base_url !== undefined) Object.assign(c, { base_url: body.base_url, host: new URL(body.base_url).host });
      if (body.name !== undefined) c.name = body.name;
      if (body.email !== undefined) c.email = body.email;
      return c;
    }
    if (path === '/api/gitlab' && method === 'DELETE') {
      bridge.data = gitlab();
      return { status: 204 };
    }
    if (path === '/api/gitlab/check' && method === 'POST') return bridge.check;
    return { status: 404, body: { error: 'not found' } };
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  const card = gitlabCard(ctx);
  root.append(card);
  await resource('/api/gitlab').refresh();
  await resource('/api/git-identities').refresh();
  await settle();
  /** @param {GitLabConnection} next */
  const poll = async (next) => { bridge.data = structuredClone(next); await resource('/api/gitlab').refresh(); await settle(); };
  /** @param {GitIdentityNode[]} next */
  const pollNodes = async (next) => { bridge.ids = identities(next); await resource('/api/git-identities').refresh(); await settle(); };
  const sent = (path, method) => calls.filter((c) => c.path === path && c.method === method);
  return { root, card, bridge, sent, poll, pollNodes };
}

test('a GitLab without a token says what it is for, and saving asks for what is missing', async (t) => {
  const { root, card, sent } = await openCard(t, gitlab());
  assert.match(root.textContent, /Not set up/);
  assert.match(root.textContent, /A GitLab of your own, such as a work GitLab, for FrIDE\./);
  assert.match(root.textContent, /The token is kept in this machine’s data folder \(core\/local-secrets\): never in a backup, a snapshot or an export\. Fridolin never shows it again\./);
  assert.match(root.textContent, /read_user, read_repository and write_repository; add api if FrIDE’s agents should use glab/);
  assert.equal(button(root, 'Check'), undefined, 'nothing to check without a token');
  assert.equal(button(root, 'Remove'), undefined);
  assert.equal(root.querySelector('input[type=password]').getAttribute('placeholder'), '');

  button(root, 'Save').click();
  await settle();
  assert.deepEqual(toasts(), ['Enter GitLab’s address, such as https://gitlab.example.com']);

  type(inputFor(root, 'GitLab address'), 'http://gitlab.example.com');
  button(root, 'Save').click();
  await settle();
  assert.ok(toasts().includes('GitLab’s address starts with https://'));

  type(inputFor(root, 'GitLab address'), ' https://gitlab.example.com ');
  button(root, 'Save').click();
  await settle();
  assert.ok(toasts().includes('Paste a GitLab access token first'));

  type(root.querySelector('input[type=password]'), '   ');
  button(root, 'Save').click();
  await settle();
  assert.equal(sent('/api/gitlab', 'PUT').length, 0, 'blank is no token');

  type(root.querySelector('input[type=password]'), TOKEN);
  type(inputFor(root, 'Commit name'), 'Jane Doe');
  button(root, 'Save').click();
  await settle();
  assert.ok(toasts().includes('Give the commit name and email together, or neither'));
  assert.equal(sent('/api/gitlab', 'PUT').length, 0, 'nothing went out');
  assert.equal(card.dirty(), true, 'what is typed counts as unsaved');
});

test('saving sends the address, the token and the commit identity, and the token leaves the page', async (t) => {
  const { root, card, sent } = await openCard(t, gitlab());
  type(inputFor(root, 'GitLab address'), 'https://gitlab.example.com');
  type(root.querySelector('input[type=password]'), TOKEN);
  type(inputFor(root, 'Commit name'), ' Jane Doe ');
  type(inputFor(root, 'Commit email'), 'you@example.com');
  button(root, 'Save').click();
  await settle();
  assert.deepEqual(sent('/api/gitlab', 'PUT').map((c) => c.body), [{ token: TOKEN, base_url: 'https://gitlab.example.com', name: 'Jane Doe', email: 'you@example.com' }]);
  assert.ok(toasts().includes('GitLab token saved'));
  assert.equal(card.dirty(), false);
  assert.match(root.textContent, /Token set/);
  assert.match(root.textContent, /GitLab at gitlab\.example\.com, token ending …wxyz\./);
  assert.equal(root.querySelector('input[type=password]').getAttribute('placeholder'), 'saved — type to replace');
  assert.ok(button(root, 'Check') && button(root, 'Remove'));
  // The token went to the bridge, and is nowhere on the page now.
  assert.equal(document.body.textContent.includes(TOKEN), false);
  assert.ok(document.querySelectorAll('input').every((i) => i.value !== TOKEN), 'no field holds the token once it is saved');
});

test('a configured GitLab sends only what changed, a blank token keeping the token', async (t) => {
  const { root, sent } = await openCard(t, configured({ name: 'Jane Doe', email: 'you@example.com' }));
  assert.match(root.textContent, /GitLab at gitlab\.example\.com, token ending …c3d4\./);
  assert.equal(inputFor(root, 'GitLab address').value, 'https://gitlab.example.com');
  assert.match(root.textContent, /Leave it empty to keep the token Fridolin has\./);

  type(inputFor(root, 'GitLab address'), 'https://git.example.com/gitlab');
  button(root, 'Save').click();
  await settle();
  assert.ok(toasts().includes('GitLab settings saved'));
  type(inputFor(root, 'Commit email'), 'jane@example.com');
  button(root, 'Save').click();
  await settle();
  type(inputFor(root, 'Commit name'), '');
  type(inputFor(root, 'Commit email'), '');
  button(root, 'Save').click();
  await settle();
  assert.deepEqual(sent('/api/gitlab', 'PUT').map((c) => c.body), [
    { token: '', base_url: 'https://git.example.com/gitlab' },
    { token: '', name: 'Jane Doe', email: 'jane@example.com' },
    { token: '', name: '', email: '' },
  ], 'the name and email go together; neither leaves them to git');
});

test('edits survive the polls, and a change elsewhere is said', async (t) => {
  const { root, card, poll } = await openCard(t, configured());
  const note = () => /** @type {HTMLElement} */ (root.querySelector('.live-form').firstChild); // the "changed elsewhere" note
  assert.match(note().textContent, /The GitLab connection changed elsewhere/);
  const address = inputFor(root, 'GitLab address');
  type(address, 'https://gitlab.example.org');
  await poll(configured({ token_hint: '…e5f6' }));
  assert.equal(inputFor(root, 'GitLab address'), address, 'the form was not rebuilt');
  assert.equal(address.value, 'https://gitlab.example.org');
  assert.equal(card.dirty(), true);
  assert.match(root.textContent, /token ending …e5f6/, 'what is around the form follows the poll');
  assert.equal(note().hidden, true, 'a new token is no change to what the form edits');
  await poll(configured({ token_hint: '…e5f6', name: 'Jane Doe', email: 'you@example.com' }));
  assert.equal(note().hidden, false, 'the change elsewhere is said');
  assert.equal(address.value, 'https://gitlab.example.org', 'the edits are kept');
});

test('Check says what GitLab knows the token as, or why it failed', async (t) => {
  const { root, bridge, sent, poll } = await openCard(t, configured());
  button(root, 'Check').click();
  await settle();
  assert.equal(sent('/api/gitlab/check', 'POST').length, 1);
  assert.ok(toasts().includes('GitLab at gitlab.example.com knows the token as you'));
  assert.doesNotMatch(root.textContent, /The token did not work/);

  bridge.check = { ok: false, error: 'GitLab says the token is wrong (401)' };
  button(root, 'Check').click();
  await settle();
  assert.match(root.textContent, /The token did not work.*GitLab says the token is wrong \(401\)/);
  assert.ok(root.querySelector('.callout.bad'));
  // A token replaced elsewhere is not the one that failed.
  await poll(configured({ token_hint: '…9z9z' }));
  assert.doesNotMatch(root.textContent, /The token did not work/);
});

test('Remove asks first, says the FrIDEs lose it too, and deletes', async (t) => {
  const { root, sent } = await openCard(t, configured());
  button(root, 'Remove').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.match(dialog.textContent, /Remove the GitLab connection\?/);
  assert.match(dialog.textContent, /The FrIDEs of your nodes lose the GitLab identity too\./);
  button(dialog, 'Cancel').click();
  await settle();
  assert.equal(sent('/api/gitlab', 'DELETE').length, 0, 'cancelled, nothing is removed');

  button(root, 'Remove').click();
  await settle();
  const again = document.querySelectorAll('dialog').find((d) => d.open);
  button(again, 'Remove').click();
  await settle();
  assert.equal(sent('/api/gitlab', 'DELETE').length, 1);
  assert.ok(toasts().includes('GitLab removed'));
  assert.match(root.textContent, /Not set up/);
  assert.equal(button(root, 'Check'), undefined);
  assert.equal(inputFor(root, 'GitLab address').value, '', 'the form shows the connection is gone');
});

test('the nodes part says what each node’s FrIDE got', async (t) => {
  const at = '2026-10-27T09:00:00Z';
  const { root, pollNodes } = await openCard(t, configured(), {
    nodes: [
      node({ id: 'n1', name: 'laptop', got: ['github', 'gitlab'], supported: true, at }),
      node({ id: 'n2', name: 'mac-mini', online: false, allowed: ['github'], got: ['github'], supported: true, at }),
      node({ id: 'n3', name: 'build-box', pending: true }),
      node({ id: 'n4', name: 'old-box', supported: false, at }),
      node({ id: 'n5', name: 'broken-box', supported: true, error: 'fride said: no such workspace', at }),
      node({ id: 'n6', name: 'new-box' }),
      node({ id: 'n7', name: 'sandbox-box', supported: true, pending: true, error: 'x', held: 'the FrIDE in a Docker Sandbox gets no git token yet', at }),
    ],
  });
  const part = root.querySelector('.connection-part');
  assert.match(part.textContent, /On the FrIDE nodes/);
  const rows = Array.from(part.querySelector('.facts').children);
  const lines = rows.map((d) => d.textContent);
  assert.equal(lines.length, 7);
  assert.match(lines[0], /^laptop online got GitHub, GitLab /);
  assert.doesNotMatch(lines[0], /kept from it/);
  assert.match(lines[1], /^mac-mini offline got GitHub .*· GitLab kept from it \(nodes\.git_identities\)$/);
  assert.equal(lines[2], 'build-box online waits for its FrIDE to answer');
  assert.equal(lines[3], 'old-box online its FrIDE does not take git identities yet: update it');
  assert.equal(lines[4], 'broken-box online fride said: no such workspace');
  assert.equal(lines[5], 'new-box online nothing yet');
  assert.equal(lines[6], 'sandbox-box online the FrIDE in a Docker Sandbox gets no git token yet', 'held wins over the other states');
  const held = rows[6].querySelectorAll('span.muted');
  assert.ok(held.some((s) => s.textContent.startsWith('the FrIDE in a Docker Sandbox')), 'held is a note, not an error');

  await pollNodes([]);
  assert.match(root.querySelector('.connection-part').textContent, /No node has an approved FrIDE yet\. A node with FrIDE gets the git identities once its fride is approved/);
  assert.equal(root.querySelector('.facts'), null);
});
