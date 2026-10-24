import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { setStatus } from '../assets/app/store.js';
import page from '../assets/pages/overview.js';
import { resource } from '../assets/app/resources.js';

async function openOverview(t, status, routes = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  serve(t, (path) => (path in routes ? routes[path] : path.startsWith('/api/activity') ? [] : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => { ctx.destroy(); setStatus(null); });
  setStatus({ whatsapp: { state: 'connected' }, brains: [], issues: [], warnings: [], claude: {}, sandbox: {}, ...status });
  page.render(root, ctx);
  await settle();
  return root;
}

test('daily backups without a Drive link say so instead of "not set up"', async (t) => {
  const root = await openOverview(t, { backup: { connected: false, enabled: true } });
  const text = root.textContent;
  assert.ok(text.includes('Not connected'), text);
  assert.ok(text.includes('Daily backups are on, but Google Drive is not connected'), text);
  assert.ok(!text.includes('Not set up'), text);
});

test('backups that were never set up are offered', async (t) => {
  const root = await openOverview(t, { backup: { connected: false, enabled: false } });
  const text = root.textContent;
  assert.ok(text.includes('Not set up'), text);
  assert.ok(!text.includes('Daily backups are on'), text);
});

/** @type {[{restoring?: boolean}, string][]} */
const jobs = [[{}, 'Backing up…'], [{ restoring: true }, 'Restoring…']];
for (const [job, want] of jobs) {
  test(`a running job reads "${want}"`, async (t) => {
    const root = await openOverview(t, { backup: { connected: true, enabled: true, running: true, ...job } });
    assert.ok(root.textContent.includes(want), root.textContent);
  });
}

test('a failed backup says whose it was', async (t) => {
  const root = await openOverview(t, {
    brains: [{ id: 'notes', name: 'Notes', routing: {} }],
    backup: { connected: true, enabled: true, last_error: 'quota exceeded', last_error_brain: 'notes' },
  });
  assert.ok(root.textContent.includes('Notes: quota exceeded'), root.textContent);
});

test('the CLI card follows the selected mode', async (t) => {
  const root = await openOverview(t, { agent_provider: 'codex', codex: { found: true, path: '/bin/codex', version: 'codex-cli 0.163.0' } });
  assert.ok(root.textContent.includes('Codex CLI'));
  assert.ok(root.textContent.includes('codex-cli 0.163.0'));
  assert.ok(!root.textContent.includes('Claude CLI'));
});

test('the housekeeping card shows the last dry run and what it would stop', async (t) => {
  const hk = {
    enabled: true, dry_run: true, every_hours: 48, running: false, asked: [],
    next_run: '2026-10-22T10:00:00Z', summary: 'dry run: 412 processes looked at; would stop or remove 1',
    counts: { would_stop: 1, problems: 0 },
    last: {
      started: '2026-10-20T10:00:00Z', trigger: 'schedule', dry_run: true, scanned: 412,
      findings: [{ kind: 'run-orphan', action: 'stop', pid: 4242, name: 'claude', members: [{ pid: 4242 }, { pid: 4243 }],
        rss: 300 * 1024 * 1024, age_seconds: 93600, owner: 'a run of brain main', reason: 'left running by a run of brain main that is over', outcome: 'would stop' }],
    },
  };
  const root = await openOverview(t, {}, { '/api/housekeeping': hk });
  await resource('/api/housekeeping').refresh();
  await settle();
  const text = root.textContent;
  assert.ok(text.includes('Housekeeping'), text);
  assert.ok(text.includes('Dry run'), text);
  assert.ok(text.includes('PID 4242'), text);
  assert.ok(text.includes('would stop'), text);
  assert.ok(text.includes('left running by a run of brain main'), text);
});

test('scheduled agents waiting for approval are flagged', async (t) => {
  const root = await openOverview(t, {}, {
    '/api/schedules': { agents: [
      { brain: 'main', name: 'review-bugs', state: 'pending', review: { status: 'new' } },
      { brain: 'main', name: 'brief', state: 'active' },
    ] },
  });
  const text = root.textContent;
  assert.ok(text.includes('1 scheduled agent waiting for your approval'), text);
  assert.ok(text.includes('review-bugs'), text);
  assert.ok(root.querySelectorAll('a').some((a) => a.getAttribute('href') === '#/schedules'));
});

test('a brain whose Google Calendar sign-in broke is named, with a link to Reconnect', async (t) => {
  const root = await openOverview(t, {}, {
    '/api/calendar/health': { brains: [
      { id: 'fam', name: 'Family', state: 'revoked', detail: 'Google no longer accepts this sign-in' },
      { id: 'work', name: 'Work', state: 'valid' },
    ] },
  });
  const text = root.textContent;
  assert.ok(text.includes('Google Calendar stopped working for Family'), text);
  assert.ok(!text.includes('Work'), 'a working calendar is not named');
  assert.ok(root.querySelectorAll('a').some((a) => a.getAttribute('href') === '#/brains/fam'), 'a link to the brain');
});

test('no warning while every calendar works', async (t) => {
  const root = await openOverview(t, {}, { '/api/calendar/health': { brains: [{ id: 'work', name: 'Work', state: 'expiring' }] } });
  assert.ok(!root.textContent.includes('Google Calendar stopped working'), root.textContent);
});
