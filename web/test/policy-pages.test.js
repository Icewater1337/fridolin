import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import approvalsPage from '../assets/pages/approvals.js';
import threadsPage from '../assets/pages/threads.js';
import auditPage from '../assets/pages/audit.js';
import devPage from '../assets/pages/dev.js';
import { skillsCard } from '../assets/components/skills-card.js';

const DM = '15551234567@s.whatsapp.net';

async function open(t, page, route, params = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, route);
  const root = h('div');
  document.body.append(root);
  const ctx = createPage({ params });
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  return { root, calls };
}

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent.includes(label));

const pending = {
  id: 'a1', kind: 'approval', brain: 'taxes', chat: DM, thread: DM, action: 'Submit the 2025 tax return',
  details: 'Refund: 312 EUR', risk: 'irreversible', skill: 'taxes-de', step: 'submit', options: [], status: 'pending',
  created_at: '2026-10-20T10:00:00Z', expires_at: '2026-10-21T10:00:00Z',
};

// The Approvals page lists what waits with its draft, and Approve is the
// owner's vote; the lockdown shows with its Unlock.
test('the Approvals page decides and unlocks', async (t) => {
  const bridge = { pending: [pending], lockdown: true };
  const { root, calls } = await open(t, approvalsPage, (path, init) => {
    if (path === '/api/approvals/a1' && init.method === 'POST') { bridge.pending = []; return { status: 'approved' }; }
    if (path === '/api/policy/lockdown') { bridge.lockdown = false; return { lockdown: false }; }
    if (path.startsWith('/api/approvals')) return { pending: bridge.pending, history: [] };
    if (path.startsWith('/api/policy')) return { lockdown: bridge.lockdown, lockdown_by: '15551234567', lockdown_since: '2026-10-20T09:00:00Z', max_runs_per_hour: 20, max_pending_irreversible: 3, approval_timeout_hours: 24 };
    return {};
  });
  assert.match(root.textContent, /Submit the 2025 tax return/);
  assert.match(root.textContent, /Refund: 312 EUR/);
  assert.match(root.textContent, /Lockdown/);
  button(root, 'Approve').click();
  await settle();
  t.mock.timers.tick(3000);
  await settle();
  const post = calls.find((c) => c.path === '/api/approvals/a1');
  assert.deepEqual(post.body, { decision: 'approve' });
  assert.match(root.textContent, /Nothing waiting/);
  button(root, 'Unlock').click();
  await settle();
  t.mock.timers.tick(5000);
  await settle();
  assert.deepEqual(calls.find((c) => c.path === '/api/policy/lockdown').body, { on: false });
  assert.match(root.textContent, /Lock down/);
});

test('the Threads page shows runs by chat', async (t) => {
  const { root } = await open(t, threadsPage, (path) => {
    if (path.startsWith('/api/threads')) {
      return [{ thread: DM, last: '2026-10-20T10:00:00Z', count: 2, brains: ['core', 'family'], runs: [
        { run: 'r2', brain: 'family', chat: 'delegate:r1', started: '2026-10-20T10:00:00Z', skill: '', risk: 'write', detail: 'delegated by run r1 of core', outcome: 'ok', duration_ms: 1200 },
        { run: 'r1', brain: 'core', chat: DM, started: '2026-10-20T09:59:00Z', skill: 'taxes-de', risk: 'irreversible', detail: '', outcome: 'refused: lockdown', duration_ms: 0 },
      ] }];
    }
    return {};
  });
  assert.match(root.textContent, /2 runs/);
  assert.match(root.textContent, /taxes-de/);
  assert.match(root.textContent, /delegated by run r1 of core/);
  assert.ok(root.querySelector('a[href="#/audit/r2"]'), 'a run links to its audit lines');
});

test('the Audit page filters by kind and run', async (t) => {
  const { root, calls } = await open(t, auditPage, (path) => {
    if (path.startsWith('/api/audit')) return [{ ts: '2026-10-20T10:00:00Z', kind: 'approval_vote', brain: 'taxes', run: 'r1', thread: DM, by: '15559876543', outcome: 'ignored: not an allowed number' }];
    return {};
  }, { run: 'r1' });
  assert.match(root.textContent, /ignored: not an allowed number/);
  assert.match(calls.find((c) => c.path.startsWith('/api/audit')).path, /run=r1/);
  const kind = root.querySelector('select');
  kind.value = 'approval_vote';
  kind.dispatchEvent(new Event('change'));
  await settle();
  assert.match(calls.filter((c) => c.path.startsWith('/api/audit')).at(-1).path, /kind=approval_vote/);
});

// A run's audit lines come with the files it changed and what undoing it
// would do; Undo asks first, then shows what it did.
test('the Audit page of a run undoes its files', async (t) => {
  const steps = [
    { root: 'brain', path: 'notes.md', op: 'changed', action: 'restore' },
    { root: 'brain', path: 'draft.md', op: 'created', action: 'trash' },
    { root: 'brain', path: 'summary.md', op: 'changed', action: 'conflict', why: 'it changed since the run' },
  ];
  const record = { v: 1, run: 'r1', brain: 'taxes', started: '2026-10-20T10:00:00Z', ended: '2026-10-20T10:01:00Z', roots: [], alongside: ['r2'], changes: [] };
  let undone = false;
  const { root, calls } = await open(t, auditPage, (path, init) => {
    if (path === '/api/runs/r1/undo' && init.method === 'POST') {
      undone = true;
      return { run: 'r1', brain: 'taxes', dry_run: false, steps: [{ ...steps[0], done: true }, { ...steps[1], done: true, to: '.fridolin-trash/20261020-100500/draft.md' }, steps[2]] };
    }
    if (path === '/api/runs/r1/files') {
      return undone
        ? { running: false, record: { ...record, undone: { at: '2026-10-20T10:05:00Z', by: 'dashboard' } }, plan: { run: 'r1', brain: 'taxes', dry_run: true, steps: steps.map((s) => ({ ...s, action: s.action === 'conflict' ? 'conflict' : 'none', why: s.why || 'it is as it was before the run' })) } }
        : { running: false, record, plan: { run: 'r1', brain: 'taxes', dry_run: true, steps } };
    }
    if (path.startsWith('/api/audit')) return [{ ts: '2026-10-20T10:01:00Z', kind: 'run_files', brain: 'taxes', run: 'r1', detail: '1 created, 2 changed: draft.md, notes.md, summary.md', outcome: 'ok' }];
    return {};
  }, { run: 'r1' });
  assert.match(root.textContent, /Files a run changed/);
  assert.match(root.textContent, /Undoing it would/);
  assert.match(root.textContent, /it changed since the run/);
  assert.match(root.textContent, /Ran alongside r2/);
  button(root, 'Undo this run').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.match(dialog.textContent, /2 files are written back/);
  dialog.querySelectorAll('button').find((b) => b.textContent === 'Undo').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'POST' && c.path === '/api/runs/r1/undo'));
  assert.match(root.textContent, /done: \.fridolin-trash\/20261020-100500\/draft\.md/);
  assert.match(root.textContent, /Undone/);
  assert.equal(button(root, 'Undo this run'), undefined, 'nothing is left to undo');
});

// A run without a record says so.
test('the Audit page of a run that changed no files', async (t) => {
  const { root } = await open(t, auditPage, (path) => {
    if (path === '/api/runs/r9/files') return { status: 404, body: { error: 'no record' } };
    if (path.startsWith('/api/audit')) return [];
    return {};
  }, { run: 'r9' });
  assert.match(root.textContent, /changed no files in its brain's folder/);
});

test('the Dev page votes in a poll', async (t) => {
  const { root, calls } = await open(t, devPage, (path) => {
    if (path.startsWith('/api/dev/outbox')) return [{ ts: '2026-10-20T10:00:00Z', jid: DM, text: '🔐 Approve? Submit', poll: ['✅ Yes', '❌ No'], id: 'dev-poll-1' }];
    if (path === '/api/dev/vote') return { status: 202, body: { status: 'accepted' } };
    return {};
  });
  t.mock.timers.tick(2000);
  await settle();
  root.querySelectorAll('ul.poll-options button')[1].click();
  await settle();
  assert.deepEqual(calls.find((c) => c.path === '/api/dev/vote').body, { poll: 'dev-poll-1', voter: '15551234567', options: ['❌ No'] });
});

test('the skills card approves a skill as declared', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const declared = { risk: 'irreversible', vault: [], approve: ['submit'], triggers: ['steuer'] };
  const read = { risk: 'read', vault: [], approve: [], triggers: [] };
  let status = 'new';
  const calls = serve(t, (path) => {
    if (path === '/api/brains/taxes/skills/approve') { status = 'approved'; return {}; }
    if (path === '/api/brains/taxes/skills') {
      return { risks: ['read', 'write', 'irreversible'], skills: [{ path: 'skills/taxes-de/SKILL.md', name: 'taxes-de', description: 'Taxes', status, hash: 'h1', fridolin: declared, effective: status === 'new' ? read : declared }] };
    }
    return {};
  });
  const el = skillsCard('taxes');
  document.body.append(el);
  await settle();
  assert.match(el.textContent, /not approved/);
  assert.match(el.textContent, /The file declares/);
  el.querySelectorAll('button').find((b) => b.textContent.includes('Approve as declared')).click();
  await settle();
  assert.deepEqual(calls.find((c) => c.path.endsWith('/approve')).body, { path: 'skills/taxes-de/SKILL.md', hash: 'h1' });
  assert.match(el.textContent, /approved/);
  assert.doesNotMatch(el.textContent, /The file declares/);
});
