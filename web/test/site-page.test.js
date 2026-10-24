import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/site.js';
import { setStatus } from '../assets/app/store.js';

// The tests give only the fields of the status that the page reads.
/** @import { Status } from '../types/api.js' */

const commit = (c) => c.repeat(40);

// What GET /api/site answers for a site that is connected, deployed and on.
function siteStatus() {
  return {
    settings: {
      enabled: true, title: 'My site', listen: '127.0.0.1:8790', domain: 'example.com',
      repo: { url: 'git@github.com:you/my-site.git', branch: 'main', subdir: '' },
      roles: ['admin', 'member'], approved: commit('a'),
      history: [{ commit: commit('a'), subject: 'Welcome page', at: '2026-10-18T09:00:00Z' }],
      tunnel: false, builders: [], proposals: [],
    },
    runtime: { node: '/usr/local/bin/node', cloudflared: true, gh: false },
    site: {
      process: { state: 'running', error: '', restarts: 0, log: ['listening on 127.0.0.1:8790'] },
      healthy: true, local_url: 'http://127.0.0.1:8790/', sandbox: { on: true, note: 'sandbox-exec' },
    },
    public_url: 'https://example.com/',
    tunnel: { process: { state: '', error: '', log: null }, token_saved: false },
    repo: { connected: true, github: 'you/my-site' },
    deploy_key: 'ssh-ed25519 AAAAexample fridolin-site',
    google: { source: 'shared', type: 'web', redirect_uri: 'https://example.com/auth/callback' },
    accounts: { total: 1, admins: 1 },
    deploy: null,
  };
}

const proposal = { proposal: { brain: 'web', title: 'Events page', summary: 'A page with this month’s events.', commit: commit('c'), at: '2026-10-18T10:00:00Z' }, changes: [], preview: null };

async function openPage(t, first = siteStatus(), { proposals = [], brains = [{ id: 'web', name: 'Web' }], secrets = [] } = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = {
    site: first,
    users: { users: [{ id: 'u1', kind: 'google', email: 'you@example.com', name: 'You', role: 'admin' }], roles: ['admin', 'member'] },
    proposals,
    holdNext: null, // a promise the next GET /api/site waits for
    holdBuilder: null, // a promise the next PUT /api/site/builders/{id} waits for
    github: /** @type {object} */ ({ ok: true, repo: 'you/my-site' }), // what POST /api/site/deploy-key/github answers
  };
  const calls = serve(t, (path, init) => {
    const method = init.method || 'GET';
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (path === '/api/site' && method === 'GET') {
      const now = structuredClone(bridge.site); // what the bridge has when asked
      now.public_url = now.settings.domain ? `https://${now.settings.domain}/` : undefined;
      const gate = bridge.holdNext;
      bridge.holdNext = null;
      return gate ? gate.then(() => now) : now;
    }
    if (path === '/api/site/settings' && method === 'PUT') { Object.assign(bridge.site.settings, body); return bridge.site.settings; }
    if (path === '/api/site/users' && method === 'GET') return bridge.users;
    if (path === '/api/site/users' && method === 'POST') {
      bridge.users.users.push({ id: `u${bridge.users.users.length + 1}`, kind: body.kind, email: body.email, name: body.name, role: body.role });
      bridge.site.accounts.total += 1;
      return { ok: true };
    }
    if (path === '/api/site/deploy-key' && method === 'POST') { bridge.site.deploy_key = 'ssh-ed25519 AAAAnew fridolin-site'; return { ok: true }; }
    if (path === '/api/site/roles' && method === 'PUT') {
      if (body.roles) bridge.users.roles = ['admin', ...body.roles.map((r) => r.trim()).filter(Boolean)];
      if (body.brains) bridge.site.settings.role_brains = { ...(bridge.site.settings.role_brains || {}), ...body.brains };
      if (body.uploads) bridge.site.settings.role_uploads = { ...(bridge.site.settings.role_uploads || {}), ...body.uploads };
      return { ok: true };
    }
    if (path === '/api/site/check-domain' && method === 'POST') return { status: 200, body: bridge.check || { url: 'https://example.com/_fridolin/health', ok: true, status: 200 } };
    if (path === '/api/site/builders') return { brains: brains.map((b) => ({ ...b, builder: bridge.site.settings.builders.includes(b.id) })) };
    if (path.startsWith('/api/site/builders/') && method === 'PUT') {
      const id = decodeURIComponent(path.split('/').pop());
      const done = () => {
        const s = bridge.site.settings;
        s.builders = body.on ? [...s.builders.filter((x) => x !== id), id] : s.builders.filter((x) => x !== id);
        return { ok: true };
      };
      const gate = bridge.holdBuilder;
      bridge.holdBuilder = null;
      // The gate resolves with how the save ends: 'ok', 'fails', or
      // 'fails late', as one whose npm install fails, once the brain is
      // listed as a builder (SiteManager.SetBuilder).
      const failed = { status: 500, body: { error: 'npm install failed' } };
      const end = { ok: done, fails: () => failed, 'fails late': () => { done(); return failed; } };
      return gate ? gate.then((how) => end[how]()) : done();
    }
    if (path === '/api/site/proposals') return { proposals: bridge.proposals };
    if (path === '/api/site/secrets') return { modules: secrets };
    if (path === '/api/site/github-repo' && method === 'POST') return { url: `git@github.com:you/${body.name}.git` };
    if (path === '/api/site/deploy-key/github' && method === 'POST') return bridge.github;
    return { ok: true };
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  // The page asks for the status every 5 s, or every 1.5 s during a deploy.
  const poll = async (next, ms = 5000) => { bridge.site = next; t.mock.timers.tick(ms); await settle(); };
  const gets = () => calls.filter((c) => c.path === '/api/site' && c.method === 'GET').length;
  return { root, ctx, bridge, calls, poll, gets };
}

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const inputFor = (root, label) => {
  const l = root.querySelectorAll('label').find((x) => x.textContent === label);
  return /** @type {FakeElement} */ (document.getElementById(l.getAttribute('for')));
};
/**
 * A switch toggle() made: its label, with the checkbox as .input.
 * @typedef {ReturnType<typeof import('../assets/components/fields.js').toggle>} Switch
 */
const siteSwitch = (root) => root.querySelectorAll('label.switch').find((l) => /The site is/.test(l.textContent));

test('opening the page asks for the site once', async (t) => {
  const { root, gets } = await openPage(t);
  assert.equal(gets(), 1);
  assert.match(root.querySelector('h2').textContent, /My site/);
  assert.match(root.textContent, /The site is on/);
});

// A poll that was on its way when something was saved may answer after the
// refresh the save asked for, with what the bridge had before the save.
test('a poll that answers after a save’s refresh is never painted', async (t) => {
  const publicAddress = (root) => root.querySelector('dl.kv').textContent;
  const cases = [
    {
      name: 'saving the domain',
      save: async (root) => {
        type(inputFor(root, 'Domain'), 'example.org');
        button(root, 'Save').click();
      },
      saved: (root) => {
        assert.match(publicAddress(root), /https:\/\/example\.org\//);
        assert.equal(inputFor(root, 'Domain').value, 'example.org');
      },
    },
    {
      name: 'turning the site on',
      first: (s) => { s.settings.enabled = false; },
      save: async (root) => {
        const input = siteSwitch(root).input;
        input.checked = true;
        input.dispatchEvent(new Event('change', { bubbles: true }));
      },
      saved: (root) => assert.match(siteSwitch(root).textContent, /The site is on/),
    },
  ];
  for (const c of cases) {
    await t.test(c.name, async (t) => {
      const first = siteStatus();
      if (c.first) c.first(first);
      const { root, bridge, gets } = await openPage(t, first);
      let answer;
      bridge.holdNext = new Promise((resolve) => { answer = resolve; });
      const asked = gets();
      t.mock.timers.tick(5000); // the poll asks; the bridge takes its time
      await settle();
      assert.equal(gets(), asked + 1);

      await c.save(root);
      await settle();
      assert.equal(gets(), asked + 2, 'the save asked again at once');
      c.saved(root);

      answer(); // the poll from before the save answers now
      await settle();
      c.saved(root);
      assert.equal(gets(), asked + 2);
    });
  }
});

test('a new log line changes only the log', async (t) => {
  const { root, poll } = await openPage(t);
  const log = root.querySelector('details.site-log');
  assert.equal(log.hidden, false);
  log.open = true;
  const restart = button(root, 'Restart');
  const sw = siteSwitch(root);
  const next = siteStatus();
  next.site.process.log.push('GET / 200');
  await poll(next);
  assert.equal(root.querySelector('details.site-log'), log, 'the same <details>');
  assert.equal(log.open, true, 'still open');
  assert.equal(log.querySelector('pre').textContent, 'listening on 127.0.0.1:8790\nGET / 200');
  assert.equal(button(root, 'Restart'), restart, 'the Restart button is the one under the pointer');
  assert.equal(siteSwitch(root), sw);
});

test('saving one card keeps what is typed into another', async (t) => {
  const cases = [
    {
      name: 'adding someone',
      save: async (root, calls) => {
        type(inputFor(root, 'Google address'), 'sam@example.com');
        button(root, 'Add').click();
        await settle();
        assert.ok(calls.some((c) => c.path === '/api/site/users' && c.method === 'POST'), 'added');
        assert.equal(inputFor(root, 'Google address').value, '', 'the form is empty for the next one');
        assert.match(root.querySelector('table').textContent, /sam@example\.com/);
      },
    },
    {
      name: 'saving the roles',
      save: async (root, calls) => {
        type(inputFor(root, 'Roles besides admin, separated by commas'), 'member, family');
        button(root, 'Save roles').click();
        await settle();
        assert.ok(calls.some((c) => c.path === '/api/site/roles' && c.method === 'PUT'), 'saved');
        assert.equal(inputFor(root, 'Roles besides admin, separated by commas').value, 'member, family');
      },
    },
  ];
  for (const c of cases) {
    await t.test(c.name, async (t) => {
      const { root, ctx, calls } = await openPage(t);
      const domain = inputFor(root, 'Domain');
      type(domain, 'example.org');
      assert.equal(ctx.dirty(), true);
      await c.save(root, calls);
      assert.equal(inputFor(root, 'Domain'), domain, 'the Domain form was not rebuilt');
      assert.equal(domain.value, 'example.org', 'and keeps its draft');
      assert.equal(ctx.dirty(), true, 'so leaving still asks');
      type(domain, 'example.com');
      assert.equal(ctx.dirty(), false, 'typed back, nothing is unsaved');
    });
  }
});

test('a repository typed in stays while the deploy key is made', async (t) => {
  const fresh = siteStatus();
  fresh.repo = { connected: false, github: '' };
  fresh.settings = { ...fresh.settings, enabled: false, approved: '', history: [], repo: { url: '', branch: 'main', subdir: '' } };
  delete fresh.deploy_key;
  const { root, ctx, bridge, poll } = await openPage(t, fresh);
  const url = inputFor(root, 'Repository (SSH address)');
  type(url, 'git@github.com:you/my-site.git');
  assert.equal(ctx.dirty(), true);
  button(root, 'Create a deploy key').click();
  await settle();
  assert.match(root.textContent, /ssh-ed25519 AAAAnew fridolin-site/, 'the new key shows');
  await poll(bridge.site);
  assert.equal(inputFor(root, 'Repository (SSH address)'), url, 'the form was not rebuilt');
  assert.equal(url.value, 'git@github.com:you/my-site.git');
  assert.equal(ctx.dirty(), true);
});

test('what is typed into the repository form outlasts a clone', async (t) => {
  const notConnected = () => {
    const s = siteStatus();
    s.repo = { connected: false, github: '' };
    s.settings = { ...s.settings, enabled: false, approved: '', history: [], repo: { url: '', branch: 'main', subdir: '' } };
    return s;
  };
  // git makes the clone's folder at once, so the status says connected
  // before the settings name the repository.
  const cloning = (url) => {
    const s = notConnected();
    s.repo.connected = true;
    s.settings.repo.url = url;
    return s;
  };
  const cases = [
    { name: 'a clone that fails gives the form back as typed', during: cloning(''), kept: true },
    { name: 'a repository connected elsewhere starts it afresh', during: cloning('git@github.com:you/other.git'), kept: false },
  ];
  for (const c of cases) {
    await t.test(c.name, async (t) => {
      const { root, ctx, poll } = await openPage(t, notConnected());
      const url = inputFor(root, 'Repository (SSH address)');
      type(url, 'git@github.com:you/my-site.git');
      await poll(c.during);
      assert.equal(ctx.dirty(), false, 'the form is hidden, so there is nothing to lose');
      await poll(notConnected());
      const now = inputFor(root, 'Repository (SSH address)');
      assert.equal(now === url, c.kept);
      assert.equal(now.value, c.kept ? 'git@github.com:you/my-site.git' : '');
      assert.equal(ctx.dirty(), c.kept);
    });
  }
});

test('a secret or token typed in counts as unsaved until it is saved', async (t) => {
  const notConnected = siteStatus();
  notConnected.repo = { connected: false, github: '' };
  notConnected.runtime.gh = true;
  notConnected.settings = { ...notConnected.settings, enabled: false, approved: '', history: [], repo: { url: '', branch: 'main', subdir: '' } };
  const placeholder = (p) => (root) => root.querySelectorAll('input, textarea').find((i) => i.getAttribute('placeholder') === p);
  const cases = [
    {
      name: 'a module’s secret',
      options: { secrets: [{ module: 'finance', title: 'Finance', live: true, declared: ['API_KEY'], set: [] }] },
      find: placeholder('value'), save: 'Save', path: '/api/site/secrets/finance/API_KEY',
    },
    { name: 'the tunnel token', find: placeholder('eyJ…'), save: 'Save token', path: '/api/site/tunnel' },
    { name: 'a Google client', find: (root) => root.querySelector('textarea.draft'), save: 'Save client', path: '/api/site/google-client' },
    {
      name: 'a new GitHub repository’s name', first: notConnected,
      find: placeholder('my-site'), save: 'Create a private GitHub repository', path: '/api/site/github-repo',
      after: true, // the Repository form has its address now, to connect it
    },
  ];
  for (const c of cases) {
    await t.test(c.name, async (t) => {
      const { root, ctx, calls } = await openPage(t, structuredClone(c.first || siteStatus()), c.options);
      const input = c.find(root);
      assert.equal(ctx.dirty(), false);
      type(input, 'typed');
      assert.equal(ctx.dirty(), true, 'leaving would lose it');
      input.closest('li, .actions, details').querySelectorAll('button').find((x) => x.textContent === c.save).click();
      await settle();
      assert.ok(calls.some((x) => x.path === c.path && x.method !== 'GET'), 'saved');
      assert.equal(input.value, '', 'never shown again');
      assert.equal(ctx.dirty(), c.after || false);
    });
  }
});

test('a deploy asks for the proposals again', async (t) => {
  // What a proposal changes is measured against the live commit.
  const reviewed = (review) => [{ ...proposal, changes: [{ id: 'events', status: 'changed', review }] }];
  const first = siteStatus();
  first.settings.builders = ['web'];
  first.settings.proposals = [proposal.proposal];
  const { root, bridge, calls, poll } = await openPage(t, first, {
    proposals: reviewed([{ field: 'access', note: 'who may open it', old: null, new: ['member'] }]),
  });
  const shown = () => root.querySelectorAll('.site-review li').length;
  const asked = () => calls.filter((c) => c.path === '/api/site/proposals').length;
  assert.equal(shown(), 1);
  const before = asked();

  bridge.proposals = reviewed([]); // the live commit has that access now
  const deployed = structuredClone(first);
  deployed.settings.approved = commit('b');
  deployed.settings.history.push({ commit: commit('b'), subject: 'Events page', at: '2026-10-18T11:00:00Z' });
  await poll(deployed);
  assert.equal(asked(), before + 1);
  assert.equal(shown(), 0);
  assert.match(root.textContent, /No change to access, server code, files or commands\./);
});

test('a builder switch stays put while it saves', async (t) => {
  const cases = [
    { name: 'and then shows the saved state', ends: 'ok', builders: ['docs', 'web'] },
    { name: 'and goes back when the save fails', ends: 'fails', builders: ['docs'] },
    { name: 'or shows what a failed save changed', ends: 'fails late', builders: ['docs', 'web'] },
  ];
  for (const c of cases) {
    await t.test(c.name, async (t) => {
      const { root, bridge, calls, poll } = await openPage(t, siteStatus(), { brains: [{ id: 'web', name: 'Web' }, { id: 'docs', name: 'Docs' }] });
      const switchOf = (label) => {
        /** @type {Switch[]} */
        const switches = root.querySelectorAll('.site-builders label.switch');
        return switches.find((l) => l.textContent === label);
      };
      const web = switchOf('Web (web)');
      const docs = switchOf('Docs (docs)');
      let release;
      bridge.holdBuilder = new Promise((resolve) => { release = resolve; });
      web.input.checked = true; // npm install takes its time
      web.input.dispatchEvent(new Event('change', { bubbles: true }));
      await settle();
      assert.ok(calls.some((x) => x.path === '/api/site/builders/web' && x.method === 'PUT'));
      assert.equal(web.input.disabled, true);

      // Meanwhile docs becomes a builder elsewhere, and the page notices.
      const elsewhere = structuredClone(bridge.site);
      elsewhere.settings.builders = ['docs'];
      await poll(elsewhere);
      assert.equal(switchOf('Docs (docs)'), docs, 'each switch keeps its node');
      assert.equal(docs.input.checked, true, 'and shows what changed');
      assert.equal(switchOf('Web (web)'), web);
      assert.equal(web.input.disabled, true, 'the one saving stays disabled');
      assert.equal(web.input.checked, true, 'and shows what you chose');

      release(c.ends);
      await settle();
      assert.deepEqual(bridge.site.settings.builders, c.builders);
      assert.equal(switchOf('Web (web)'), web);
      assert.equal(web.input.disabled, false);
      assert.equal(web.input.checked, c.builders.includes('web'));
    });
  }
});

test('the deploy and the proposals keep updating while a card has edits', async (t) => {
  const { root, bridge, poll } = await openPage(t);
  const domain = inputFor(root, 'Domain');
  type(domain, 'example.org');
  const deploying = (step, log) => {
    const s = siteStatus();
    s.deploy = { state: 'running', commit: commit('b'), step, log };
    return s;
  };
  await poll(deploying('npm install', ['fetched bbbbbbbb']));
  assert.match(root.textContent, /Deploying bbbbbbbbbbbb: npm install/);
  const steps = root.querySelectorAll('details').find((d) => d.querySelector('summary').textContent === 'Steps');
  steps.open = true;
  await poll(deploying('build', ['fetched bbbbbbbb', 'npm install done']), 1500);
  assert.match(root.textContent, /Deploying bbbbbbbbbbbb: build/, 'the next step shows');
  assert.equal(steps.open, true, 'with the steps still open');
  assert.deepEqual(steps.querySelectorAll('li').map((li) => li.textContent), ['fetched bbbbbbbb', 'npm install done']);

  // A builder brain proposes something meanwhile.
  bridge.proposals = [proposal];
  const proposed = deploying('build', ['fetched bbbbbbbb', 'npm install done']);
  proposed.settings.builders = ['web'];
  proposed.settings.proposals = [proposal.proposal];
  await poll(proposed, 1500);
  assert.match(root.textContent, /Events page/, 'the proposal shows');
  assert.equal(inputFor(root, 'Domain'), domain);
  assert.equal(domain.value, 'example.org', 'and the draft is still there');
});

// ── the deploy key, added with gh ──────────────────────────────────────

// A site with gh and a deploy key, but no repository connected yet.
function notConnectedYet({ key = true } = {}) {
  const s = siteStatus();
  s.repo = { connected: false, github: '' };
  s.runtime = { ...s.runtime, gh: true };
  s.settings = { ...s.settings, enabled: false, approved: '', history: [], repo: { url: '', branch: 'main', subdir: '' } };
  if (!key) delete s.deploy_key;
  return s;
}

const toasts = (kind) => document.querySelectorAll(`.toast.${kind}`).map((el) => el.textContent);
const ghCalls = (calls) => calls.filter((c) => c.path === '/api/site/deploy-key/github').map((c) => [c.method, c.body]);

test('before Connect, gh adds the key to the address typed for Connect', async (t) => {
  const { root, ctx, calls } = await openPage(t, notConnectedYet());
  const url = inputFor(root, 'Repository (SSH address)');
  type(url, '  git@github.com:you/my-site.git ');
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls(calls), [['POST', { url: 'git@github.com:you/my-site.git' }]]);
  assert.deepEqual(toasts('ok'), ['Deploy key added to you/my-site on GitHub']);
  assert.ok(!calls.some((c) => c.path === '/api/site/repo'), 'adding the key does not connect');
  assert.equal(inputFor(root, 'Repository (SSH address)'), url);
  assert.equal(url.value, '  git@github.com:you/my-site.git ', 'the form keeps the address for Connect');
  assert.equal(ctx.dirty(), true);
});

test('with no address typed, the gh button says what is missing and asks the bridge nothing', async (t) => {
  const { root, calls } = await openPage(t, notConnectedYet());
  type(inputFor(root, 'Repository (SSH address)'), '   ');
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls(calls), []);
  assert.equal(toasts('warn').length, 1);
  assert.match(toasts('warn')[0], /Enter the repository’s SSH address below first.*git@github\.com:you\/my-site\.git/);
  assert.equal(document.activeElement, inputFor(root, 'Repository (SSH address)'), 'the address field gets the focus');
});

test('an address the bridge refuses for gh shows its reason', async (t) => {
  const { root, bridge, calls } = await openPage(t, notConnectedYet());
  bridge.github = { status: 400, body: { error: 'the repository is not on GitHub; add the key in its settings by hand' } };
  type(inputFor(root, 'Repository (SSH address)'), 'git@example.com:you/my-site.git');
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls(calls), [['POST', { url: 'git@example.com:you/my-site.git' }]]);
  assert.ok(toasts('error').some((m) => /not on GitHub/.test(m)), `toasts: ${toasts('error')}`);
  assert.deepEqual(toasts('ok'), []);
});

test('a key made after typing the address goes to that address', async (t) => {
  const { root, calls } = await openPage(t, notConnectedYet({ key: false }));
  assert.equal(button(root, 'Add it to GitHub with gh'), undefined, 'no key to add yet');
  type(inputFor(root, 'Repository (SSH address)'), 'git@github.com:you/my-site.git');
  button(root, 'Create a deploy key').click();
  await settle();
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls(calls), [['POST', { url: 'git@github.com:you/my-site.git' }]]);
});

test('once connected, gh adds the key to the connected repository as before', async (t) => {
  const connected = siteStatus();
  connected.runtime = { ...connected.runtime, gh: true };
  const { root, calls } = await openPage(t, connected);
  button(root, 'Add it to GitHub with gh').click();
  await settle();
  assert.deepEqual(ghCalls(calls), [['POST', undefined]]);
  assert.deepEqual(toasts('ok'), ['Deploy key added to you/my-site on GitHub']);
});

test('without gh there is no gh button, before or after Connect', async (t) => {
  for (const st of [notConnectedYet(), siteStatus()]) {
    st.runtime = { ...st.runtime, gh: false };
    const { root } = await openPage(t, st);
    assert.equal(button(root, 'Add it to GitHub with gh'), undefined);
    t.mock.timers.reset();
  }
});

test('the domain steps guide through the tunnel and check the domain', async (t) => {
  const { root, bridge, calls } = await openPage(t);
  const steps = root.querySelectorAll('ol.site-steps h3').map((e) => e.textContent);
  assert.deepEqual(steps, ['The domain', 'A Cloudflare tunnel to it', 'Check that it answers']);
  assert.match(root.textContent, /Cloudflare Zero Trust/);
  assert.match(root.textContent, /wiki\.example\.com/);
  bridge.check = { url: 'https://example.com/_fridolin/health', ok: false, status: 530, error: 'Cloudflare answers for example.com but cannot reach the site (530).' };
  button(root, 'Check https://example.com').click();
  await settle();
  assert.ok(calls.some((c) => c.path === '/api/site/check-domain' && c.method === 'POST'));
  assert.match(root.textContent, /example\.com does not answer yet/);
  assert.match(root.textContent, /cannot reach the site \(530\)/);
});

test('a site with no repository can go on, with Files and Wiki alone', async (t) => {
  const first = siteStatus();
  first.settings.enabled = false;
  first.settings.approved = '';
  first.settings.history = [];
  first.repo = { connected: false, github: '' };
  const { root } = await openPage(t, first);
  assert.doesNotMatch(root.textContent, /Before the site can go on/, 'nothing stands before turning it on');
  assert.equal(siteSwitch(root).querySelector('input').disabled, false);
  assert.match(root.textContent, /no modules deployed/);
});

test('each role is given the brains it sees on Files and Wiki', async (t) => {
  const { root, bridge, calls } = await openPage(t);
  setStatus(/** @type {Status} */ ({ brains: [{ id: 'main', name: 'Main' }, { id: 'work', name: 'Work' }] }));
  await settle();
  const row = root.querySelectorAll('tr').find((tr) => tr.textContent.startsWith('member'));
  assert.ok(row, 'a row for the member role, none for admin');
  assert.equal(root.querySelectorAll('tr').some((tr) => tr.textContent.startsWith('admin')), false);
  const work = row.querySelectorAll('label.switch').find((l) => l.textContent.startsWith('Work')).querySelector('input');
  work.checked = true;
  work.dispatchEvent(new Event('change', { bubbles: true }));
  await settle();
  const put = calls.find((c) => c.path === '/api/site/roles' && c.method === 'PUT');
  assert.deepEqual(put.body, { brains: { member: ['work'] } });
  assert.deepEqual(bridge.site.settings.role_brains, { member: ['work'] });
  assert.match(root.textContent, /every view and change is written to the audit log/);
  // Uploads, to a brain the role sees.
  const again = root.querySelectorAll('tr').find((tr) => tr.textContent.startsWith('member'));
  const cells = again.querySelectorAll('td');
  assert.deepEqual(cells[2].querySelectorAll('label.switch').map((l) => l.textContent.trim()), ['Work (work)'], 'only the brains it sees');
  const up = cells[2].querySelector('input');
  up.checked = true;
  up.dispatchEvent(new Event('change', { bubbles: true }));
  await settle();
  assert.deepEqual(calls.filter((c) => c.path === '/api/site/roles' && c.method === 'PUT').pop().body, { uploads: { member: ['work'] } });
});
