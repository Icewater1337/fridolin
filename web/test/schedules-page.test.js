import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors, type } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import schedulesPage from '../assets/pages/schedules.js';
import { inTime, scheduleOf, taskKind, varsText } from '../assets/lib/schedules.js';

const DM = '15551234567@s.whatsapp.net';

async function open(t, route, params = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: Date.parse('2026-10-23T05:00:00Z') });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, route);
  const root = h('div');
  document.body.append(root);
  const navigated = [];
  const ctx = createPage({ params, navigate: (hash) => navigated.push(hash) });
  t.after(() => ctx.destroy());
  schedulesPage.render(root, ctx);
  await settle();
  return { root, calls, ctx, navigated };
}

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent.includes(label));

const template = { name: 'morning-briefing', description: "Today's appointments.", when: 'daily 06:00', report: 'worth', risk: 'read', vars: [{ name: 'greeting', default: 'Good morning', required: false }], optional: false };

function agent(over = {}) {
  return {
    brain: 'main', brain_name: 'Main', name: 'brief', kind: 'brain', task: 'template morning-briefing', report: 'worth', chat: DM,
    enabled: true, paused: false, running: false, next: '2026-10-23T06:00:00Z',
    last_run: { id: '20261022T060000-abcdef', status: 'ok', started: '2026-10-22T06:00:00Z' },
    schedule: { name: 'brief', when: 'daily 06:00', template: 'morning-briefing', report: 'worth' },
    ...over,
  };
}

function bridge(state) {
  return (path, init) => {
    const method = init.method || 'GET';
    if (path === '/api/schedules') return { enabled: state.enabled, agents: state.agents, reminders: state.reminders || [], templates: [template] };
    if (path === '/api/schedules/main/brief/run' && method === 'POST') { state.started = (state.started || 0) + 1; return { id: '20261023T050000-000001', status: 'running' }; }
    if (path === '/api/schedules/main/brief/pause' && method === 'POST') { state.agents[0].paused = true; return { paused: true, changed: true }; }
    if (path.startsWith('/api/schedules/main/brief/runs/') && path.endsWith('/log')) return { id: 'x', log: '[06:00:01] 🔧 Tool: Bash\n[06:00:09] ■ ok after 8s\n', cut: false, running: false };
    if (path.startsWith('/api/schedules/main/brief/runs')) {
      return [{ id: '20261022T060000-abcdef', trigger: 'schedule', started: '2026-10-22T06:00:00Z', duration_ms: 8000, status: 'ok', sent: true, sent_to: DM, cost_usd: 0.0123 }];
    }
    if (path === '/api/schedules/main/brief' && method === 'PUT') { state.put = JSON.parse(init.body); return { status: 'saved' }; }
    if (path === '/api/brains') return [{ id: 'main', name: 'Main' }];
    return {};
  };
}

// The list shows each agent with its schedule, state, next and last run,
// and Run now and Pause act on it; the reminders are listed below.
test('the Schedules page lists agents and runs and pauses them', async (t) => {
  const state = { enabled: true, agents: [agent()], reminders: [{ brain: 'main', id: 'i1', text: 'Call the dentist', when: '2026-10-24T09:00', next: '2026-10-24T09:00:00Z', chat: DM }] };
  const { root, calls } = await open(t, bridge(state));
  assert.match(root.textContent, /brief/);
  assert.match(root.textContent, /daily 06:00/);
  assert.match(root.textContent, /in 1 h/);
  assert.match(root.textContent, /Call the dentist/);
  button(root, 'Run now').click();
  await settle();
  assert.equal(state.started, 1);
  button(root, 'Pause').click();
  await settle();
  assert.ok(calls.find((c) => c.path === '/api/schedules/main/brief/pause'));
  assert.ok(button(root, 'Resume'), 'a paused agent offers Resume');
});

test('the Schedules page says when schedules are off', async (t) => {
  const { root } = await open(t, bridge({ enabled: false, agents: [] }));
  assert.match(root.textContent, /Scheduled agents are off/);
  assert.match(root.textContent, /No scheduled agents/);
});

// An agent's page shows its runs, a run's log, and saves an edit as the
// brain.json entry.
test('an agent shows its runs and log, and saves an edit', async (t) => {
  const state = { enabled: true, agents: [agent()] };
  const { root, ctx } = await open(t, bridge(state), { brain: 'main', name: 'brief', run: '20261022T060000-abcdef' });
  assert.match(root.textContent, /\$0\.0123/);
  assert.match(root.textContent, /Tool: Bash/);
  const when = root.querySelectorAll('input').find((i) => i.value === 'daily 06:00');
  type(when, 'daily 06:30');
  assert.equal(ctx.dirty(), true);
  button(root, 'Save').click();
  await settle();
  assert.deepEqual(state.put, { name: 'brief', when: 'daily 06:30', template: 'morning-briefing', report: 'worth' });
});

test('the schedule helpers', () => {
  assert.equal(taskKind({ command: { node: 'n', argv: ['x'] } }), 'command');
  assert.equal(taskKind({ prompt: 'x' }), 'prompt');
  assert.equal(taskKind({}), 'file');
  assert.equal(varsText({ a: '1', b: 'two' }), 'a=1\nb=two');
  const base = { name: 'x', when: 'every 6h', description: '', enabled: true, kind: 'template', template: 't', prompt: '', promptFile: '', node: '', argv: '', dir: '', vars: 'repos = a/b\n\n', risk: '', preapproved: 'file issues\n', githubRepos: 'a/b, c/d', githubPermissions: 'issues=write\ncontents=read', ownTools: false, tools: '', report: '', chat: '', timeout: '', missed: '', jitter: '' };
  assert.deepEqual(scheduleOf(base), { name: 'x', when: 'every 6h', template: 't', vars: { repos: 'a/b' }, preapproved: ['file issues'], github: { repos: ['a/b', 'c/d'], permissions: { issues: 'write', contents: 'read' } } });
  assert.deepEqual(scheduleOf({ ...base, kind: 'command', node: 'laptop', argv: '/bin/x\n--y', enabled: false, report: 'silent', jitter: '0' }),
    { name: 'x', when: 'every 6h', enabled: false, command: { node: 'laptop', argv: ['/bin/x', '--y'] }, report: 'silent', jitter_minutes: 0 });
  assert.throws(() => scheduleOf({ ...base, vars: 'nonsense' }), /name=value/);
  const now = Date.parse('2026-10-23T05:00:00Z');
  assert.equal(inTime('2026-10-23T05:00:30Z', now), 'now');
  assert.equal(inTime('2026-10-23T05:20:00Z', now), 'in 20 min');
  assert.equal(inTime('2026-10-23T07:05:00Z', now), 'in 2 h 5 min');
  assert.equal(inTime('2026-10-26T05:00:00Z', now), 'in 3 days');
});
