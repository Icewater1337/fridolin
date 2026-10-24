import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors, type } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import schedulesPage from '../assets/pages/schedules.js';
import {
  fieldsOf, fileJSON, filterAgents, groupByBrain, inTime, permissionsOf, presetOf, scheduleOf, stateOf, taskKind, varsText, whenOf, zoneNote,
} from '../assets/lib/schedules.js';

test('zoneNote says the zone times of day are in, and warns about an unset one in UTC', () => {
  assert.equal(zoneNote(undefined), null);
  assert.deepEqual(zoneNote({ name: 'Europe/Zurich', source: 'config' }), { text: 'Times of day are Europe/Zurich.', warn: false });
  assert.equal(zoneNote({ name: 'UTC', source: 'process' }).warn, true);
  assert.match(zoneNote({ name: 'Europe/Zurich', source: 'process' }).text, /Europe\/Zurich \(the bridge’s; the time zone is unset/);
  assert.match(zoneNote({ name: 'Local', source: 'process' }).text, /the bridge’s own time zone/);
});

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
const link = (root, label) => root.querySelectorAll('a').find((a) => a.textContent.includes(label));

const templates = [
  { name: 'morning-briefing', description: "Today's appointments.", when: 'daily 06:00', report: 'worth', risk: 'read', vars: [{ name: 'greeting', default: 'Good morning', required: false }], optional: false },
  { name: 'code-review-bugs', description: 'Daily bug hunt.', when: 'daily 03:30', report: 'worth', risk: 'irreversible', github: { issues: 'write', contents: 'read' }, vars: [{ name: 'repos', required: true }, { name: 'max_issues', default: '3', required: false }], optional: true },
];

function agent(over = {}) {
  return {
    brain: 'main', brain_name: 'Main', name: 'brief', kind: 'brain', task: 'template morning-briefing', report: 'worth', chat: DM,
    state: 'active', enabled: true, paused: false, running: false, next: '2026-10-23T06:00:00Z', file: '.brain/schedules/brief.json', risky: [],
    last_run: { id: '20261022T060000-abcdef', status: 'ok', started: '2026-10-22T06:00:00Z', duration_ms: 8000 },
    schedule: { name: 'brief', when: 'daily 06:00', template: 'morning-briefing', report: 'worth' },
    ...over,
  };
}

const review = {
  name: 'review-bugs', state: 'pending', next: null, last_run: null, task: 'template code-review-bugs', file: '.brain/schedules/review-bugs.json',
  risky: ['may take irreversible steps (risk budget irreversible)', 'can write issues on GitHub in you/project'],
  schedule: { name: 'review-bugs', when: 'daily 03:30', template: 'code-review-bugs', risk: 'irreversible', github: { repos: ['you/project'], permissions: { issues: 'write' } } },
};
review.review = { name: 'review-bugs', status: 'new', source: review.file, disk: review.schedule };

function bridge(state) {
  return (path, init) => {
    const method = init.method || 'GET';
    if (path === '/api/schedules') return { enabled: state.enabled, pending: 0, agents: state.agents, reminders: state.reminders || [], templates };
    const run = /^\/api\/schedules\/main\/([a-z-]+)\/run$/.exec(path);
    if (run && method === 'POST') { state.started = [...(state.started || []), run[1]]; return { status: 200, body: { id: '20261023T050000-000001', status: 'running' } }; }
    if (path === '/api/schedules/main/brief/pause' && method === 'POST') { state.agents[0].paused = true; state.agents[0].state = 'paused'; return { paused: true, changed: true }; }
    const approve = /^\/api\/schedules\/main\/([a-z-]+)\/approve$/.exec(path);
    if (approve && method === 'POST') {
      state.approved = JSON.parse(init.body);
      const a = state.agents.find((x) => x.name === approve[1]);
      delete a.review;
      a.state = 'active';
      return { status: 200, body: { status: 'approved' } };
    }
    if (path.startsWith('/api/schedules/preview')) return { when: decodeURIComponent(path.split('when=')[1]), next: ['2026-10-24T06:00:00Z', '2026-10-25T06:00:00Z', '2026-10-26T06:00:00Z'] };
    if (path.startsWith('/api/schedules/main/brief/runs/') && path.endsWith('/log')) return { id: 'x', log: '[06:00:01] 🔧 Tool: Bash\n[06:00:09] ■ ok after 8s\n', cut: false, running: false };
    if (path.startsWith('/api/schedules/main/brief/runs')) {
      return [{ id: '20261022T060000-abcdef', trigger: 'schedule', started: '2026-10-22T06:00:00Z', duration_ms: 8000, status: 'ok', sent: true, sent_to: DM, cost_usd: 0.0123 }];
    }
    const putPath = /^\/api\/schedules\/([a-z]+)\/([a-z-]+)$/.exec(path);
    if (putPath && method === 'PUT') { state.put = { brain: putPath[1], body: JSON.parse(init.body) }; return { status: 200, body: { status: 'saved' } }; }
    if (putPath && method === 'DELETE') { state.deleted = putPath[2]; return { status: 200, body: { status: 'removed' } }; }
    if (path === '/api/brains') return [{ id: 'main', name: 'Main' }, { id: 'work', name: 'Work' }];
    if (path === '/api/github') return { connected: true, installations: [{ account: 'you', repos: ['you/project', 'you/other'] }] };
    if (path === '/api/nodes') return { nodes: [{ id: 'n1', name: 'mac-mini', online: true, declared: { exec: { allow: [], accept_from_core: ['/Users/you/tools'] } }, effective: { exec: { allow: [{ name: '/opt/bin/ok', args: [] }], accept_from_core: ['/Users/you/tools'] } } }] };
    return {};
  };
}

// The list groups the agents by brain, each with its state, next and last
// run and what it may do; Run now and Pause act on it; the reminders,
// the brain's own file's included, are listed below.
test('the Schedules page lists agents by brain and runs and pauses them', async (t) => {
  const state = {
    enabled: true,
    agents: [agent(), agent({ brain: 'work', brain_name: 'Work', name: 'scrape', state: 'failing', task: 'runs x on node mac-mini', schedule: { name: 'scrape', when: 'every 6h', command: { node: 'mac-mini', argv: ['/bin/x'] } }, program: { node: 'n1', node_name: 'mac-mini', state: 'not-accepted', detail: '/bin/x is in none of the folders node mac-mini accepts programs from (/Users/you/tools)' }, last_run: { status: 'failed', started: '2026-10-23T00:00:00Z' } })],
    reminders: [
      { brain: 'main', id: 'i1', text: 'Call the dentist', when: '2026-10-24T09:00', next: '2026-10-24T09:00:00Z', chat: DM, source: 'bridge' },
      { brain: 'main', id: 'skills/reminders.json#54', text: 'Stretch', when: 'daily 20:00', next: '2026-10-23T20:00:00Z', chat: '', source: 'skills/reminders.json' },
    ],
  };
  const { root, calls } = await open(t, bridge(state));
  const text = root.textContent;
  assert.match(text, /Main/);
  assert.match(text, /Work/);
  assert.match(text, /daily 06:00/);
  assert.match(text, /Active/);
  assert.match(text, /Failing/);
  assert.match(text, /in 1 h/, 'the next run, relative');
  assert.match(text, /took 8 s|took 8s/);
  assert.match(text, /node mac-mini/);
  assert.match(text, /\/bin\/x is in none of the folders node mac-mini accepts/, 'a command agent says why its node will not run it');
  assert.match(text, /Call the dentist/);
  assert.match(text, /Stretch/);
  assert.match(text, /skills\/reminders\.json/);
  assert.equal(root.querySelectorAll('button').filter((b) => b.textContent.includes('Remove')).length, 1, 'only the bridge’s own reminder can be removed here');
  button(root, 'Run now').click();
  await settle();
  assert.deepEqual(state.started, ['brief']);
  button(root, 'Pause').click();
  await settle();
  assert.ok(calls.find((c) => c.path === '/api/schedules/main/brief/pause'));
  assert.ok(button(root, 'Resume'), 'a paused agent offers Resume');
});

// An agent whose file waits for approval comes first, with what it may
// do, and is approved inline as it was reviewed; Approve & run now runs
// it after.
test('a pending agent is reviewed and approved inline', async (t) => {
  const state = { enabled: true, agents: [agent(), agent({ ...review })] };
  const { root } = await open(t, bridge(state));
  assert.match(root.textContent, /Waiting for your approval \(1\)/);
  assert.match(root.textContent, /Pending approval/);
  assert.match(root.textContent, /can write issues on GitHub in you\/project/);
  assert.match(root.textContent, /after approval/);
  button(root, 'Approve & run now').click();
  await settle();
  // Risky: it asks first.
  const confirm = document.body.querySelector('dialog').querySelectorAll('button').find((b) => b.textContent === 'Approve');
  assert.ok(confirm, 'a risky agent asks before it is approved');
  confirm.click();
  await settle(60);
  assert.deepEqual(state.approved, { schedule: review.schedule });
  assert.deepEqual(state.started, ['review-bugs']);
  assert.doesNotMatch(root.textContent, /Waiting for your approval/);
});

test('the Schedules page filters by brain and state', async (t) => {
  const state = { enabled: true, agents: [agent(), agent({ ...review }), agent({ brain: 'work', brain_name: 'Work', name: 'other' })] };
  const { root } = await open(t, bridge(state));
  const [brainSel, stateSel] = root.querySelectorAll('.agent-filters select');
  stateSel.value = 'pending';
  stateSel.dispatchEvent(new Event('change'));
  let cards = root.querySelectorAll('.agent-list .agent-card');
  assert.equal(cards.length, 1);
  assert.match(cards[0].textContent, /review-bugs/);
  stateSel.value = '';
  stateSel.dispatchEvent(new Event('change'));
  brainSel.value = 'work';
  brainSel.dispatchEvent(new Event('change'));
  cards = root.querySelectorAll('.agent-list .agent-card');
  assert.equal(cards.length, 1);
  assert.match(cards[0].textContent, /other/);
});

test('the Schedules page says when schedules are off, and what to do without agents', async (t) => {
  const { root } = await open(t, bridge({ enabled: false, agents: [] }));
  assert.match(root.textContent, /Scheduled agents are off/);
  assert.match(root.textContent, /No scheduled agents yet/);
  assert.ok(root.querySelectorAll('a').filter((a) => a.getAttribute('href') === '#/schedules/new').length >= 2);
});

// An agent's page shows every field, its runs and a run's log, and runs
// it now.
test('an agent shows everything about it, its runs and log', async (t) => {
  const state = { enabled: true, agents: [agent({ schedule: { name: 'brief', when: 'daily 06:00', template: 'morning-briefing', report: 'worth', vars: { greeting: 'Hi' }, timeout_minutes: 5 } })] };
  const { root } = await open(t, bridge(state), { brain: 'main', name: 'brief', run: '20261022T060000-abcdef' });
  const text = root.textContent;
  assert.match(text, /\$0\.0123/);
  assert.match(text, /Tool: Bash/);
  assert.match(text, /\.brain\/schedules\/brief\.json/);
  assert.match(text, /greeting=Hi/);
  assert.match(text, /5 min/);
  assert.ok(link(root, 'Edit'), 'it can be edited');
  assert.ok(link(root, 'Duplicate'));
  button(root, 'Run now').click();
  await settle();
  assert.deepEqual(state.started, ['brief']);
});

// The wizard: a template, a brain and name, when (with the next runs),
// its vars with a repo picker from GitHub, what it may do, and the exact
// file; saving writes it.
test('the wizard adds an agent step by step', async (t) => {
  const state = { enabled: true, agents: [] };
  const { root, ctx, navigated, calls } = await open(t, bridge(state), { brain: 'new' });
  assert.match(root.textContent, /What should it do\?/);
  assert.match(root.textContent, /Run a command on a node/);
  root.querySelectorAll('.template-card').find((b) => b.textContent.includes('Code review: bugs')).click();
  await settle();
  // Brain and name, the template's name suggested.
  assert.match(root.textContent, /Which brain runs it\?/);
  const name = root.querySelectorAll('input').find((i) => i.value === 'review-bugs');
  assert.ok(name, 'the name is suggested');
  const brainSel = root.querySelector('.agent-wizard select');
  brainSel.value = 'work';
  brainSel.dispatchEvent(new Event('change'));
  assert.equal(ctx.dirty(), true);
  button(root, 'Next').click();
  await settle();
  // When: the template's daily 03:30, with the next runs.
  assert.match(root.textContent, /When does it run\?/);
  t.mock.timers.tick(300);
  await settle();
  assert.ok(calls.find((c) => c.path === '/api/schedules/preview?when=daily%2003%3A30'), calls.map((c) => c.path).join(' '));
  assert.equal(root.querySelectorAll('.next-runs li').length, 3);
  button(root, 'Next').click();
  await settle();
  // Details: repos is needed; picked from the GitHub app's repositories.
  assert.match(root.textContent, /Settings of code-review-bugs/);
  button(root, 'Next').click();
  await settle();
  assert.match(root.textContent, /Fill in repos/);
  const box = root.querySelectorAll('.repo-list input').find((i) => i.parentNode.textContent.includes('you/project'));
  box.checked = true;
  box.dispatchEvent(new Event('change'));
  button(root, 'Next').click();
  await settle();
  // Permissions in plain words.
  assert.match(root.textContent, /What may it do\?/);
  assert.match(root.textContent, /GitHub: issues write, contents read on you\/project/);
  button(root, 'Next').click();
  await settle();
  // The exact file.
  assert.match(root.textContent, /\.brain\/schedules\/review-bugs\.json/);
  const json = root.querySelector('.agent-json').textContent;
  assert.match(json, /"template": "code-review-bugs"/);
  button(root, 'Save and approve').click();
  await settle(40);
  assert.equal(state.put.brain, 'work');
  assert.deepEqual(state.put.body, {
    name: 'review-bugs', when: 'daily 03:30', description: 'Daily bug hunt.', template: 'code-review-bugs',
    vars: { max_issues: '3', repos: 'you/project' }, risk: 'irreversible', report: 'worth',
    github: { repos: ['you/project'], permissions: { issues: 'write', contents: 'read' } },
  });
  assert.equal(JSON.stringify(JSON.parse(json)), JSON.stringify(JSON.parse(fileJSON(state.put.body))));
  assert.deepEqual(navigated, ['#/schedules/work/review-bugs']);
});

// A command's program: the wizard says whether its node runs it, whether
// saving adds it, or what the node must opt into first.
test('the wizard says how a program stands on its node', async (t) => {
  const state = { enabled: true, agents: [] };
  const { root } = await open(t, bridge(state), { brain: 'new' });
  root.querySelectorAll('.template-card').find((b) => b.textContent.includes('Run a command on a node')).click();
  await settle();
  root.querySelectorAll('.step-btn').find((b) => b.textContent.includes('Details')).click();
  await settle();
  const sel = root.querySelector('.agent-wizard select');
  sel.value = 'mac-mini';
  sel.dispatchEvent(new Event('change'));
  const argv = root.querySelector('.agent-wizard textarea');
  const status = () => root.querySelector('.program-status').textContent;
  /** @type {[string, RegExp][]} */
  const cases = [
    ['/Users/you/tools/daily.sh\n--quiet', /Saving adds it to node mac-mini.*exactly these arguments/],
    ['/opt/bin/ok', /Node mac-mini runs it.*node\.json allows/],
    ['/usr/local/bin/x', /would not run it.*in none of the folders/],
  ];
  for (const [typed, want] of cases) {
    argv.value = typed;
    argv.dispatchEvent(new Event('input'));
    assert.match(status(), want, typed);
  }
});

test('the wizard edits an agent and writes what changed', async (t) => {
  const state = { enabled: true, agents: [agent()] };
  const { root } = await open(t, bridge(state), { brain: 'main', name: 'brief', run: 'edit' });
  assert.match(root.textContent, /When does it run\?/);
  const time = root.querySelectorAll('input').find((i) => i.getAttribute('type') === 'time' && i.value === '06:00');
  type(time, '06:30');
  root.querySelectorAll('.stepper button').find((b) => b.textContent.includes('Review')).click();
  await settle();
  button(root, 'Save changes').click();
  await settle();
  assert.deepEqual(state.put.body, { name: 'brief', when: 'daily 06:30', template: 'morning-briefing', report: 'worth' });
});

test('the wizard duplicates an agent under another name', async (t) => {
  const state = { enabled: true, agents: [agent()] };
  const { root } = await open(t, bridge(state), { brain: 'main', name: 'brief', run: 'duplicate' });
  root.querySelectorAll('.stepper button').find((b) => b.textContent.includes('Review')).click();
  await settle();
  button(root, 'Save and approve').click();
  await settle();
  assert.equal(state.put.body.name, 'brief-copy');
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
  assert.throws(() => scheduleOf({ ...base, name: 'Bad Name' }), /name/);
  assert.deepEqual(scheduleOf({ ...base, vars: { repos: 'a/b', empty: ' ' } }).vars, { repos: 'a/b' });
  // fieldsOf is scheduleOf's inverse.
  for (const s of [{ name: 'a', when: 'daily 07:00', prompt: 'p', risk: 'read', timeout_minutes: 5, jitter_minutes: 0 }, { name: 'b', when: 'every 2h', command: { node: 'n', argv: ['x', 'y'], dir: '/d' }, enabled: false }]) {
    assert.deepEqual(scheduleOf(fieldsOf(s)), s);
  }
  // The file as Go writes it: its fields in order, maps sorted.
  assert.equal(fileJSON({ github: { permissions: { issues: 'write', contents: 'read' }, repos: ['a/b'] }, when: 'daily 07:00', name: 'x' }),
    '{\n  "name": "x",\n  "when": "daily 07:00",\n  "github": {\n    "repos": [\n      "a/b"\n    ],\n    "permissions": {\n      "contents": "read",\n      "issues": "write"\n    }\n  }\n}\n');
  for (const w of ['every 6h', 'daily 07:00', 'weekly mon 09:00', 'cron 0 7 * * 1-5', 'every 30m']) assert.equal(whenOf(presetOf(w)), w);
  assert.deepEqual(presetOf('weekly fri 9:05'), { mode: 'weekly', day: 'fri', time: '09:05' });
  const agents = [{ brain: 'a', name: 'x', state: 'active' }, { brain: 'b', name: 'y', state: 'pending', task: 'review' }, { brain: 'a', name: 'z', state: 'active', review: { status: 'changed' } }];
  assert.deepEqual(filterAgents(agents, { state: 'pending' }).map((a) => a.name), ['y', 'z']);
  assert.deepEqual(filterAgents(agents, { q: 'REV' }).map((a) => a.name), ['y']);
  assert.deepEqual(groupByBrain(agents).map((g) => [g.brain, g.agents.length]), [['a', 2], ['b', 1]]);
  assert.equal(stateOf({ state: 'failing' }).kind, 'bad');
  assert.match(permissionsOf({ command: { node: 'n', argv: ['/bin/x'] }, report: 'silent' }).join(' '), /Runs \/bin\/x on node n.*none sent/);
  assert.match(permissionsOf({ risk: 'read', preapproved: ['p'] }, DM).join(' '), /Risk read.*Approved with it.*15551234567/);
  const now = Date.parse('2026-10-23T05:00:00Z');
  assert.equal(inTime('2026-10-23T05:00:30Z', now), 'now');
  assert.equal(inTime('2026-10-23T05:20:00Z', now), 'in 20 min');
  assert.equal(inTime('2026-10-23T07:05:00Z', now), 'in 2 h 5 min');
  assert.equal(inTime('2026-10-26T05:00:00Z', now), 'in 3 days');
});
