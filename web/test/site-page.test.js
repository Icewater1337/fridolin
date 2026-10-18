import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/site.js';

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
    repo: { connected: true, github: true },
    deploy_key: 'ssh-ed25519 AAAAexample fridolin-site',
    google: { source: 'shared', type: 'web', redirect_uri: 'https://example.com/auth/callback' },
    accounts: { total: 1, admins: 1 },
    deploy: null,
  };
}

const proposal = { proposal: { brain: 'web', title: 'Events page', summary: 'A page with this month’s events.', commit: commit('c'), at: '2026-10-18T10:00:00Z' }, changes: [], preview: null };

async function openPage(t, first = siteStatus(), { proposals = [] } = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = {
    site: first,
    users: { users: [{ id: 'u1', kind: 'google', email: 'you@example.com', name: 'You', role: 'admin' }], roles: ['admin', 'member'] },
    proposals,
    holdNext: null, // a promise the next GET /api/site waits for
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
    if (path === '/api/site/roles' && method === 'PUT') { bridge.users.roles = ['admin', ...body.roles.map((r) => r.trim()).filter(Boolean)]; return { ok: true }; }
    if (path === '/api/site/builders') return { brains: [{ id: 'web', name: 'Web', builder: bridge.site.settings.builders.includes('web') }] };
    if (path === '/api/site/proposals') return { proposals: bridge.proposals };
    if (path === '/api/site/secrets') return { modules: [] };
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
  return document.getElementById(l.getAttribute('for'));
};
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
  fresh.repo = { connected: false, github: false };
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
    s.repo = { connected: false, github: false };
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
