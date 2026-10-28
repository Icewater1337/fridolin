// The GitLab card (docs/CONNECTIONS.md, "GitLab"): a GitLab of your own,
// such as a work GitLab, for FrIDE. Fridolin hands its token to the
// FrIDEs of its nodes, so that a FrIDE workspace can be made under the
// GitLab identity. The token is kept on this machine only, never in a
// backup, a snapshot or an export, and never comes back to the dashboard:
// the card names only its last characters. It follows GET /api/gitlab,
// and GET /api/git-identities for what each node's FrIDE got.

import { del, post, put } from '../app/api.js';
import { resource } from '../app/resources.js';
import { h, paint } from '../lib/dom.js';
import { icon } from './icons.js';
import { toast } from './toast.js';
import { btn } from './buttons.js';
import { confirmDialog } from './dialog.js';
import { field, nextId, textInput } from './fields.js';
import { callout, loading, mono, pill, timeAgo } from './common.js';
import { liveForm } from './live-form.js';

/** @import { Page } from '../app/page.js' */
/** @import { Child } from '../lib/dom.js' */
/** @import { GitIdentityID, GitIdentityNode, GitLabConnection } from '../../types/api.js' */

/** @type {Record<GitIdentityID, string>} */
const LABELS = { github: 'GitHub', gitlab: 'GitLab' };
/** @type {GitIdentityID[]} */
const IDS = ['github', 'gitlab'];

/**
 * What the form is built from.
 * @typedef {{configured: boolean, base_url: string, name: string, email: string}} GitLabForm
 */

/**
 * gitlabCard(ctx): the card, following GET /api/gitlab and
 * GET /api/git-identities for the page. Its dirty() says whether its form
 * has edits.
 * @param {Page} ctx
 * @returns {HTMLElement & {dirty: () => boolean}}
 */
export function gitlabCard(ctx) {
  const gitlab = resource('/api/gitlab');
  const identities = resource('/api/git-identities');
  const refresh = () => Promise.all([gitlab.refresh(), identities.refresh()]);
  /** @type {GitLabConnection | null} */
  let data = null;
  /** @type {{key: string, error: string} | null} */
  let failed = null; // the last check, when it failed for this token at this address

  const headBox = h('div.region');
  const introBox = h('div.region');
  const stateBox = h('div.region');
  const actionsBox = h('div.actions');
  const nodesBox = h('div.connection-part');
  const form = liveForm({ build: buildForm, staleNote: { title: 'The GitLab connection changed elsewhere', action: 'Load the new one' } });

  const card = Object.assign(h('section.card', loading('Loading GitLab…')), { dirty: () => form.dirty() });

  ctx.follow(gitlab, (d) => { data = d; render(); }, 30000);
  ctx.follow(identities, (d) => { paint(nodesBox, (d && d.nodes) || [], renderNodes); }, 30000);

  /** @param {GitLabConnection} d which token the last check was about */
  const keyOf = (d) => `${d.base_url || ''} ${d.token_hint || ''}`;

  function render() {
    if (!data) return;
    const d = data;
    paint(card, 'ready', () => [headBox, introBox, stateBox, form.el, actionsBox, nodesBox]);
    paint(headBox, !!d.configured, (on) => h('div.card-head',
      h('div.card-title', icon('git'), h('h2', 'GitLab')),
      on ? pill('Token set', 'ok') : pill('Not set up', 'neutral')));
    paint(introBox, d.where || '', (where) => [
      h('p', 'A GitLab of your own, such as a work GitLab, for FrIDE. Fridolin hands its token to the FrIDEs of your nodes, so that a FrIDE workspace can be made under the GitLab identity.'),
      h('p.muted.small',
        where ? ['The token is kept in ', where, '.'] : 'The token is kept on this machine only: never in a backup, a snapshot or an export.',
        ' Fridolin never shows it again.'),
    ]);
    paint(stateBox, {
      configured: !!d.configured, host: d.host || '', hint: d.token_hint || '',
      failed: failed && d.configured && failed.key === keyOf(d) ? failed.error : '',
    }, (s) => [
      s.configured ? h('p', 'GitLab at ', mono(s.host), s.hint ? [', token ending ', mono(s.hint)] : null, '.') : null,
      s.failed ? callout('bad', 'The token did not work', s.failed) : null,
    ]);
    form.set({ configured: !!d.configured, base_url: d.base_url || '', name: d.name || '', email: d.email || '' });
    paint(actionsBox, !!d.configured, (on) => [
      btn('Save', { kind: 'primary', icon: 'check', onClick: save }),
      on ? btn('Check', { icon: 'check', title: 'Ask GitLab whether the saved token works', onClick: check }) : null,
      on ? btn('Remove', { kind: 'danger', icon: 'trash', small: true, onClick: remove }) : null,
    ]);
  }

  /** @param {GitLabForm} d */
  function buildForm({ configured, base_url: baseURL, name, email }) {
    const address = textInput({ type: 'url', class: 'mono', value: baseURL, placeholder: 'https://gitlab.example.com' });
    const token = h('input', { type: 'password', id: nextId('gl'), autocomplete: 'off', placeholder: configured ? 'saved — type to replace' : '' });
    const nameIn = textInput({ value: name, placeholder: 'Your Name' });
    const emailIn = textInput({ type: 'email', value: email, placeholder: 'you@example.com' });
    const value = () => ({ base_url: address.value.trim(), token: token.value, name: nameIn.value.trim(), email: emailIn.value.trim() });
    const scopes = h('span', 'Make a personal or group access token in GitLab with ', mono('read_user'), ', ', mono('read_repository'), ' and ',
      mono('write_repository'), '; add ', mono('api'), ' if FrIDE’s agents should use ', mono('glab'), '.');
    const el = h('div.settings-fields',
      field({ label: 'GitLab address', control: address, help: 'Where your GitLab answers, with https.' }),
      field({ label: 'Access token', control: token, help: configured ? h('span', 'Leave it empty to keep the token Fridolin has. ', scopes) : scopes }),
      field({ label: 'Commit name', tag: 'optional', control: nameIn, help: 'What FrIDE’s commits on GitLab carry.' }),
      field({ label: 'Commit email', tag: 'optional', control: emailIn, help: 'Both or neither. Empty: git’s own name and email count.' }));
    return { el, value, token };
  }

  async function save() {
    const f = form.form();
    if (!f) return;
    const v = f.value();
    const d = data;
    const configured = !!(d && d.configured);
    if (!v.base_url) {
      toast('Enter GitLab’s address, such as https://gitlab.example.com', 'warn');
      return;
    }
    if (!/^https:\/\//i.test(v.base_url)) {
      toast('GitLab’s address starts with https://', 'warn');
      return;
    }
    if (!configured && !v.token.trim()) {
      toast('Paste a GitLab access token first', 'warn');
      return;
    }
    if (Boolean(v.name) !== Boolean(v.email)) {
      toast('Give the commit name and email together, or neither', 'warn');
      return;
    }
    // Only what changed goes along, so the audit says what was saved:
    // a blank token keeps the token, a field left out keeps its value.
    /** @type {{token: string, base_url?: string, name?: string, email?: string}} */
    const body = { token: v.token };
    if (v.base_url !== ((d && d.base_url) || '')) body.base_url = v.base_url;
    if (v.name !== ((d && d.name) || '') || v.email !== ((d && d.email) || '')) {
      body.name = v.name;
      body.email = v.email;
    }
    await put('/api/gitlab', body);
    f.token.value = ''; // never kept in the page once it is saved
    form.saved();
    failed = null;
    toast(v.token.trim() ? 'GitLab token saved' : 'GitLab settings saved', 'ok');
    await refresh();
  }

  async function check() {
    if (!data) return;
    const key = keyOf(data);
    const r = await post('/api/gitlab/check');
    if (r && r.ok) {
      failed = null;
      toast(r.detail || 'GitLab knows the token', 'ok', { timeout: 8000 });
    } else {
      failed = { key, error: (r && r.error) || 'The check failed.' };
    }
    render();
  }

  async function remove() {
    const ok = await confirmDialog({
      title: 'Remove the GitLab connection?',
      message: 'Fridolin forgets the token, the address and the commit name and email. The FrIDEs of your nodes lose the GitLab identity too.',
      confirm: 'Remove', danger: true,
    });
    if (!ok) return;
    await del('/api/gitlab');
    failed = null;
    toast('GitLab removed', 'ok');
    await refresh();
  }

  return card;
}

// ── the FrIDE nodes ───────────────────────────────────────────────────

/** @param {GitIdentityNode[]} nodes */
function renderNodes(nodes) {
  return [
    h('h3', 'On the FrIDE nodes'),
    nodes.length
      ? h('div.facts', nodes.map((n) => {
        const kept = IDS.filter((id) => !(n.allowed || []).includes(id));
        return h('div',
          h('strong', n.name || n.id), ' ', n.online ? pill('online', 'ok') : pill('offline', 'neutral'), ' ', nodeState(n),
          kept.length ? h('span.muted', ' · ', kept.map((id) => LABELS[id]).join(' and '), ' kept from it (', mono('nodes.git_identities'), ')') : null);
      }))
      : h('p.muted', 'No node has an approved FrIDE yet. A node with FrIDE gets the git identities once its fride is approved on the ', h('a', { href: '#/nodes' }, 'Nodes'), ' page.'),
    nodes.length ? h('p.muted.small', 'What each node’s FrIDE took last. The GitHub identity is set on the GitHub card.') : null,
  ];
}

/**
 * What a node's FrIDE has of the git identities, in words.
 * @param {GitIdentityNode} n
 * @returns {Child}
 */
function nodeState(n) {
  if (n.held) return h('span.muted', n.held); // kept from its FrIDE on purpose
  if (n.pending) return 'waits for its FrIDE to answer';
  if (n.error) return h('span.warn-text', n.error);
  if (n.at && !n.supported) return 'its FrIDE does not take git identities yet: update it';
  const got = n.got || [];
  if (!got.length) return 'nothing yet';
  return ['got ', got.map((id) => LABELS[id] || id).join(', '), n.at ? [' ', timeAgo(n.at)] : null];
}
