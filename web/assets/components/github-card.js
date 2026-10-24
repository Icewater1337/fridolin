// The GitHub card (docs/GITHUB.md): Connect GitHub makes a GitHub App of
// this Fridolin's own through GitHub's manifest flow, then GitHub asks
// which repositories it may use. Runs get short-lived tokens of it, each
// for the repositories and permissions of one use. Its key never comes
// back to the dashboard.

import { post } from '../app/api.js';
import { resource } from '../app/resources.js';
import { h, paint } from '../lib/dom.js';
import { icon } from './icons.js';
import { toast } from './toast.js';
import { btn } from './buttons.js';
import { confirmDialog } from './dialog.js';
import { field, toggle, textInput } from './fields.js';
import { pill, callout, mono, loading } from './common.js';

/** @import { Page } from '../app/page.js' */
/** @import { GitHubStatus } from '../../types/api.js' */

/** @type {(href: string, text: string) => HTMLAnchorElement} */
const external = (href, text) => h('a', { href, target: '_blank', rel: 'noopener noreferrer' }, text);
/** @type {(href: string | undefined, label: string, kind?: string) => HTMLAnchorElement} */
const linkButton = (href, label, kind = '') => h(`a.btn${kind ? `.${kind}` : ''}`, { href, target: '_blank', rel: 'noopener noreferrer' }, icon('external'), h('span.btn-label', label));

/**
 * githubCard(ctx): the card, following /api/github for the page.
 * @param {Page} ctx
 */
export function githubCard(ctx) {
  const card = h('section.card', loading('Checking GitHub…'));
  const github = resource('/api/github');
  const refresh = () => github.refresh();
  // Where the app is deleted on GitHub, shown once it is disconnected here.
  let deleteURL = '';

  // The connect form is built once, so what is typed survives a refresh.
  const prs = toggle({ label: 'Let FrIDE open pull requests (pull_requests: write)' });
  const org = textInput({ placeholder: 'your own account' });
  const connectForm = h('div.settings-fields',
    field({ control: prs, help: 'Off: the app may read code and write issues. FrIDE’s pushes and pull requests need this on; you can change the app’s permissions on GitHub later too.' }),
    field({ label: 'Organization that owns the app', control: org, help: 'Leave empty to make the app on your own account.' }));

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

  /** @param {Partial<GitHubStatus>} st */
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

  /** @param {Partial<GitHubStatus>} st */
  function render(st) {
    if (!st.connected) {
      paint(card, { error: st.error || '', typed: !!st.typed_token, deleteURL }, (s) => [
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
    paint(card, st, (s) => [
      head(insts.length ? 'Connected' : 'Not installed yet', insts.length ? 'ok' : 'warn'),
      h('p', 'App ', external(s.app.html_url || s.settings_url, s.app.name || s.app.slug), s.app.owner ? ` on ${s.app.owner}` : '', '. Its key is kept in ', s.where, '.'),
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

  return card;
}
