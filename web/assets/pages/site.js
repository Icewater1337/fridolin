// Site: your own website (docs/SITE.md). Link a domain through a Cloudflare
// tunnel, choose which brains each role sees on the built-in Files and Wiki,
// manage who may sign in, and, optionally, link the repository that holds
// more modules and approve what goes live. Everything here stays on this
// machine; the site itself only reads the result.

import { post, put, del } from '../app/api.js';
import { resource } from '../app/resources.js';
import { store } from '../app/store.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { brainLabel } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast, toastError } from '../components/toast.js';
import { btn, copyButton } from '../components/buttons.js';
import { confirmDialog, openDialog } from '../components/dialog.js';
import { field, toggle, select, textInput } from '../components/fields.js';
import { pill, callout, mono, pageHeader, loading, timeAgo, kv } from '../components/common.js';
import { liveForm } from '../components/live-form.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Child } from '../lib/dom.js' */
/** @import { BrainRuntime, ModuleChange, ProposalSnapshot, SiteDeploy, SiteDeploySnapshot, SiteModuleSecrets, SiteProcStatus, SiteStatus, SiteUser } from '../../types/api.js' */

/**
 * What Check for changes finds: POST /api/site/fetch (site.SiteUpdates).
 * @typedef {object} SiteUpdates
 * @property {string} head
 * @property {string} approved
 * @property {{commit: string, subject: string, author: string, at: string}[]} commits
 * @property {ModuleChange[]} changes
 */

/**
 * A brain's switch under Builders.
 * @typedef {{id: string, name: string, label: string, on: boolean}} BuilderChoice
 */

// What some requests run on the bridge has limits of its own: 3 minutes for
// a git or gh call (SiteManager.git), 10 for an npm install. They wait
// longer than that, so the bridge's own error shows, not "did not answer".
const GIT_TIMEOUT_MS = 200000; // one git or gh call
const STEPS_TIMEOUT_MS = 30 * 60000; // several of them in a row, or an npm install

// How fresh the page keeps what GET /api/site does not hold: the builders,
// the proposals, the live commit's secrets and the accounts. The status
// says when they changed, and the page asks for them again then.
const SLOW_MS = 60000;

// What of the status those depend on, as the page saw it last (on this
// visit or an earlier one).
/** @type {{builders: string, proposals: string, secrets: string, users: string} | null} */
let seenDeps = null;

const short = (/** @type {string} */ c) => (c ? c.slice(0, 12) : '');
/**
 * @param {string} state a SiteProcStatus state, or ''
 * @param {boolean} [healthy]
 */
const procPill = (state, healthy) => {
  if (state === 'running') return healthy === false ? pill('Starting…', 'warn', { pulse: true }) : pill('Running', 'ok');
  if (state === 'starting') return pill('Starting…', 'warn', { pulse: true });
  if (state === 'crashed') return pill('Crashed — restarting', 'bad');
  return pill('Off', 'neutral');
};

// A part of a card that paint() fills on its own. It lays out its children
// as the card's own (display: contents), so the card's spacing holds.
const region = (/** @type {Child[]} */ ...children) => h('div.region', children);

/**
 * A process's recent output, folded: built once, so it stays open while
 * new lines come in. set(lines) changes only the text, and hides it while
 * there are none.
 */
function logBox() {
  const text = h('pre.mono');
  const el = h('details.site-log', { hidden: true }, h('summary', 'Recent output'), text);
  return Object.assign(el, {
    /** @param {string[] | null | undefined} lines */
    set: (lines) => {
      el.hidden = !(lines && lines.length);
      paint(text, lines || [], (l) => l.join('\n'));
    },
  });
}

export default /** @satisfies {PageModule} */ ({
  title: 'Site',
  render(root, ctx) {
    const site = resource('/api/site');
    const builders = resource('/api/site/builders');
    const proposals = resource('/api/site/proposals');
    const secrets = resource('/api/site/secrets');
    const users = resource('/api/site/users');
    /** @type {SiteStatus | null} */
    let st = null; // the newest GET /api/site
    /** @type {SiteUpdates | null} */
    let updates = null; // what Check for changes found, until it is deployed

    // Resolves once the page shows what the bridge has now. A poll on its
    // way is dropped, so it cannot paint what was there before a save.
    const refresh = () => site.refresh();

    // Each card paints its parts from its own data, so a new log line or
    // deploy step repaints only what shows it: a button is not replaced
    // under the pointer, an open <details> stays open, and the cards with
    // a form keep what is typed into it.
    const statusHead = region();
    const todoBox = region();
    const factsBox = region();
    const stoppedBox = region();
    const statusActions = h('div.actions');
    const siteLog = logBox();

    const repoHead = region();
    const repoTop = region();
    const keyBox = region();
    const repoBottom = region();

    const deployHead = region();
    const jobBox = region();
    const stepsList = h('ol.small');
    const steps = h('details', { hidden: true }, h('summary', 'Steps'), stepsList);
    const updatesBox = region();
    const earlierList = h('ul');
    const earlier = h('details.site-history', { hidden: true }, h('summary', 'Earlier deploys'), earlierList);

    const proposalsBox = region();
    const buildersBox = region();
    const secretsBox = region(loading('Loading the secrets…'));

    const googleBox = region();
    const tunnelGuide = region();
    const tunnelSetup = region();
    const tunnelSwitch = region();
    const tunnelState = region();
    const tunnelError = region();
    const tunnelLog = logBox();
    const checkBox = region();
    const builtinBox = region();
    /** @type {BrainRuntime[]} */
    let brains = []; // the bridge's brains, for the roles' Files and Wiki

    const usersBox = region(loading('Loading the accounts…'));
    const userRows = h('tbody');

    // The cards with something to type in are forms of their own: saving one
    // keeps what is typed into the others.
    const repoForm = liveForm({ build: buildRepoForm });
    const domainForm = liveForm({ build: buildDomainForm });
    // Adding someone changes nothing anyone else edits.
    const addForm = liveForm({ build: buildAddForm, edits: () => null });
    const rolesForm = liveForm({
      build: buildRolesForm,
      staleNote: { title: 'The roles changed elsewhere', text: 'Saving now would overwrite that change.', action: 'Load the new roles' },
    });
    // The Repository form is hidden while a repository is connected. The
    // fields that save with a button of their own (a secret, the tunnel
    // token, a Google client, a new GitHub repository's name) are .draft:
    // empty until you type, and emptied again once saved.
    ctx.dirty = () => [st && st.repo.connected ? null : repoForm, domainForm, addForm, rolesForm].some((f) => f && f.dirty())
      || [...stack.querySelectorAll('.draft')].some((el) => /** @type {HTMLInputElement | HTMLTextAreaElement} */ (el).value !== '');

    const stack = h('div.page-stack', h('section.card', loading('Loading the site…')));
    mount(root,
      pageHeader('Site', 'Your own website: people sign in with Google or a password, and every page is a module in your own repository.'),
      stack);
    let laidOut = false;
    function layout() {
      laidOut = true;
      /** @type {(iconName: string, title: string) => HTMLDivElement} */
      const head = (iconName, title) => h('div.card-head', h('div.card-title', icon(iconName), h('h2', title)));
      mount(stack,
        h('section.card', statusHead, todoBox, factsBox, stoppedBox, statusActions, siteLog),
        h('section.card', head('external', 'Your domain'),
          h('p.small', 'Three steps give the site a domain of yours, reached from the internet through a Cloudflare tunnel that Fridolin runs: no port is opened on this machine. docs/SITE.md, “From zero to your domain”, has the same steps.'),
          h('ol.site-steps',
            h('li', h('h3', 'The domain'), domainForm.el),
            h('li', h('h3', 'A Cloudflare tunnel to it'), tunnelGuide, tunnelSetup, h('div.actions', tunnelSwitch, tunnelState), tunnelError, tunnelLog),
            h('li', h('h3', 'Check that it answers'), checkBox))),
        h('section.card', head('folder', 'Files and Wiki'), builtinBox),
        h('section.card', head('users', 'People'),
          h('p.small', 'Who may sign in to the site, and their role. Admins sign in with Google. The role decides which brains someone sees on Files and Wiki (above), and a module’s ', mono('module.json'), ' which roles may open it.'),
          usersBox, addForm.el,
          h('details.site-block', h('summary', 'Roles'), rolesForm.el), googleBox),
        h('div.site-section', h('h2', 'Modules from your repository'),
          h('p.small.muted', 'Optional: more pages of your own, each a module in a private repository, which goes live only once you approve a commit. Start from examples/site-module in Fridolin’s repository.')),
        h('section.card', repoHead, repoTop, keyBox, repoBottom),
        h('section.card', deployHead, jobBox, updatesBox, earlier),
        h('section.card', head('inbox', 'Proposals'), proposalsBox),
        h('section.card', head('brain', 'Builders'), buildersBox),
        h('section.card', head('key', 'Secrets'),
          h('p.small', 'API keys and passwords a module needs, by the names its ', mono('module.json'), ' lists in ', mono('capabilities.secrets'), '. A value is never shown again. Only the module that declares a secret gets it, through ', mono('ctx.secret(name)'), ', when the site starts; saving one restarts the site.'),
          secretsBox));
    }

    /** @param {SiteStatus} st the newest GET /api/site */
    function paintAll(st) {
      if (!laidOut) layout();
      paintStatus(st);
      paintRepo(st);
      paintDeploy(st);
      paintProposals(st);
      paintBuilders(st);
      paintDomain(st);
      paintBuiltin(st);
      // The status names what the other requests answer about; when that
      // changed, they are asked again.
      const s = st.settings;
      const deps = {
        builders: JSON.stringify([s.builders, s.proposals, st.repo.connected]),
        // A proposal's changes are against the live commit, so a deploy or
        // a rollback changes what to review in the others.
        proposals: JSON.stringify([s.builders, s.proposals, st.repo.connected, s.approved]),
        secrets: s.approved,
        users: JSON.stringify(st.accounts),
      };
      if (seenDeps) {
        if (deps.builders !== seenDeps.builders) builders.refresh();
        if (deps.proposals !== seenDeps.proposals) proposals.refresh();
        if (deps.secrets !== seenDeps.secrets) secrets.refresh();
        if (deps.users !== seenDeps.users) users.refresh();
      }
      seenDeps = deps;
    }

    // ── status ─────────────────────────────────────────────────────────
    /** @param {SiteStatus} st the newest GET /api/site */
    function paintStatus(st) {
      const s = st.settings;
      const proc = /** @type {Partial<SiteProcStatus>} */ (st.site.process || {});
      const todo = [];
      if (st.runtime.node_error) todo.push(st.runtime.node_error);
      if (!st.accounts.admins) todo.push('Add yourself as an admin with your Google address (People, below).');
      paint(statusHead, { title: s.title, enabled: s.enabled, state: proc.state || '', healthy: st.site.healthy }, ({ title, enabled, state, healthy }) => h('div.card-head',
        h('div.card-title', icon('external'), h('h2', title)),
        enabled ? procPill(state, healthy) : pill('Off', 'neutral')));
      paint(todoBox, todo, (list) => (list.length ? callout('info', 'Before the site can go on', h('ul', list.map((t) => h('li', t)))) : null));
      const last = /** @type {Partial<SiteDeploy>} */ ((s.history || []).at(-1) || {});
      paint(factsBox, {
        local: st.site.local_url, public: st.public_url || '', approved: s.approved || '', subject: last.subject || '',
        accounts: st.accounts, sandbox: st.site.sandbox || null,
      }, renderFacts);
      paint(stoppedBox, proc.error || '', (error) => (error ? callout('bad', 'The site stopped', error) : null));
      paint(statusActions, { enabled: s.enabled, blocked: !s.enabled && todo.length > 0 }, renderStatusActions);
      siteLog.set(proc.log);
    }

    /**
     * @param {{local: string, public: string, approved: string, subject: string,
     *   accounts: SiteStatus['accounts'], sandbox: SiteStatus['site']['sandbox'] | null}} f
     */
    function renderFacts(f) {
      return kv([
        ['On this Mac', h('a', { href: f.local, target: '_blank', rel: 'noopener noreferrer' }, f.local)],
        ['Public address', f.public ? h('a', { href: f.public, target: '_blank', rel: 'noopener noreferrer' }, f.public) : h('span.muted', 'no domain yet')],
        ['Pages', h('span', 'Files and Wiki', f.approved ? h('span', ', and the modules of commit ', mono(short(f.approved)), ' ', f.subject) : h('span.muted', ' (no modules deployed)'))],
        ['Accounts', `${f.accounts.total} (${f.accounts.admins} admin${f.accounts.admins === 1 ? '' : 's'})`],
        ['Sandbox', f.sandbox ? h('span', pill(f.sandbox.on ? 'On' : 'Off', f.sandbox.on ? 'ok' : 'warn'), ' ', h('span.small.muted', f.sandbox.note)) : null],
      ]);
    }

    /** @param {{enabled: boolean, blocked: boolean}} state */
    function renderStatusActions({ enabled, blocked }) {
      const sw = toggle({
        checked: enabled, label: enabled ? 'The site is on' : 'The site is off', disabled: blocked,
        onChange: async (on, input) => {
          try {
            await put('/api/site/settings', { enabled: on });
            toast(on ? 'Site turned on' : 'Site turned off', 'ok');
            await refresh();
          } catch (e) { input.checked = !on; toastError(e); }
        },
      });
      return [sw, enabled ? btn('Restart', { icon: 'refresh', small: true, onClick: async () => { await post('/api/site/restart'); toast('Restarting the site', 'ok'); await refresh(); } }) : null];
    }

    // ── repository ─────────────────────────────────────────────────────
    /** @param {SiteStatus} st the newest GET /api/site */
    function paintRepo(st) {
      const s = st.settings;
      const connected = !!st.repo.connected;
      paint(repoHead, connected, (c) => h('div.card-head', h('div.card-title', icon('backup'), h('h2', 'Repository')), c ? pill('Connected', 'ok') : pill('Not connected', 'neutral')));
      paint(keyBox, { key: st.deploy_key || '', github: !!st.repo.github, gh: !!st.runtime.gh, connected }, renderDeployKey);
      if (connected) {
        // The clone's folder is there as soon as git starts, before the
        // settings name the repository. Until they do, the form keeps what
        // is typed, so a clone that fails gives it back. Once they do, the
        // repository was connected (here or elsewhere), and the form is
        // built afresh for the next time.
        if (s.repo.url && repoForm.dirty()) repoForm.discard();
        paint(repoTop, { repo: s.repo }, ({ repo }) => kv([['Repository', mono(repo.url)], ['Branch', mono(repo.branch)], ['Folder', repo.subdir ? mono(repo.subdir) : 'the root']]));
        paint(repoBottom, { enabled: s.enabled }, renderDisconnect);
        return;
      }
      paint(repoTop, { gh: !!st.runtime.gh }, renderRepoIntro);
      paint(repoBottom, 'form', () => repoForm.el);
      repoForm.set({ url: s.repo.url || '', branch: s.repo.branch || 'main', subdir: s.repo.subdir || '' });
    }

    // The key has to be on a private repository before Connect can clone
    // it, so until a repository is connected gh adds it to the address
    // typed in the form below.
    /** @param {{key: string, github: boolean, gh: boolean, connected: boolean}} state */
    function renderDeployKey({ key, github, gh, connected }) {
      const keyRow = key
        ? h('div', h('p.small', connected
          ? 'Fridolin’s deploy key for this repository. It needs write access, so Fridolin can push the starter files and, later, approved changes.'
          : 'Fridolin’s deploy key for the repository below. Add it with write access before you connect, so Fridolin can clone a private repository, push the starter files and, later, approved changes.'),
          h('div.site-key', mono(key), copyButton(key)),
          gh && (github || !connected) ? h('div.actions', btn('Add it to GitHub with gh', { small: true, icon: 'key', onClick: () => addKeyWithGH(connected) })) : null)
        : h('div.actions', btn('Create a deploy key', { icon: 'key', onClick: async () => { await post('/api/site/deploy-key'); await refresh(); } }));
      return h('div.site-block', h('h3', 'Deploy key'), keyRow);
    }

    /** @param {boolean} connected */
    async function addKeyWithGH(connected) {
      let body;
      if (!connected) {
        const form = repoForm.form();
        const url = form ? form.value().url.trim() : '';
        if (!url) {
          toast('Enter the repository’s SSH address below first (e.g. git@github.com:you/my-site.git), so gh knows where to add the key.', 'warn');
          if (form) form.focusURL();
          return;
        }
        body = { url };
      }
      const r = await post('/api/site/deploy-key/github', body, { timeout: GIT_TIMEOUT_MS });
      toast(`Deploy key added to ${r.repo} on GitHub`, 'ok');
    }

    /** @param {{enabled: boolean}} state */
    function renderDisconnect({ enabled }) {
      return h('div.actions',
        btn('Disconnect', {
          small: true, icon: 'x', disabled: enabled, title: enabled ? 'Turn the site off first' : undefined,
          onClick: async () => {
            if (!await confirmDialog({ title: 'Disconnect the repository?', message: 'The site’s approved commits are forgotten. Fridolin’s clone is moved aside, not deleted; the repository itself is untouched.', confirm: 'Disconnect', danger: true })) return;
            await del('/api/site/repo');
            updates = null;
            await refresh();
          },
        }));
    }

    /** @param {{gh: boolean}} state */
    function renderRepoIntro({ gh }) {
      const ghName = textInput({ placeholder: 'my-site', class: 'draft' });
      return [
        h('p', 'A private repository holds the site’s modules, one folder each. Fridolin clones it with its own deploy key, and nothing in it reaches the site until you approve a commit.'),
        gh ? h('div.site-block', h('h3', 'No repository yet?'),
          h('div.actions', ghName, btn('Create a private GitHub repository', {
            small: true, onClick: async () => {
              const r = await post('/api/site/github-repo', { name: ghName.value }, { timeout: GIT_TIMEOUT_MS });
              ghName.value = '';
              const form = repoForm.form();
              if (form) form.setURL(r.url);
              toast(`Created ${r.url}`, 'ok');
            },
          }))) : null,
      ];
    }

    /**
     * @param {{url: string, branch: string, subdir: string}} data
     * @param {() => void} changed
     */
    function buildRepoForm({ url, branch, subdir }, changed) {
      const urlIn = textInput({ placeholder: 'git@github.com:you/my-site.git', value: url });
      const branchIn = textInput({ value: branch });
      const subdirIn = textInput({ placeholder: 'leave empty for the root', value: subdir });
      const value = () => ({ url: urlIn.value, branch: branchIn.value, subdir: subdirIn.value });
      const el = h('div.site-form',
        h('div.form-grid',
          field({ label: 'Repository (SSH address)', control: urlIn, wide: true }),
          field({ label: 'Branch', control: branchIn }),
          field({ label: 'Folder inside the repository', control: subdirIn, help: 'Where modules/ lives, if not at the top.' })),
        h('div.actions', btn('Connect', {
          kind: 'primary', icon: 'check', onClick: async () => {
            await put('/api/site/repo', value(), { timeout: GIT_TIMEOUT_MS });
            repoForm.saved();
            toast('Repository connected', 'ok');
            updates = null;
            await refresh();
            await checkUpdates();
          },
        })));
      return { el, value, setURL(/** @type {string} */ u) { urlIn.value = u; changed(); }, focusURL() { urlIn.focus(); } };
    }

    // ── deploys ────────────────────────────────────────────────────────
    async function checkUpdates() {
      updates = await post('/api/site/fetch', undefined, { timeout: GIT_TIMEOUT_MS });
      if (st) paintDeploy(st);
    }

    /** @param {SiteStatus} st the newest GET /api/site */
    function paintDeploy(st) {
      const s = st.settings;
      const job = st.deploy || null;
      const running = !!(job && job.state === 'running');
      paint(deployHead, running, (r) => h('div.card-head', h('div.card-title', icon('play'), h('h2', 'Deploys')),
        r ? pill('Deploying…', 'warn', { pulse: true }) : null));
      if (!st.repo.connected) {
        paint(jobBox, null, () => null);
        paint(updatesBox, 'not connected', () => h('p.muted', 'Connect a repository first.'));
        earlier.hidden = true;
        return;
      }
      // The steps keep their <details>, open or not, while the deploy goes on.
      paint(jobBox, job && { state: job.state, commit: job.commit, step: job.step, error: job.error || '' }, renderJob);
      const log = (job && job.log) || [];
      steps.hidden = !log.length;
      paint(stepsList, log, (l) => l.map((line) => h('li', line)));
      paint(updatesBox, { updates, approved: s.approved || '', branch: s.repo.branch, running }, renderUpdates);
      const done = s.history || [];
      earlier.hidden = !done.length;
      paint(earlierList, { done, approved: s.approved || '', running }, renderEarlier);
    }

    /** @param {Pick<SiteDeploySnapshot, 'state' | 'commit' | 'step' | 'error'> | null} job */
    function renderJob(job) {
      if (!job) return null;
      return callout(job.state === 'error' ? 'bad' : job.state === 'done' ? 'ok' : 'info',
        job.state === 'error' ? `Deploy of ${short(job.commit)} failed` : job.state === 'done' ? `Deployed ${short(job.commit)}` : `Deploying ${short(job.commit)}: ${job.step}`,
        job.error ? h('pre.mono.site-error', job.error) : null,
        steps);
    }

    // "It changes who may open it and where it is served." — from the review items.
    /** @param {ModuleChange[] | null} changes */
    function reviewSentence(changes) {
      const notes = [...new Set((changes || []).flatMap((c) => c.review.map((r) => r.note)))];
      if (!notes.length) return '';
      const list = notes.length === 1 ? notes[0] : `${notes.slice(0, -1).join(', ')} and ${notes.at(-1)}`;
      return `It changes ${list}. Look at the list before approving.`;
    }

    /** @param {ModuleChange} change */
    function reviewList(change) {
      if (!change.review.length) return h('p.small.muted', 'No change to access, server code, files or commands.');
      return h('ul.site-review', change.review.map((r) => h('li',
        h('strong', r.field), ` — ${r.note}: `,
        mono(r.old === null || r.old === undefined ? 'none' : JSON.stringify(r.old)), ' → ',
        mono(r.new === null || r.new === undefined ? 'none' : JSON.stringify(r.new)))));
    }

    const changeBlock = (/** @type {ModuleChange} */ c) => h('div.site-change', h('div', h('strong', c.id), ' ', pill(c.status, c.status === 'removed' ? 'bad' : c.review.length ? 'warn' : 'neutral')), reviewList(c));

    /** @param {{updates: SiteUpdates | null, approved: string, branch: string, running: boolean}} state */
    function renderUpdates({ updates: u, approved, branch, running }) {
      if (!u) return h('div.actions', btn('Check for changes', { icon: 'refresh', onClick: checkUpdates }));
      if (!u.head) {
        return callout('info', 'The repository is empty',
          h('p', 'Add Fridolin’s starter: a welcome page, the files a module needs, and a CLAUDE.md for the site’s conventions. Fridolin commits it and pushes it to the branch.'),
          h('div.actions', btn('Add the starter files', { kind: 'primary', onClick: async () => { await post('/api/site/repo/starter', undefined, { timeout: GIT_TIMEOUT_MS }); toast('Starter pushed', 'ok'); await checkUpdates(); } })));
      }
      if (u.head === approved) {
        return [h('p', 'The live site is the newest commit of ', mono(branch), '.'),
          h('div.actions', btn('Check again', { small: true, icon: 'refresh', onClick: checkUpdates }))];
      }
      return [
        h('h3', `${u.commits.length} new commit${u.commits.length === 1 ? '' : 's'} on ${branch}`),
        h('ul.site-commits', u.commits.map((c) => h('li', mono(short(c.commit)), ' ', c.subject, h('span.muted.small', ` — ${c.author}, `, timeAgo(c.at))))),
        u.changes.length ? h('div.site-block', h('h3', 'Modules that change'), u.changes.map(changeBlock))
          : h('p.small.muted', 'No module changes (only other files).'),
        h('div.actions',
          btn(`Approve and deploy ${short(u.head)}`, {
            kind: 'primary', icon: 'check', disabled: running, onClick: async () => {
              const sentence = reviewSentence(u.changes);
              if (sentence && !await confirmDialog({ title: 'Approve these changes?', message: sentence, confirm: 'Approve and deploy' })) return;
              await post('/api/site/deploy', { commit: u.head });
              updates = null;
              if (st) paintDeploy(st);
              await refresh();
            },
          }),
          btn('Check again', { small: true, icon: 'refresh', onClick: checkUpdates })),
      ];
    }

    /** @param {{done: SiteDeploy[], approved: string, running: boolean}} state */
    function renderEarlier({ done, approved, running }) {
      return [...done].reverse().map((d) => h('li', mono(short(d.commit)), ' ', d.subject, h('span.muted.small', ' — ', timeAgo(d.at)),
        d.commit === approved ? pill('live', 'ok') : btn('Roll back to this', {
          small: true, disabled: running, onClick: async () => {
            if (!await confirmDialog({ title: `Roll back to ${short(d.commit)}?`, message: d.subject, confirm: 'Roll back' })) return;
            await post('/api/site/deploy', { commit: d.commit });
            await refresh();
          },
        })));
    }

    // ── proposals from builder brains ─────────────────────────────────
    /** @param {SiteStatus} st the newest GET /api/site */
    function paintProposals(st) {
      const s = st.settings;
      if (!s.builders.length && !s.proposals.length) {
        paint(proposalsBox, 'no builders', () => h('p.muted', 'Brains you allow to build the site (below) propose their work here. You review it and deploy it — they cannot.'));
        return;
      }
      const known = proposals.data();
      if (!known) { paint(proposalsBox, 'loading', () => loading('Comparing the proposals with the live site…')); return; }
      if (!known.proposals.length) {
        paint(proposalsBox, 'none', () => h('p.muted', 'Nothing waiting. Ask a builder brain for a page — on WhatsApp, or here on the Build page.'));
        return;
      }
      paint(proposalsBox, { list: known.proposals, running: !!(st.deploy && st.deploy.state === 'running'), branch: s.repo.branch }, renderProposals);
    }

    /** @param {{list: ProposalSnapshot[], running: boolean, branch: string}} state */
    function renderProposals({ list, running, branch }) {
      return list.map(({ proposal: p, changes, preview, error }) => h('div.site-block',
        h('div', h('strong', p.title), h('span.muted.small', ` — ${p.brain}, `, timeAgo(p.at), ' · ', mono(short(p.commit)))),
        p.summary ? h('p.small.site-summary', p.summary) : null,
        error ? callout('bad', 'Cannot compare it with the live site', error) : null,
        changes && changes.length ? changes.map(changeBlock) : h('p.small.muted', 'No module changes.'),
        h('div.actions',
          btn('Merge and deploy', {
            kind: 'primary', icon: 'check', disabled: running, onClick: async () => {
              const sentence = reviewSentence(changes);
              if (!await confirmDialog({ title: `Merge “${p.title}”?`, message: `${sentence ? `${sentence} ` : ''}It is merged into ${branch}, pushed and deployed.`, confirm: 'Merge and deploy' })) return;
              await post(`/api/site/proposals/${encodeURIComponent(p.brain)}/merge`, undefined, { timeout: STEPS_TIMEOUT_MS });
              toast('Merged — deploying', 'ok');
              await Promise.all([refresh(), proposals.refresh()]);
            },
          }),
          preview ? h('a.btn.small', { href: preview.local, target: '_blank', rel: 'noopener noreferrer' }, icon('external'), h('span.btn-label', 'Open the preview'))
            : btn('Start a preview', {
              small: true, icon: 'play', onClick: async () => {
                const u = await post(`/api/site/previews/${encodeURIComponent(p.brain)}`, undefined, { timeout: STEPS_TIMEOUT_MS });
                window.open(u.local, '_blank', 'noopener');
                await proposals.refresh();
              },
            }),
          btn('Discard', {
            small: true, icon: 'x', onClick: async () => {
              if (!await confirmDialog({ title: 'Discard this proposal?', message: 'It leaves the list. The brain’s branch and workspace stay as they are.', confirm: 'Discard', danger: true })) return;
              await del(`/api/site/proposals/${encodeURIComponent(p.brain)}`);
              await Promise.all([refresh(), proposals.refresh()]);
            },
          }))));
    }

    // ── which brains may build ─────────────────────────────────────────
    // Setting a builder up can take minutes (npm install), and the list
    // changes meanwhile (a preview starts, a poll comes). Each brain's
    // switch is a node of its own that shows a newer state in place, so
    // it is never replaced under the pointer, and one whose save is on its
    // way stays disabled.
    const builderSwitches = h('div.site-builders');

    /** @param {SiteStatus} st the newest GET /api/site */
    function paintBuilders(st) {
      if (!st.repo.connected) { paint(buildersBox, 'not connected', () => h('p.muted', 'Connect a repository first.')); return; }
      const known = builders.data();
      if (!known) { paint(buildersBox, 'loading', () => loading('Loading the brains…')); return; }
      paint(buildersBox, known.brains.length > 0, renderBuilders);
      reconcile(builderSwitches, known.brains.map((br) => ({ id: br.id, name: br.name || br.id, label: brainLabel(br), on: br.builder })), (b) => b.id, builderSwitch);
    }

    /** @param {boolean} any */
    function renderBuilders(any) {
      return [
        h('p.small', 'A brain you allow gets the site-builder skills and tools, and a workspace of its own (a clone of the repository in its folder, under .fridolin-site). It builds and previews inside its sandbox and can only propose; you merge and deploy here.'),
        any ? builderSwitches : h('p.muted', 'No brains yet.'),
      ];
    }

    /** @param {BuilderChoice} b */
    function builderSwitch(b) {
      let latest = b; // the newest state of this brain
      let busy = false; // its save is on its way
      const el = toggle({
        checked: b.on, label: b.label,
        onChange: async (on, input) => {
          busy = true;
          input.disabled = true;
          try {
            await put(`/api/site/builders/${encodeURIComponent(b.id)}`, { on }, { timeout: STEPS_TIMEOUT_MS });
            toast(on ? `${latest.name} may build the site` : `${latest.name} no longer builds the site`, 'ok');
          } catch (e) { toastError(e); }
          // What the bridge says now: a save that failed during npm install
          // has listed the brain already.
          await Promise.all([refresh(), builders.refresh()]);
          busy = false;
          input.disabled = false;
          show();
        },
      });
      const label = el.querySelector('.switch-label');
      const show = () => { el.input.checked = latest.on; if (label) label.textContent = latest.label; };
      // While its save is on its way, the switch shows what you chose.
      return Object.assign(el, { update: (/** @type {BuilderChoice} */ next) => { latest = next; if (!busy) show(); } });
    }

    // ── secrets the modules declare ───────────────────────────────────
    function paintSecrets() {
      const known = secrets.data();
      if (known) paint(secretsBox, known.modules, renderSecrets);
    }

    /** @param {SiteModuleSecrets[]} modules */
    function renderSecrets(modules) {
      if (!modules.length) return h('p.muted', 'No live module declares a secret.');
      /**
       * @param {SiteModuleSecrets} m
       * @param {string} name
       */
      const row = (m, name) => {
        const isSet = m.set.includes(name);
        const declared = m.declared.includes(name);
        const value = h('input.draft', { type: 'password', autocomplete: 'off', placeholder: isSet ? 'set — enter a new value to replace it' : 'value', 'aria-label': `Value of ${name}` });
        const path = `/api/site/secrets/${encodeURIComponent(m.module)}/${encodeURIComponent(name)}`;
        return h('li',
          mono(name),
          isSet ? pill('set', 'ok') : pill('not set', declared ? 'warn' : 'neutral'),
          declared ? null : pill('not declared', 'neutral', { title: 'No live module.json lists it, so no module gets it.' }),
          declared ? h('span.site-secret-input', value, btn(isSet ? 'Replace' : 'Save', {
            small: true, onClick: async () => { await put(path, { value: value.value }); value.value = ''; toast(`Saved ${name}; restarting the site`, 'ok'); await secrets.refresh(); },
          })) : null,
          isSet ? btn('', {
            small: true, icon: 'trash', title: `Remove ${name}`, onClick: async () => {
              if (!await confirmDialog({ title: `Remove ${name}?`, message: `${m.title} no longer gets it once the site restarts.`, confirm: 'Remove', danger: true })) return;
              await del(path);
              await secrets.refresh();
            },
          }) : null);
      };
      return modules.map((m) => h('div.site-block',
        h('h3', m.title, m.title !== m.module ? h('span.muted.small', ' ', mono(m.module)) : null, m.live ? null : h('span.muted.small', ' — not in the live commit')),
        h('ul.site-secrets', [...new Set([...m.declared, ...m.set])].map((name) => row(m, name)))));
    }

    // ── domain and sign-in ─────────────────────────────────────────────
    /** @param {SiteStatus} st the newest GET /api/site */
    function paintDomain(st) {
      const s = st.settings;
      domainForm.set({ domain: s.domain || '', title: s.title, listen: s.listen });
      paint(googleBox, st.google, renderGoogle);
      const tunnel = /** @type {Partial<SiteProcStatus>} */ (st.tunnel.process || {});
      paint(tunnelGuide, { listen: s.listen, domain: s.domain || '' }, renderTunnelGuide);
      paint(tunnelSetup, { listen: s.listen, saved: !!st.tunnel.token_saved, cloudflared: !!st.runtime.cloudflared }, renderTunnelSetup);
      paint(checkBox, { domain: s.domain || '', on: !!s.enabled }, renderCheck);
      paint(tunnelSwitch, { on: !!s.tunnel, disabled: !st.tunnel.token_saved || !st.runtime.cloudflared }, ({ on, disabled }) => toggle({
        checked: on, label: 'Run the tunnel while the site is on', disabled,
        onChange: async (want, input) => { try { await put('/api/site/settings', { tunnel: want }); await refresh(); } catch (e) { input.checked = !want; toastError(e); } },
      }));
      paint(tunnelState, s.tunnel ? tunnel.state || '' : null, (state) => (state === null ? null : procPill(state)));
      paint(tunnelError, tunnel.error || '', (error) => (error ? callout('bad', 'The tunnel stopped', error) : null));
      tunnelLog.set(tunnel.log);
    }

    /** @param {{domain: string, title: string, listen: string}} data */
    function buildDomainForm({ domain, title, listen }) {
      const domainIn = textInput({ placeholder: 'example.com', value: domain });
      const titleIn = textInput({ value: title });
      const listenIn = textInput({ value: listen });
      const value = () => ({ domain: domainIn.value, title: titleIn.value, listen: listenIn.value });
      const el = h('div.site-form',
        h('div.form-grid',
          field({ label: 'Domain', control: domainIn, help: 'The public host name, e.g. example.com, served over https. Its DNS must be with Cloudflare.' }),
          field({ label: 'Site name', control: titleIn }),
          field({ label: 'Listen on', control: listenIn, help: 'Keep it on 127.0.0.1: the tunnel reaches it from this machine.' })),
        h('div.actions', btn('Save', {
          kind: 'primary', onClick: async () => {
            await put('/api/site/settings', value());
            domainForm.saved();
            toast('Saved', 'ok');
            await refresh();
          },
        })));
      return { el, value };
    }

    // Saving or removing the site's own client restarts the site (site_api.go).
    const restarting = (/** @type {string} */ what) => (st && st.settings.enabled ? `${what}; restarting the site` : what);

    /** @param {SiteStatus['google']} g */
    function renderGoogle(g) {
      const clientNote = g.source === 'none'
        ? callout('warn', 'Google sign-in is off', 'Add a Google OAuth client — under Settings → Google sign-in for all of Fridolin, or a separate one for the site below.')
        : g.type !== 'web'
          ? callout('warn', 'Your Google client is a Desktop app client', 'Google lets a website sign people in only with a “Web application” client. Create one in Google Cloud Console and paste it below.')
          : callout('ok', g.source === 'site' ? 'Using the site’s own Google client' : 'Using Fridolin’s Google client',
            h('div', 'In Google Cloud Console, add this to the client’s ', h('em', 'Authorized redirect URIs'), ': ', mono(g.redirect_uri, { copy: true })));
      const clientJSON = h('textarea.mono.draft', { rows: 3, placeholder: '{"web": {"client_id": "…", "client_secret": "…"}}' });
      return h('div.site-block', h('h3', 'Google sign-in'), clientNote,
        h('details', h('summary', g.source === 'site' ? 'Replace or remove the site’s Google client' : 'Use a separate Google client for the site'),
          field({ label: 'Client JSON (as downloaded from Google Cloud Console)', control: clientJSON, wide: true }),
          h('div.actions',
            btn('Save client', { small: true, onClick: async () => { await put('/api/site/google-client', { json: clientJSON.value }); clientJSON.value = ''; toast(restarting('Google client saved'), 'ok'); await refresh(); } }),
            g.source === 'site' ? btn('Remove', { small: true, onClick: async () => { await del('/api/site/google-client'); toast(restarting('Google client removed'), 'ok'); await refresh(); } }) : null)));
    }

    /** @param {{listen: string, domain: string}} state */
    function renderTunnelGuide({ listen, domain }) {
      /** @type {(href: string, text: string) => HTMLAnchorElement} */
      const ext = (href, text) => h('a', { href, target: '_blank', rel: 'noopener noreferrer' }, text);
      const name = domain || 'example.com';
      return h('ol.small.site-guide',
        h('li', 'The domain’s DNS must be with Cloudflare: if it is not yet, ', ext('https://dash.cloudflare.com/', 'add it to Cloudflare'), ' and change its name servers where you bought it.'),
        h('li', 'Open ', ext('https://one.dash.cloudflare.com/', 'Cloudflare Zero Trust'), ' → Networks → Tunnels → ', h('em', 'Create a tunnel'), ', type ', h('em', 'Cloudflared'), '. Name it, e.g. fridolin.'),
        h('li', 'On ', h('em', 'Install and run a connector'), ', copy the token: the long text after ', mono('--token'), ' in any of the commands shown. Install nothing: Fridolin runs the tunnel. Paste the token below.'),
        h('li', 'Next, add a public host name: ', mono(name), ', no path, service ', h('em', 'HTTP'), ', URL ', mono(listen, { copy: true }), '. Cloudflare adds the DNS record. Optionally add ', mono(`wiki.${name}`), ' the same way: it leads to ', mono(`${name}/wiki`), '.'),
        h('li', 'Turn on ', h('em', 'Run the tunnel'), ' below, and the site (top of this page).'));
    }

    /** @param {{domain: string, on: boolean}} state */
    function renderCheck({ domain, on }) {
      const out = region();
      return [
        h('div.actions', btn(domain ? `Check https://${domain}` : 'Check', {
          small: true, icon: 'refresh', disabled: !domain,
          onClick: async () => {
            const r = await post('/api/site/check-domain', undefined, { timeout: 45000 });
            paint(out, r, (x) => (x.ok
              ? callout('ok', `${domain} answers`, h('div', 'The site is live: ', h('a', { href: `https://${domain}/`, target: '_blank', rel: 'noopener noreferrer' }, `https://${domain}/`)))
              : callout('bad', `${domain} does not answer yet`, x.error)));
          },
        }), on ? null : h('span.small.muted', 'Turn the site on first.')),
        out,
      ];
    }

    // ── Files and Wiki ─────────────────────────────────────────────────
    ctx.watch(store, (x) => x.status, (status) => {
      brains = (status && status.brains) || [];
      if (st) paintBuiltin(st);
    });

    /** @param {SiteStatus} st the newest GET /api/site */
    function paintBuiltin(st) {
      const s = st.settings;
      paint(builtinBox, {
        base: st.public_url ? st.public_url.replace(/\/$/, '') : (st.site.local_url || '').replace(/\/$/, ''),
        roles: s.roles, map: s.role_brains || {}, uploads: s.role_uploads || {}, brains: brains.map((b) => ({ id: b.id, label: brainLabel(b) })),
      }, renderBuiltin);
    }

    /** @param {{base: string, roles: string[], map: Record<string, string[]>, uploads: Record<string, string[]>, brains: {id: string, label: string}[]}} state */
    function renderBuiltin({ base, roles, map, uploads, brains: list }) {
      // One column of toggles: the brains a role sees (key "brains"), or
      // those it may upload to ("uploads"), of those it sees.
      /**
       * @param {string} role
       * @param {'brains' | 'uploads'} key
       * @param {{id: string, label: string}[]} from
       * @param {(want: boolean, label: string) => string} msg
       */
      const picks = (role, key, from, msg) => {
        const chosen = new Set((key === 'brains' ? map : uploads)[role] || []);
        return h('div.site-brain-picks', from.map((b) => toggle({
          checked: chosen.has(b.id), label: b.label,
          onChange: async (want, input) => {
            const next = list.map((x) => x.id).filter((id) => (id === b.id ? want : chosen.has(id)));
            try {
              await put('/api/site/roles', { [key]: { [role]: next } });
              toast(msg(want, b.label), 'ok');
              await refresh();
            } catch (e) { input.checked = !want; toastError(e); }
          },
        })));
      };
      /** @param {string} role */
      const roleRow = (role) => {
        const sees = list.filter((b) => (map[role] || []).includes(b.id));
        return h('tr', h('td', h('strong', role)),
          h('td', picks(role, 'brains', list, (want, label) => (want ? `${role} sees ${label}` : `${role} no longer sees ${label}`))),
          h('td', sees.length ? picks(role, 'uploads', sees, (want, label) => (want ? `${role} may upload to ${label}` : `${role} no longer uploads to ${label}`)) : h('span.muted.small', '—')));
      };
      const others = roles.filter((r) => r !== 'admin');
      return [
        h('p.small', 'Every Fridolin’s site has two pages of its own: ',
          base ? h('a', { href: `${base}/files`, target: '_blank', rel: 'noopener noreferrer' }, 'Files') : 'Files', ', the brains’ folders to browse and open, and ',
          base ? h('a', { href: `${base}/wiki`, target: '_blank', rel: 'noopener noreferrer' }, 'Wiki') : 'Wiki',
          ', their wiki/ and memories/ pages. Someone who signs in sees the brains their role is given here; admins see every brain, and edit, upload, rename and delete there as you do here. A role may upload to a brain it sees, into the folders the brain takes uploads in: choose those on the Files page, in each folder. What a brain’s backup leaves out, secrets, chat histories and Fridolin’s own state are never shown, and every view and change is written to the audit log.'),
        !list.length ? callout('info', 'No brains yet', 'Add a brain first; its folder and wiki then show here.')
          : others.length ? h('div.table-wrap', h('table.table', h('thead', h('tr', h('th', 'Role'), h('th', 'Brains it sees'), h('th', 'May upload to'))), h('tbody', others.map(roleRow))))
            : callout('info', 'Only admins so far', 'Add a role under People → Roles to share brains with others.'),
      ];
    }

    /** @param {{saved: boolean, cloudflared: boolean}} state */
    function renderTunnelSetup({ saved, cloudflared }) {
      const token = h('input.draft', { type: 'password', autocomplete: 'off', placeholder: saved ? 'saved — paste a new one to replace it' : 'eyJ…' });
      return [
        !cloudflared ? callout('info', 'cloudflared is not installed', h('div', 'macOS: ', mono('brew install cloudflared', { copy: true }))) : null,
        h('div.actions', token, btn(saved ? 'Replace token' : 'Save token', { small: true, onClick: async () => { await put('/api/site/tunnel', { token: token.value }); token.value = ''; toast('Tunnel token saved', 'ok'); await refresh(); } }),
          saved ? btn('Remove token', { small: true, onClick: async () => { await put('/api/site/tunnel', { token: '' }); await refresh(); } }) : null),
      ];
    }

    // ── people ─────────────────────────────────────────────────────────
    function paintPeople() {
      const known = users.data();
      if (!known) return;
      const { users: list, roles } = known;
      paint(usersBox, list.length > 0, (any) => (any
        ? h('div.table-wrap', h('table.table', h('thead', h('tr', h('th', 'Account'), h('th', 'Role'), h('th.right', ''))), userRows))
        : callout('info', 'No accounts yet', 'Start with yourself: your Google address, role admin.')));
      // A row is rebuilt only when its account (or the roles) changed.
      reconcile(userRows, list.map((u) => ({ u, roles })), (r) => r.u.id, userRow);
      addForm.set({ roles });
      rolesForm.set({ roles });
    }

    /**
     * @param {string[]} roles
     * @param {string} value
     * @param {SiteUser['kind']} kind
     */
    const roleSelect = (roles, value, kind) => select(roles.filter((r) => kind === 'google' || r !== 'admin'), value);
    const who = (/** @type {SiteUser} */ u) => u.name || u.email || u.username;

    /** @param {{u: SiteUser, roles: string[]}} row */
    function userRow({ u, roles }) {
      const role = roleSelect(roles, u.role, u.kind);
      role.addEventListener('change', async () => {
        try {
          await put(`/api/site/users/${encodeURIComponent(u.id)}`, { role: role.value });
          toast('Role changed', 'ok');
          await Promise.all([users.refresh(), refresh()]);
        } catch (e) { role.value = u.role; toastError(e); }
      });
      return h('tr',
        h('td', h('strong', who(u)), h('div.small.muted', u.kind === 'google' ? `Google: ${u.email}` : `Password: ${u.username}`)),
        h('td', role),
        h('td.right',
          u.kind === 'password' ? btn('New password', { small: true, onClick: () => newPassword(u) }) : null,
          btn('', {
            small: true, icon: 'trash', title: 'Remove', onClick: async () => {
              if (!await confirmDialog({ title: `Remove ${who(u)}?`, message: 'They are signed out at once and can no longer sign in.', confirm: 'Remove', danger: true })) return;
              await del(`/api/site/users/${encodeURIComponent(u.id)}`);
              await Promise.all([users.refresh(), refresh()]);
            },
          })));
    }

    /** @param {{roles: string[]}} data */
    function buildAddForm({ roles }) {
      const kind = select([['google', 'Google account'], ['password', 'Username and password']], 'google');
      const ident = textInput({ placeholder: 'name@example.com' });
      const name = textInput({ placeholder: 'optional' });
      const role = roleSelect(roles, roles.includes('member') ? 'member' : roles[1] || 'admin', 'google');
      const pw = h('input', { type: 'password', autocomplete: 'new-password', placeholder: 'at least 10 characters' });
      const pwField = field({ label: 'Password', control: pw });
      pwField.hidden = true;
      const identField = field({ label: 'Google address', control: ident });
      kind.addEventListener('change', () => {
        const isPw = kind.value === 'password';
        pwField.hidden = !isPw;
        const identLabel = identField.querySelector('label'); // field() makes one, as it has a label
        if (identLabel) identLabel.textContent = isPw ? 'Username' : 'Google address';
        ident.placeholder = isPw ? 'sam' : 'name@example.com';
        const keep = role.value;
        mount(role, ...roles.filter((r) => !isPw || r !== 'admin').map((r) => h('option', { value: r }, r)));
        role.value = isPw && keep === 'admin' ? (roles.find((r) => r !== 'admin') || '') : keep;
      });
      const value = () => {
        const isPw = kind.value === 'password';
        return { kind: kind.value, email: isPw ? '' : ident.value, username: isPw ? ident.value : '', name: name.value, role: role.value, password: isPw ? pw.value : '' };
      };
      const el = h('div.site-block', h('h3', 'Add someone'),
        h('div.form-grid', field({ label: 'Sign-in', control: kind }), identField, field({ label: 'Name', control: name }), field({ label: 'Role', control: role }), pwField),
        h('div.actions', btn('Add', {
          kind: 'primary', icon: 'plus', onClick: async () => {
            await post('/api/site/users', value());
            toast('Added', 'ok');
            addForm.discard(); // empty again, for the next one
            await Promise.all([users.refresh(), refresh()]);
          },
        })));
      return { el, value };
    }

    /** @param {{roles: string[]}} data */
    function buildRolesForm({ roles }) {
      const rolesIn = textInput({ value: roles.filter((r) => r !== 'admin').join(', ') });
      const value = () => rolesIn.value;
      const el = h('div.site-form',
        field({ label: 'Roles besides admin, separated by commas', control: rolesIn }),
        h('div.actions', btn('Save roles', {
          small: true, onClick: async () => {
            await put('/api/site/roles', { roles: rolesIn.value.split(',') });
            rolesForm.saved();
            toast('Roles saved', 'ok');
            await users.refresh();
          },
        })));
      return { el, value };
    }

    /** @param {SiteUser} u */
    async function newPassword(u) {
      const pw = h('input', { type: 'password', autocomplete: 'new-password', placeholder: 'at least 10 characters' });
      await openDialog({
        title: `New password for ${u.username}`,
        body: field({ label: 'Password', control: pw }),
        actions: [{ label: 'Cancel', value: null }, {
          label: 'Set password', kind: 'primary',
          onClick: async () => { await put(`/api/site/users/${encodeURIComponent(u.id)}`, { password: pw.value }); toast('Password changed', 'ok'); return true; },
        }],
      });
    }

    // Followed last: what is known from an earlier visit paints at once,
    // with everything above defined. The status comes every 1.5 s while a
    // deploy runs, to show its steps, and every 5 s otherwise.
    ctx.follow(site, (data) => { st = data; paintAll(data); }, () => (st && st.deploy && st.deploy.state === 'running' ? 1500 : 5000));
    ctx.follow(builders, () => { if (st) paintBuilders(st); }, SLOW_MS);
    ctx.follow(proposals, () => { if (st) paintProposals(st); }, SLOW_MS);
    ctx.follow(secrets, paintSecrets, SLOW_MS);
    ctx.follow(users, paintPeople, SLOW_MS);
  },
});
