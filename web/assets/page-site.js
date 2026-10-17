// Site: your own website (docs/SITE.md). Link the repository that holds its
// modules, approve what goes live, set the domain and sign-in, and manage who
// may sign in. Everything here stays on this machine; the site itself only
// reads the result.

import { get, post, put, del } from './api.js';
import {
  h, mount, icon, pill, btn, callout, mono, field, toggle, select, pageHeader, textInput,
  confirmDialog, toast, loading, timeAgo, kv, copyButton, openDialog,
} from './ui.js';

const short = (c) => (c ? c.slice(0, 12) : '');
const procPill = (p, healthy) => {
  if (!p) return pill('Off', 'neutral');
  if (p.state === 'running') return healthy === false ? pill('Starting…', 'warn', { pulse: true }) : pill('Running', 'ok');
  if (p.state === 'starting') return pill('Starting…', 'warn', { pulse: true });
  if (p.state === 'crashed') return pill('Crashed — restarting', 'bad');
  return pill('Off', 'neutral');
};

function logBox(lines) {
  if (!lines || !lines.length) return null;
  return h('details.site-log', h('summary', 'Recent output'), h('pre.mono', lines.join('\n')));
}

export default {
  title: 'Site',
  render(root, ctx) {
    const statusCard = h('section.card', loading('Loading the site…'));
    const repoCard = h('section.card');
    const deployCard = h('section.card');
    const proposalsCard = h('section.card');
    const buildersCard = h('section.card');
    const domainCard = h('section.card');
    const peopleCard = h('section.card');
    mount(root,
      pageHeader('Site', 'Your own website: people sign in with Google or a password, and every page is a module in your own repository.'),
      h('div.page-stack', statusCard, repoCard, deployCard, proposalsCard, buildersCard, domainCard, peopleCard));

    let st = null;
    let updates = null;
    let editing = false;
    ctx.dirty = () => editing;
    const markEditing = (el) => { el.addEventListener('input', () => { editing = true; }); return el; };

    const refresh = async () => { st = await get('/api/site'); renderAll(); };
    // Redraw a card only when what it shows changed, so a button is never
    // replaced under the pointer.
    let statusSig = '';
    let deploySig = '';
    let proposalSig = '';
    const sig = (...v) => JSON.stringify(v);
    const poller = ctx.poll(async () => {
      st = await get('/api/site');
      const s1 = sig(st.settings, st.site, st.accounts, st.runtime, st.public_url);
      if (s1 !== statusSig) renderStatus();
      const s2 = sig(st.deploy, st.settings.approved, st.settings.history, st.repo);
      if (!editing && s2 !== deploySig) renderDeploy();
      if (!editing && sig(st.settings.proposals, st.settings.builders) !== proposalSig) { renderProposals(); renderBuilders(); }
    }, () => (st && st.deploy && st.deploy.state === 'running' ? 1500 : 5000));

    get('/api/site').then((s) => { st = s; renderAll(); }).catch((e) => mount(statusCard, callout('bad', 'Could not load the site', e.message)));

    function renderAll() {
      editing = false;
      renderStatus(); renderRepo(); renderDeploy(); renderProposals(); renderBuilders(); renderDomain(); renderPeople();
    }

    // ── status ─────────────────────────────────────────────────────────
    function renderStatus() {
      statusSig = sig(st.settings, st.site, st.accounts, st.runtime, st.public_url);
      const s = st.settings;
      const history = s.history || [];
      const site = st.site;
      const todo = [];
      if (st.runtime.node_error) todo.push(st.runtime.node_error);
      if (!st.repo.connected) todo.push('Connect the repository that holds the site (below).');
      else if (!s.approved) todo.push('Deploy a commit of the repository (Deploys, below).');
      if (!st.accounts.admins) todo.push('Add yourself as an admin with your Google address (People, below).');
      const sw = toggle({
        checked: s.enabled, label: s.enabled ? 'The site is on' : 'The site is off',
        disabled: !s.enabled && todo.length > 0,
        onChange: async (on, input) => {
          try { await put('/api/site/settings', { enabled: on }); toast(on ? 'Site turned on' : 'Site turned off', 'ok'); poller.kick(); } catch (e) { input.checked = !on; toast(e.message, 'bad'); }
        },
      });
      mount(statusCard,
        h('div.card-head', h('div.card-title', icon('external'), h('h2', s.title)), s.enabled ? procPill(site.process, site.healthy) : pill('Off', 'neutral')),
        todo.length ? callout('info', 'Before the site can go on', h('ul', todo.map((t) => h('li', t)))) : null,
        kv([
          ['On this Mac', h('a', { href: site.local_url, target: '_blank', rel: 'noopener noreferrer' }, site.local_url)],
          ['Public address', st.public_url ? h('a', { href: st.public_url, target: '_blank', rel: 'noopener noreferrer' }, st.public_url) : h('span.muted', 'no domain yet')],
          ['Live commit', s.approved ? h('span', mono(short(s.approved)), ' ', (history.at(-1) || {}).subject || '') : null],
          ['Accounts', `${st.accounts.total} (${st.accounts.admins} admin${st.accounts.admins === 1 ? '' : 's'})`],
        ]),
        site.process.error ? callout('bad', 'The site stopped', site.process.error) : null,
        h('div.actions', sw, s.enabled ? btn('Restart', { icon: 'refresh', small: true, onClick: async () => { await post('/api/site/restart'); toast('Restarting the site', 'ok'); poller.kick(); } }) : null),
        logBox(site.process.log));
    }

    // ── repository ─────────────────────────────────────────────────────
    function deployKeyBlock() {
      const keyRow = st.deploy_key
        ? h('div', h('p.small', 'Fridolin’s deploy key for this repository. It needs write access, so Fridolin can push the starter files and, later, approved changes.'),
          h('div.site-key', mono(st.deploy_key), copyButton(st.deploy_key)),
          st.repo.github && st.runtime.gh ? h('div.actions', btn('Add it to GitHub with gh', { small: true, icon: 'key', onClick: async () => { await post('/api/site/deploy-key/github'); toast('Deploy key added on GitHub', 'ok'); } })) : null)
        : h('div.actions', btn('Create a deploy key', { icon: 'key', onClick: async () => { await post('/api/site/deploy-key'); await refresh(); } }));
      return h('div.site-block', h('h3', 'Deploy key'), keyRow);
    }

    function renderRepo() {
      const s = st.settings;
      const head = h('div.card-head', h('div.card-title', icon('backup'), h('h2', 'Repository')), st.repo.connected ? pill('Connected', 'ok') : pill('Not connected', 'neutral'));
      if (st.repo.connected) {
        mount(repoCard, head,
          kv([['Repository', mono(s.repo.url)], ['Branch', mono(s.repo.branch)], ['Folder', s.repo.subdir ? mono(s.repo.subdir) : 'the root']]),
          deployKeyBlock(),
          h('div.actions',
            btn('Disconnect', {
              small: true, icon: 'x', disabled: s.enabled, title: s.enabled ? 'Turn the site off first' : null,
              onClick: async () => {
                if (!await confirmDialog({ title: 'Disconnect the repository?', message: 'The site’s approved commits are forgotten. Fridolin’s clone is moved aside, not deleted; the repository itself is untouched.', confirm: 'Disconnect', danger: true })) return;
                await del('/api/site/repo'); await refresh();
              },
            })));
        return;
      }
      const url = markEditing(textInput({ placeholder: 'git@github.com:you/my-site.git', value: s.repo.url || '' }));
      const branch = markEditing(textInput({ value: s.repo.branch || 'main' }));
      const subdir = markEditing(textInput({ placeholder: 'leave empty for the root', value: s.repo.subdir || '' }));
      const ghName = textInput({ placeholder: 'my-site' });
      mount(repoCard, head,
        h('p', 'A private repository holds the site’s modules, one folder each. Fridolin clones it with its own deploy key, and nothing in it reaches the site until you approve a commit.'),
        st.runtime.gh ? h('div.site-block', h('h3', 'No repository yet?'),
          h('div.actions', ghName, btn('Create a private GitHub repository', {
            small: true, onClick: async () => {
              const r = await post('/api/site/github-repo', { name: ghName.value });
              url.value = r.url; toast(`Created ${r.url}`, 'ok');
            },
          }))) : null,
        deployKeyBlock(),
        h('div.form-grid',
          field({ label: 'Repository (SSH address)', control: url, wide: true }),
          field({ label: 'Branch', control: branch }),
          field({ label: 'Folder inside the repository', control: subdir, help: 'Where modules/ lives, if not at the top.' })),
        h('div.actions', btn('Connect', {
          kind: 'primary', icon: 'check', onClick: async () => {
            await put('/api/site/repo', { url: url.value, branch: branch.value, subdir: subdir.value });
            toast('Repository connected', 'ok');
            updates = null;
            await refresh();
            await checkUpdates();
          },
        })));
    }

    // ── deploys ────────────────────────────────────────────────────────
    async function checkUpdates() {
      updates = await post('/api/site/fetch');
      renderDeploy();
    }

    // "It changes who may open it and where it is served." — from the review items.
    function reviewSentence(changes) {
      const notes = [...new Set((changes || []).flatMap((c) => c.review.map((r) => r.note)))];
      if (!notes.length) return '';
      const list = notes.length === 1 ? notes[0] : `${notes.slice(0, -1).join(', ')} and ${notes.at(-1)}`;
      return `It changes ${list}. Look at the list before approving.`;
    }

    function reviewList(change) {
      if (!change.review.length) return h('p.small.muted', 'No change to access, server code, files or commands.');
      return h('ul.site-review', change.review.map((r) => h('li',
        h('strong', r.field), ` — ${r.note}: `,
        mono(r.old === null || r.old === undefined ? 'none' : JSON.stringify(r.old)), ' → ',
        mono(r.new === null || r.new === undefined ? 'none' : JSON.stringify(r.new)))));
    }

    function renderDeploy() {
      if (!st) return;
      deploySig = sig(st.deploy, st.settings.approved, st.settings.history, st.repo);
      const s = st.settings;
      const job = st.deploy;
      const head = h('div.card-head', h('div.card-title', icon('play'), h('h2', 'Deploys')),
        job && job.state === 'running' ? pill('Deploying…', 'warn', { pulse: true }) : null);
      if (!st.repo.connected) {
        mount(deployCard, head, h('p.muted', 'Connect a repository first.'));
        return;
      }
      const running = job && job.state === 'running';
      const parts = [head];
      if (job) {
        parts.push(callout(job.state === 'error' ? 'bad' : job.state === 'done' ? 'ok' : 'info',
          job.state === 'error' ? `Deploy of ${short(job.commit)} failed` : job.state === 'done' ? `Deployed ${short(job.commit)}` : `Deploying ${short(job.commit)}: ${job.step}`,
          job.error ? h('pre.mono.site-error', job.error) : null,
          job.log && job.log.length ? h('details', h('summary', 'Steps'), h('ol.small', job.log.map((l) => h('li', l)))) : null));
      }
      if (!updates) {
        parts.push(h('div.actions', btn('Check for changes', { icon: 'refresh', onClick: checkUpdates })));
      } else if (!updates.head) {
        parts.push(callout('info', 'The repository is empty',
          h('p', 'Add Fridolin’s starter: a welcome page, the files a module needs, and a CLAUDE.md for the site’s conventions. Fridolin commits it and pushes it to the branch.'),
          h('div.actions', btn('Add the starter files', { kind: 'primary', onClick: async () => { await post('/api/site/repo/starter'); toast('Starter pushed', 'ok'); await checkUpdates(); } }))));
      } else if (updates.head === s.approved) {
        parts.push(h('p', 'The live site is the newest commit of ', mono(s.repo.branch), '.'),
          h('div.actions', btn('Check again', { small: true, icon: 'refresh', onClick: checkUpdates })));
      } else {
        parts.push(h('h3', `${updates.commits.length} new commit${updates.commits.length === 1 ? '' : 's'} on ${s.repo.branch}`),
          h('ul.site-commits', updates.commits.map((c) => h('li', mono(short(c.commit)), ' ', c.subject, h('span.muted.small', ` — ${c.author}, `, timeAgo(c.at))))),
          updates.changes.length ? h('div.site-block', h('h3', 'Modules that change'),
            updates.changes.map((c) => h('div.site-change', h('div', h('strong', c.id), ' ', pill(c.status, c.status === 'removed' ? 'bad' : c.review.length ? 'warn' : 'neutral')), reviewList(c))))
            : h('p.small.muted', 'No module changes (only other files).'),
          h('div.actions',
            btn(`Approve and deploy ${short(updates.head)}`, {
              kind: 'primary', icon: 'check', disabled: running, onClick: async () => {
                const sentence = reviewSentence(updates.changes);
                if (sentence && !await confirmDialog({ title: 'Approve these changes?', message: sentence, confirm: 'Approve and deploy' })) return;
                await post('/api/site/deploy', { commit: updates.head });
                updates = null;
                poller.kick();
              },
            }),
            btn('Check again', { small: true, icon: 'refresh', onClick: checkUpdates })));
      }
      const history = s.history || [];
      if (history.length) {
        parts.push(h('details.site-history', h('summary', 'Earlier deploys'),
          h('ul', [...history].reverse().map((d) => h('li', mono(short(d.commit)), ' ', d.subject, h('span.muted.small', ' — ', timeAgo(d.at)),
            d.commit === s.approved ? pill('live', 'ok') : btn('Roll back to this', {
              small: true, disabled: running, onClick: async () => {
                if (!await confirmDialog({ title: `Roll back to ${short(d.commit)}?`, message: d.subject, confirm: 'Roll back' })) return;
                await post('/api/site/deploy', { commit: d.commit }); poller.kick();
              },
            }))))));
      }
      mount(deployCard, parts);
    }

    // ── proposals from builder brains ─────────────────────────────────
    async function renderProposals() {
      proposalSig = sig(st.settings.proposals, st.settings.builders);
      const head = h('div.card-head', h('div.card-title', icon('inbox'), h('h2', 'Proposals')));
      if (!st.settings.builders.length && !st.settings.proposals.length) {
        mount(proposalsCard, head, h('p.muted', 'Brains you allow to build the site (below) propose their work here. You review it and deploy it — they cannot.'));
        return;
      }
      const { proposals } = await get('/api/site/proposals');
      if (!proposals.length) {
        mount(proposalsCard, head, h('p.muted', 'Nothing waiting. Ask a builder brain for a page — on WhatsApp, or here on the Build page.'));
        return;
      }
      const running = st.deploy && st.deploy.state === 'running';
      mount(proposalsCard, head, proposals.map(({ proposal: p, changes, preview, error }) => h('div.site-block',
        h('div', h('strong', p.title), h('span.muted.small', ` — ${p.brain}, `, timeAgo(p.at), ' · ', mono(short(p.commit)))),
        p.summary ? h('p.small.site-summary', p.summary) : null,
        error ? callout('bad', 'Cannot compare it with the live site', error) : null,
        changes && changes.length ? changes.map((c) => h('div.site-change', h('div', h('strong', c.id), ' ', pill(c.status, c.status === 'removed' ? 'bad' : c.review.length ? 'warn' : 'neutral')), reviewList(c)))
          : h('p.small.muted', 'No module changes.'),
        h('div.actions',
          btn('Merge and deploy', {
            kind: 'primary', icon: 'check', disabled: running, onClick: async () => {
              const sentence = reviewSentence(changes);
              if (!await confirmDialog({ title: `Merge “${p.title}”?`, message: `${sentence ? `${sentence} ` : ''}It is merged into ${st.settings.repo.branch}, pushed and deployed.`, confirm: 'Merge and deploy' })) return;
              await post(`/api/site/proposals/${encodeURIComponent(p.brain)}/merge`);
              toast('Merged — deploying', 'ok'); await refresh();
            },
          }),
          preview ? h('a.btn.small', { href: preview.local, target: '_blank', rel: 'noopener noreferrer' }, icon('external'), h('span.btn-label', 'Open the preview'))
            : btn('Start a preview', { small: true, icon: 'play', onClick: async () => { const u = await post(`/api/site/previews/${encodeURIComponent(p.brain)}`); window.open(u.local, '_blank', 'noopener'); renderProposals(); } }),
          btn('Discard', {
            small: true, icon: 'x', onClick: async () => {
              if (!await confirmDialog({ title: 'Discard this proposal?', message: 'It leaves the list. The brain’s branch and workspace stay as they are.', confirm: 'Discard', danger: true })) return;
              await del(`/api/site/proposals/${encodeURIComponent(p.brain)}`); await refresh();
            },
          })))));
    }

    // ── which brains may build ─────────────────────────────────────────
    async function renderBuilders() {
      const head = h('div.card-head', h('div.card-title', icon('brain'), h('h2', 'Builders')));
      if (!st.repo.connected) {
        mount(buildersCard, head, h('p.muted', 'Connect a repository first.'));
        return;
      }
      const { brains } = await get('/api/site/builders');
      mount(buildersCard, head,
        h('p.small', 'A brain you allow gets the site-builder skills and tools, and a workspace of its own (a clone of the repository in its folder, under .fridolin-site). It builds and previews inside its sandbox and can only propose; you merge and deploy here.'),
        brains.length ? h('div.site-builders', brains.map((br) => toggle({
          checked: br.builder, label: br.name || br.id,
          onChange: async (on, input) => {
            input.disabled = true;
            try {
              await put(`/api/site/builders/${encodeURIComponent(br.id)}`, { on });
              toast(on ? `${br.name || br.id} may build the site` : `${br.name || br.id} no longer builds the site`, 'ok');
              await refresh();
            } catch (e) { input.checked = !on; toast(e.message, 'bad'); } finally { input.disabled = false; }
          },
        }))) : h('p.muted', 'No brains yet.'));
    }

    // ── domain and sign-in ─────────────────────────────────────────────
    function renderDomain() {
      const s = st.settings;
      const domain = markEditing(textInput({ placeholder: 'example.com', value: s.domain || '' }));
      const title = markEditing(textInput({ value: s.title }));
      const listen = markEditing(textInput({ value: s.listen }));
      const g = st.google;
      const clientNote = g.source === 'none'
        ? callout('warn', 'Google sign-in is off', 'Add a Google OAuth client — under Settings → Google sign-in for all of Fridolin, or a separate one for the site below.')
        : g.type !== 'web'
          ? callout('warn', 'Your Google client is a Desktop app client', 'Google lets a website sign people in only with a “Web application” client. Create one in Google Cloud Console and paste it below.')
          : callout('ok', g.source === 'site' ? 'Using the site’s own Google client' : 'Using Fridolin’s Google client',
            h('div', 'In Google Cloud Console, add this to the client’s ', h('em', 'Authorized redirect URIs'), ': ', mono(g.redirect_uri, { copy: true })));
      const clientJSON = h('textarea.mono', { rows: 3, placeholder: '{"web": {"client_id": "…", "client_secret": "…"}}' });
      const token = h('input', { type: 'password', autocomplete: 'off', placeholder: st.tunnel.token_saved ? 'saved — paste a new one to replace it' : 'eyJ…' });
      const tunnelSw = toggle({
        checked: s.tunnel, label: 'Run the tunnel while the site is on', disabled: !st.tunnel.token_saved || !st.runtime.cloudflared,
        onChange: async (on, input) => { try { await put('/api/site/settings', { tunnel: on }); poller.kick(); } catch (e) { input.checked = !on; toast(e.message, 'bad'); } },
      });
      mount(domainCard,
        h('div.card-head', h('div.card-title', icon('lock'), h('h2', 'Domain and sign-in'))),
        h('div.form-grid',
          field({ label: 'Domain', control: domain, help: 'The public host name, served over https. Point it at this Mac with a tunnel (below).' }),
          field({ label: 'Site name', control: title }),
          field({ label: 'Listen on', control: listen, help: 'Keep it on 127.0.0.1: the tunnel reaches it from this Mac.' })),
        h('div.actions', btn('Save', { kind: 'primary', onClick: async () => { await put('/api/site/settings', { domain: domain.value, title: title.value, listen: listen.value }); toast('Saved', 'ok'); await refresh(); } })),
        h('div.site-block', h('h3', 'Google sign-in'), clientNote,
          h('details', h('summary', g.source === 'site' ? 'Replace or remove the site’s Google client' : 'Use a separate Google client for the site'),
            field({ label: 'Client JSON (as downloaded from Google Cloud Console)', control: clientJSON, wide: true }),
            h('div.actions',
              btn('Save client', { small: true, onClick: async () => { await put('/api/site/google-client', { json: clientJSON.value }); toast('Google client saved', 'ok'); await refresh(); } }),
              g.source === 'site' ? btn('Remove', { small: true, onClick: async () => { await del('/api/site/google-client'); await refresh(); } }) : null))),
        h('div.site-block', h('h3', 'Cloudflare tunnel'),
          h('p.small', 'A tunnel gives the domain to this Mac without opening a port. In Cloudflare Zero Trust create a tunnel, give it a public host name for your domain pointing at ', mono(`http://${s.listen}`), ', and paste its token here.'),
          !st.runtime.cloudflared ? callout('info', 'cloudflared is not installed', h('div', 'macOS: ', mono('brew install cloudflared', { copy: true }))) : null,
          h('div.actions', token, btn(st.tunnel.token_saved ? 'Replace token' : 'Save token', { small: true, onClick: async () => { await put('/api/site/tunnel', { token: token.value }); toast('Tunnel token saved', 'ok'); await refresh(); } }),
            st.tunnel.token_saved ? btn('Remove token', { small: true, onClick: async () => { await put('/api/site/tunnel', { token: '' }); await refresh(); } }) : null),
          h('div.actions', tunnelSw, s.tunnel ? procPill(st.tunnel.process) : null),
          st.tunnel.process.error ? callout('bad', 'The tunnel stopped', st.tunnel.process.error) : null,
          logBox(st.tunnel.process.log)));
    }

    // ── people ─────────────────────────────────────────────────────────
    async function renderPeople() {
      const { users, roles } = await get('/api/site/users');
      const roleSelect = (value, kind, onChange) => {
        const opts = roles.filter((r) => kind === 'google' || r !== 'admin');
        const sel = select(opts, value);
        if (onChange) sel.addEventListener('change', () => onChange(sel.value, sel));
        return sel;
      };
      const rows = users.map((u) => h('tr',
        h('td', h('strong', u.name || u.email || u.username), h('div.small.muted', u.kind === 'google' ? `Google: ${u.email}` : `Password: ${u.username}`)),
        h('td', roleSelect(u.role, u.kind, async (role, sel) => {
          try { await put(`/api/site/users/${encodeURIComponent(u.id)}`, { role }); toast('Role changed', 'ok'); } catch (e) { sel.value = u.role; toast(e.message, 'bad'); }
        })),
        h('td.right',
          u.kind === 'password' ? btn('New password', { small: true, onClick: () => newPassword(u) }) : null,
          btn('', { small: true, icon: 'trash', title: 'Remove', onClick: async () => {
            if (!await confirmDialog({ title: `Remove ${u.name || u.email || u.username}?`, message: 'They are signed out at once and can no longer sign in.', confirm: 'Remove', danger: true })) return;
            await del(`/api/site/users/${encodeURIComponent(u.id)}`); renderPeople(); poller.kick();
          } }))));

      const kind = select([['google', 'Google account'], ['password', 'Username and password']], 'google');
      const ident = markEditing(textInput({ placeholder: 'name@example.com' }));
      const name = markEditing(textInput({ placeholder: 'optional' }));
      const role = roleSelect(roles.includes('member') ? 'member' : roles[1] || 'admin', 'google');
      const pw = h('input', { type: 'password', autocomplete: 'new-password', placeholder: 'at least 10 characters' });
      const pwField = field({ label: 'Password', control: pw });
      pwField.hidden = true;
      const identField = field({ label: 'Google address', control: ident });
      kind.addEventListener('change', () => {
        const isPw = kind.value === 'password';
        pwField.hidden = !isPw;
        identField.querySelector('label').textContent = isPw ? 'Username' : 'Google address';
        ident.placeholder = isPw ? 'sam' : 'name@example.com';
        const keep = role.value;
        mount(role, ...roles.filter((r) => !isPw || r !== 'admin').map((r) => h('option', { value: r }, r)));
        role.value = isPw && keep === 'admin' ? (roles.find((r) => r !== 'admin') || '') : keep;
      });
      const rolesInput = markEditing(textInput({ value: roles.filter((r) => r !== 'admin').join(', ') }));
      mount(peopleCard,
        h('div.card-head', h('div.card-title', icon('users'), h('h2', 'People'))),
        h('p.small', 'Who may sign in to the site, and their role. Admins sign in with Google. A module’s ', mono('module.json'), ' says which roles may open it.'),
        users.length ? h('div.table-wrap', h('table.table', h('thead', h('tr', h('th', 'Account'), h('th', 'Role'), h('th.right', ''))), h('tbody', rows)))
          : callout('info', 'No accounts yet', 'Start with yourself: your Google address, role admin.'),
        h('div.site-block', h('h3', 'Add someone'),
          h('div.form-grid', field({ label: 'Sign-in', control: kind }), identField, field({ label: 'Name', control: name }), field({ label: 'Role', control: role }), pwField),
          h('div.actions', btn('Add', {
            kind: 'primary', icon: 'plus', onClick: async () => {
              const isPw = kind.value === 'password';
              await post('/api/site/users', { kind: kind.value, email: isPw ? '' : ident.value, username: isPw ? ident.value : '', name: name.value, role: role.value, password: isPw ? pw.value : '' });
              toast('Added', 'ok'); editing = false; renderPeople(); poller.kick();
            },
          }))),
        h('details.site-block', h('summary', 'Roles'),
          field({ label: 'Roles besides admin, separated by commas', control: rolesInput }),
          h('div.actions', btn('Save roles', { small: true, onClick: async () => { await put('/api/site/roles', { roles: rolesInput.value.split(',') }); toast('Roles saved', 'ok'); editing = false; renderPeople(); } }))));
    }

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
  },
};
