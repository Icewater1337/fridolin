// The GitHub card (docs/GITHUB.md): Connect GitHub makes a GitHub App of
// this Fridolin's own through GitHub's manifest flow, then GitHub asks
// which repositories it may use. Runs get short-lived tokens of it, each
// for the repositories and permissions of one use. Its key never comes
// back to the dashboard. While an app is connected, or a typed token is
// set, the FrIDEs of the nodes get a GitHub identity too: its commit name
// and email are set here, following GET /api/git-identities.

import { post, put } from '../app/api.js';
import { resource } from '../app/resources.js';
import { h, paint } from '../lib/dom.js';
import { icon } from './icons.js';
import { toast } from './toast.js';
import { btn } from './buttons.js';
import { confirmDialog } from './dialog.js';
import { field, toggle, textInput } from './fields.js';
import { pill, callout, mono, loading } from './common.js';
import { liveForm } from './live-form.js';

/** @import { Page } from '../app/page.js' */
/** @import { GitHubAppInfo, GitHubStatus, GitIdentityRow } from '../../types/api.js' */

/** @type {(href: string, text: string) => HTMLAnchorElement} */
const external = (href, text) => h('a', { href, target: '_blank', rel: 'noopener noreferrer' }, text);
/** @type {(href: string | undefined, label: string, kind?: string) => HTMLAnchorElement} */
const linkButton = (href, label, kind = '') => h(`a.btn${kind ? `.${kind}` : ''}`, { href, target: '_blank', rel: 'noopener noreferrer' }, icon('external'), h('span.btn-label', label));

/**
 * githubCard(ctx): the card, following /api/github for the page. Its
 * dirty() says whether the commit identity form has edits.
 * @param {Page} ctx
 * @returns {HTMLElement & {dirty: () => boolean}}
 */
export function githubCard(ctx) {
  const github = resource('/api/github');
  const identities = resource('/api/git-identities');
  const refresh = () => github.refresh();
  // What the FrIDEs get: the app's installation token, a typed token, or
  // nothing while GitHub is not connected.
  /** @type {'app' | 'typed' | ''} */
  let fride = '';
  /** @type {GitIdentityRow | null} */
  let row = null; // the GitHub identity, with its commit name and email
  // Where the app is deleted on GitHub, shown once it is disconnected here.
  let deleteURL = '';

  // The connect form is built once, so what is typed survives a refresh.
  const prs = toggle({ label: 'Let FrIDE open pull requests (pull_requests: write)' });
  const org = textInput({ placeholder: 'your own account' });
  const connectForm = h('div.settings-fields',
    field({ control: prs, help: 'Off: the app may read code and write issues. FrIDE’s pushes and pull requests need this on; you can change the app’s permissions on GitHub later too.' }),
    field({ label: 'Organization that owns the app', control: org, help: 'Leave empty to make the app on your own account.' }));

  // The commit identity for FrIDE is kept apart from the rest of the
  // card, so that what is typed survives its refreshes.
  const mainBox = h('div.region');
  const notesBox = h('div.region');
  const fridePart = h('div.connection-part');
  const identity = liveForm({ build: buildIdentity, staleNote: { title: 'The commit identity changed elsewhere', action: 'Load the new one' } });
  const saveIdentity = btn('Save', { kind: 'primary', icon: 'check', onClick: saveCommit });
  const card = Object.assign(h('section.card', loading('Checking GitHub…')), { dirty: () => !!fride && identity.dirty() });

  /** @type {(state: string, kind: string) => HTMLDivElement} */
  const head = (state, kind) => h('div.card-head', h('div.card-title', icon('git'), h('h2', 'GitHub')), pill(state, kind));

  const connect = async () => {
    const r = await post('/api/github/connect', { pull_requests: prs.input.checked, org: org.value.trim() });
    // The hand-off page posts the manifest to GitHub, which then comes
    // back to this dashboard.
    window.location.assign(r.url);
  };

  /** @param {string} repo */
  const test = async (repo) => {
    const r = await post('/api/github/test', { repo }, { timeout: 60000 });
    const what = r.issue ? `#${r.issue.number} “${r.issue.title}”` : 'that it has no issue';
    toast(`Read ${what} of ${r.repo} with a token that may only read issues, then revoked it`, 'ok', { timeout: 8000 });
  };

  /** @param {{app: GitHubAppInfo}} st */
  const disconnect = async (st) => {
    const ok = await confirmDialog({
      title: 'Disconnect GitHub?',
      message: `Fridolin forgets the key of ${st.app.name || st.app.slug}, and runs get no GitHub tokens from it. The app stays on GitHub until you delete it there; the next page says where.`,
      confirm: 'Disconnect', danger: true,
    });
    if (!ok) return;
    const r = await post('/api/github/disconnect');
    deleteURL = r.delete_url || '';
    toast('GitHub disconnected', 'ok');
    await refresh();
  };

  ctx.follow(github, (st) => render(st || {}), 30000);
  ctx.follow(identities, (d) => {
    row = ((d && d.identities) || []).find((i) => i.id === 'github') || null;
    renderFride();
  }, 30000);

  /** @param {Partial<GitHubStatus>} st */
  function render(st) {
    const { app } = st; // the bridge names the app whenever it is connected
    fride = st.connected && app ? 'app' : st.typed_token ? 'typed' : '';
    paint(card, 'ready', () => [mainBox, fridePart]);
    renderFride();
    if (!st.connected || !app) {
      paint(mainBox, { error: st.error || '', typed: !!st.typed_token, deleteURL }, (s) => [
        head('Not connected', 'neutral'),
        h('p', 'Connect GitHub once: Fridolin makes a private GitHub App of its own, with no webhook, and you pick the repositories it may use. Each run that needs GitHub — a scheduled review that files issues, say — gets a token that lasts at most an hour, only for its repositories and permissions, revoked when the run ends.'),
        s.error ? callout('bad', 'The GitHub app’s key cannot be read', s.error) : null,
        s.deleteURL ? callout('info', 'The app is still on GitHub', h('div', 'Delete it there if you no longer use it: ', external(s.deleteURL, 'the app’s settings → Delete GitHub App'), '.')) : null,
        s.typed ? callout('info', 'A typed token is set', h('div', 'Runs get ', mono('FRIDOLIN_GITHUB_TOKEN'), ' (', mono('fridolin.github.token'), ') while no app is connected. It cannot be narrowed to one use or revoked after it: an app is the better choice.')) : null,
        connectForm,
        h('div.actions', btn('Connect GitHub', { kind: 'primary', icon: 'external', onClick: connect })),
        h('p.muted.small', 'GitHub shows the app before it is made, and asks for the repositories next. Its private key goes to the core’s secrets and is never shown again.'),
      ]);
      return;
    }
    deleteURL = '';
    const insts = st.installations || [];
    const repos = insts.flatMap((i) => i.repos || []);
    paint(mainBox, { ...st, app }, (s) => [
      head(insts.length ? 'Connected' : 'Not installed yet', insts.length ? 'ok' : 'warn'),
      h('p', 'App ', external(s.app.html_url || s.settings_url || '', s.app.name || s.app.slug), s.app.owner ? ` on ${s.app.owner}` : '', '. Its key is kept in ', s.where, '.'),
      s.error ? callout('bad', 'GitHub did not answer as expected', s.error) : null,
      insts.length ? null : callout('warn', 'Pick its repositories', 'The app is made but not installed anywhere yet: install it on the repositories Fridolin may use.'),
      insts.length ? h('div.facts', insts.map((i) => h('div',
        h('strong', i.account), i.suspended ? ' (suspended)' : '', ': ',
        i.repository_selection === 'all' ? 'all repositories' : (i.repos || []).length ? (i.repos || []).map((r, n) => [n ? ', ' : '', mono(r)]) : 'no repository',
        ' · ', external(i.html_url, 'Manage on GitHub')))) : null,
      h('div.actions',
        linkButton(s.install_url, insts.length ? 'Install on more repositories' : 'Install the app', insts.length ? '' : 'primary'),
        repos.length ? btn('Test', { icon: 'check', title: `Mint a token that may only read the issues of ${repos[0]}, read one, revoke it`, onClick: () => test(repos[0]) }) : null,
        linkButton(s.settings_url, 'App settings'),
        btn('Disconnect', { kind: 'danger', icon: 'x', small: true, onClick: () => disconnect(s) })),
    ]);
  }

  // ── the commit identity for FrIDE ───────────────────────────────────

  function renderFride() {
    if (row) identity.set({ name: row.name || '', email: row.email || '' });
    paint(fridePart, !!fride, (on) => (on ? [h('h3', 'Commit identity for FrIDE'), notesBox, identity.el, h('div.actions', saveIdentity)] : null));
    paint(notesBox, { fride, note: (row && row.note) || '' }, (s) => [
      s.note ? callout('warn', 'FrIDE gets no GitHub token', s.note) : null,
      s.fride === 'app'
        ? h('p.muted.small', 'The FrIDEs of your nodes get the app’s installation token. It lasts an hour, and the node renews it before it ends. Pushing needs the app’s Contents permission set to Read and write.')
        : h('p.muted.small', 'The FrIDEs of your nodes get the typed token, ', mono('FRIDOLIN_GITHUB_TOKEN'), ', while no app is connected.'),
    ]);
  }

  /** @param {{name: string, email: string}} d */
  function buildIdentity({ name, email }) {
    const nameIn = textInput({ value: name, placeholder: 'Your Name' });
    const emailIn = textInput({ type: 'email', value: email, placeholder: 'you@example.com' });
    const value = () => ({ name: nameIn.value.trim(), email: emailIn.value.trim() });
    const el = h('div.settings-fields',
      field({ label: 'Commit name', tag: 'optional', control: nameIn, help: 'What FrIDE’s commits on GitHub carry.' }),
      field({ label: 'Commit email', tag: 'optional', control: emailIn, help: 'Both or neither. Empty: git’s own name and email count.' }));
    return { el, value };
  }

  async function saveCommit() {
    const f = identity.form();
    if (!f) return;
    const v = f.value();
    if (Boolean(v.name) !== Boolean(v.email)) {
      toast('Give the commit name and email together, or neither', 'warn');
      return;
    }
    await put('/api/github/identity', v);
    identity.saved();
    toast(v.name ? 'Commit identity for FrIDE saved' : 'Commit identity removed: git’s own counts', 'ok');
    await identities.refresh();
  }

  return card;
}
