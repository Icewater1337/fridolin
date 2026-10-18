// Some requests run a command on the bridge that may take longer than the
// client's default deadline. They must wait for the bridge's answer: giving
// up first shows "did not answer" and the offline banner while the bridge
// is still working.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { createPage } from '../assets/app/page.js';
import { DEFAULT_TIMEOUT_MS } from '../assets/app/api.js';

// setImmediate, because the tests mock setTimeout (the requests' deadlines).
const tick = () => new Promise((r) => setImmediate(r));
const settle = async () => { for (let i = 0; i < 5; i++) await tick(); };
const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

const siteStatus = {
  settings: { enabled: false, title: 'My site', listen: '127.0.0.1:8770', domain: '', repo: { url: 'git@github.com:you/site.git', branch: 'main', subdir: '' }, roles: ['admin', 'member'], approved: '', history: [], tunnel: false, builders: [], proposals: [] },
  runtime: { node: '/usr/local/bin/node', cloudflared: false, gh: false },
  site: { process: { state: 'stopped', log: [] }, healthy: false, local_url: 'http://127.0.0.1:8770/' },
  tunnel: { process: { state: 'stopped', log: [] }, token_saved: false },
  repo: { connected: true, github: 'you/site' },
  deploy_key: 'ssh-ed25519 AAAA fridolin-site',
  google: { source: 'none', redirect_uri: 'http://127.0.0.1:8770/auth/callback' },
  accounts: { total: 0, admins: 0 },
  deploy: null,
};

const proposal = { brain: 'work', title: 'A contact page', summary: '', commit: 'a'.repeat(40), branch: 'builder/work', at: '2026-10-18T09:00:00Z' };
const withBuilder = { ...siteStatus, settings: { ...siteStatus.settings, builders: ['work'], proposals: [proposal] } };
const siteAnswers = (status) => ({
  '/api/site': status,
  '/api/site/users': { users: [], roles: ['admin', 'member'] },
  '/api/site/builders': { brains: [{ id: 'work', name: 'Work', builder: status.settings.builders.includes('work') }] },
  '/api/site/proposals': { proposals: status.settings.proposals.map((p) => ({ proposal: p, changes: [] })) },
  '/api/site/secrets': { modules: [{ module: 'finance', title: 'Finance', live: true, declared: ['API_KEY'], set: [] }] },
});

const plaudStatus = {
  cli: { found: true }, connected: true, account: 'you@example.com', login_dir: '/Users/you/.fridolin/plaud',
  settings: { enabled: false, interval_seconds: 60 },
};

const cases = [
  {
    name: 'Site: checking the repository for changes runs git fetch',
    page: () => import('../assets/pages/site.js'),
    answers: siteAnswers(siteStatus),
    act: (root) => button(root, 'Check for changes').click(),
    path: '/api/site/fetch',
    bridgeMs: 3 * 60000, // SiteManager.git
  },
  {
    name: 'Site: making a brain a builder installs its workspace',
    page: () => import('../assets/pages/site.js'),
    answers: siteAnswers(siteStatus),
    act: (root) => {
      const sw = root.querySelectorAll('.site-builders input').find((i) => !i.checked);
      sw.checked = true;
      sw.dispatchEvent(new Event('change'));
    },
    path: '/api/site/builders/work',
    bridgeMs: 3 * 60000 + 10 * 60000, // git fetch, then npm install
  },
  {
    name: 'Site: merging a proposal fetches, merges and pushes',
    page: () => import('../assets/pages/site.js'),
    answers: siteAnswers(withBuilder),
    act: async (root) => {
      button(root, 'Merge and deploy').click();
      await settle();
      button(document.querySelector('dialog'), 'Merge and deploy').click(); // confirm
    },
    path: '/api/site/proposals/work/merge',
    bridgeMs: 3 * 3 * 60000, // fetch, push, fetch
  },
  {
    name: 'Integrations: disconnecting Plaud runs its logout',
    page: () => import('../assets/pages/integrations.js'),
    answers: { '/api/brains': [], '/api/plaud/status': plaudStatus },
    act: async (root) => {
      button(root, 'Disconnect').click();
      await settle();
      button(document.querySelector('dialog'), 'Disconnect').click(); // confirm
    },
    path: '/api/plaud/disconnect',
    bridgeMs: 30000, // apiPlaudDisconnect
  },
];

for (const c of cases) {
  test(c.name, async (t) => {
    t.mock.timers.enable({ apis: ['setTimeout'] });
    const document = installDom();
    const net = stubFetch(t);
    const ctx = createPage();
    t.after(() => ctx.destroy());
    const root = document.createElement('div');
    document.body.append(root);
    (await c.page()).default.render(root, ctx);

    // Answer what the page loads, as often as it asks.
    const answered = new Set();
    const serve = async () => {
      for (let n = 0; n < 5; n++) {
        net.calls.forEach((call, i) => {
          const path = call.url.split('?')[0];
          if (!answered.has(i) && c.answers[path]) { answered.add(i); net.answer(i, c.answers[path]); }
        });
        await settle();
      }
    };
    await serve();
    await c.act(root);
    await settle();
    const slow = net.calls.find((call) => call.url === c.path);
    assert.ok(slow, `${c.path} was not requested`);
    assert.ok(c.bridgeMs >= DEFAULT_TIMEOUT_MS, 'the bridge takes longer than the default deadline');
    t.mock.timers.tick(c.bridgeMs);
    assert.equal(slow.signal.aborted, false, 'still waiting when the bridge gives up itself');
  });
}
