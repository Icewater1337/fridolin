// Schedules: every brain's scheduled agents, grouped by brain, each with
// its state (pending approval, active, paused, failing, disabled), its
// next run, its last run and what it may do, with Run now, Pause and
// Resume; the agents that wait for approval come first, each with Review
// & approve. #/schedules/new adds one with the wizard;
// #/schedules/<brain>/<agent> shows everything about one, its runs and
// its review, /edit and /duplicate take it through the wizard, and
// /<run> shows a run's log. The reminders are listed below the agents.

import { del, get, post, put } from '../app/api.js';
import { brainsResource, nodesResource, resource, schedulesResource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { fieldDiff, lineDiff, sortKeys } from '../lib/diff.js';
import { fmtDuration, prettyJSON } from '../lib/format.js';
import { STATES, fileJSON, filterAgents, groupByBrain, inTime, permissionsOf, runTime, stateOf, varsText } from '../lib/schedules.js';
import { agentWizard } from '../components/agent-wizard.js';
import { btn } from '../components/buttons.js';
import { badge, callout, empty, kv, mono, pageHeader, spinner, timeAgo } from '../components/common.js';
import { confirmDialog } from '../components/dialog.js';
import { field, select, textInput } from '../components/fields.js';
import { icon } from '../components/icons.js';
import { liveForm } from '../components/live-form.js';
import { outcomeBadge, riskBadge, threadEl } from '../components/policy.js';
import { toast, toastError } from '../components/toast.js';

const githubResource = resource('/api/github');
const enc = encodeURIComponent;
const base = (brain, name) => `/api/schedules/${enc(brain)}/${enc(name)}`;
const link = (brain, name, run) => `#/schedules/${enc(brain)}/${enc(name)}${run ? `/${enc(run)}` : ''}`;
const REVIEW_TITLE = { new: 'New agent, waiting for your approval', changed: 'Its file changed: the change waits for your approval', removed: 'Its file is gone: its removal waits for your approval', invalid: 'Its file cannot be used as it is' };

function stateBadge(a) {
  const s = stateOf(a);
  return badge(s.label, s.kind);
}

async function act(path, done, body) {
  try {
    await post(path, body);
    toast(done, 'ok', { timeout: 2500 });
    await schedulesResource.refresh();
    return true;
  } catch (e) {
    toastError(e);
    return false;
  }
}

// An agent just approved runs once its brain took up the new approval,
// which follows the save by a moment: Run now waits for it.
async function runSoon(a) {
  for (let i = 0; i < 10; i += 1) {
    try {
      await post(`${base(a.brain, a.name)}/run`);
      toast(`Started ${a.name}`, 'ok', { timeout: 2500 });
      await schedulesResource.refresh();
      return;
    } catch (e) {
      if (e.status !== 404 || i === 9) { toastError(e); return; }
      await new Promise((r) => { setTimeout(r, 500); });
    }
  }
}

async function removeAgent(a, after) {
  const pending = a.state === 'pending';
  const ok = await confirmDialog({
    title: `Delete ${a.name}?`,
    message: pending
      ? `Its file ${a.file} goes from brain ${a.brain}'s folder; it never ran.`
      : `Its file ${a.file || ''} goes from brain ${a.brain}'s folder and it stops running. Its history and logs stay until they age out.`,
    confirm: 'Delete', danger: true,
  });
  if (!ok) return;
  try {
    await del(base(a.brain, a.name));
    toast(`Deleted ${a.name}`, 'ok', { timeout: 2500 });
    await schedulesResource.refresh();
    if (after) after();
  } catch (e) { toastError(e); }
}

// How a command agent's badge looks, by how its program stands on its node.
const PROGRAM_TONE = { allowed: 'ok', waiting: 'info', refused: 'bad', 'not-accepted': 'bad', 'no-node': 'bad' };

/** How a's program stands on its node, when that needs saying: a line for its card. */
function programNote(a) {
  const p = a.program;
  if (!p || p.state === 'allowed' || a.state === 'pending') return null;
  return h(`p.small.${p.state === 'waiting' ? 'muted' : 'warn-text'}`, p.detail);
}

/** What agent a may do, as small badges: risk, node, GitHub, results. */
function mayDo(a) {
  const s = a.schedule || {};
  const out = [];
  if (s.command) out.push(badge(`node ${s.command.node}`, PROGRAM_TONE[a.program && a.program.state] || 'warn', a.program ? a.program.detail : 'Runs a program on this node'));
  else out.push(riskBadge(s.risk || 'write'));
  if (s.github) {
    const perms = Object.entries(s.github.permissions || {}).map(([k, v]) => `${k}:${v}`).join(' ');
    out.push(badge(`GitHub ${(s.github.repos || []).join(', ')}`, Object.values(s.github.permissions || {}).includes('write') ? 'warn' : 'neutral', perms));
  }
  if ((s.preapproved || []).length) out.push(badge(`${s.preapproved.length} preapproved`, 'neutral', s.preapproved.join('\n')));
  out.push(a.report === 'silent' ? badge('silent', 'neutral', 'Sends nothing') : h('span.small.muted', a.report === 'always' ? 'always → ' : 'when worth it → ', a.chat ? threadEl(a.chat) : 'no chat'));
  return h('div.agent-may', out);
}

/** The run facts of agent a: when, next run, last run. */
function facts(a) {
  const last = a.last_run;
  let next = h('span.muted', '—');
  if (a.state === 'pending') next = h('span.muted', 'after approval');
  else if (a.next) next = h('span', { title: runTime(a.next) }, h('strong', inTime(a.next)), h('span.muted', ` · ${runTime(a.next)}`));
  else if (a.paused) next = h('span.muted', 'paused');
  else if (!a.enabled) next = h('span.muted', 'only Run now');
  return h('dl.agent-facts',
    h('div', h('dt', 'When'), h('dd', mono(a.schedule.when || '?'))),
    h('div', h('dt', 'Next run'), h('dd', next)),
    h('div', h('dt', 'Last run'), h('dd', last
      ? [outcomeBadge(last.status), ' ', timeAgo(last.started), last.duration_ms ? h('span.muted', ` · took ${fmtDuration(last.duration_ms / 1000)}`) : null]
      : h('span.muted', 'never'))));
}

/** The review of a's pending change, with Approve, Approve & run now and Delete. */
function reviewPanel(a, { after } = {}) {
  const r = a.review;
  const parts = [h('div.card-title', icon('alert'), h('h3', REVIEW_TITLE[r.status] || 'Waiting for approval'))];
  parts.push(h('p.muted.small', 'In ', mono(r.source || a.file), ' of brain ', mono(a.brain), '. ',
    r.status === 'new' ? 'It runs only once you approve this version.' : r.status === 'invalid' ? 'Fix the file, or delete it.' : 'The version you approved runs until you approve this one.'));
  if (r.status === 'invalid') {
    parts.push(callout('bad', 'Why', r.detail || 'It cannot be read.'));
    parts.push(h('div.actions', btn('Delete the file', { kind: 'danger', icon: 'trash', onClick: () => removeAgent(a, after) })));
    return h('section.review-panel', parts);
  }
  const disk = r.disk || null;
  if (a.risky && a.risky.length && r.status !== 'removed') {
    parts.push(callout('warn', 'Look closely: this agent may', h('ul.plain-list', a.risky.map((x) => h('li', x)))));
  }
  if (disk) parts.push(h('h4', 'What it may do'), h('ul.plain-list', permissionsOf(disk, a.chat).map((x) => h('li', x))));
  if (a.program && r.status !== 'removed') {
    const p = a.program;
    parts.push(callout(p.state === 'allowed' || p.state === 'waiting' ? 'info' : 'warn', p.state === 'waiting' ? 'Approving adds its program to the node' : `Its program on node ${p.node_name || disk.command.node}`, p.detail));
  }
  if (r.status === 'changed') {
    const rows = fieldDiff(r.approved || {}, disk);
    parts.push(h('h4', 'The change'), rows.length ? h('div.table-wrap', h('table.table.diff-table',
      h('thead', h('tr', h('th', 'Field'), h('th', 'Approved'), h('th', 'In the file'))),
      h('tbody', rows.map((x) => h('tr', h('td', mono(x.path)), h('td.diff-old', h('code.mono', x.a)), h('td.diff-new', h('code.mono', x.b))))))) : h('p.muted', 'Only formatting differs.'));
    const { left, right } = lineDiff(prettyJSON(sortKeys(r.approved || {})), prettyJSON(sortKeys(disk)));
    parts.push(h('details.details', h('summary', 'Side-by-side JSON'),
      h('div.diff-cols',
        h('div', h('div.diff-label', 'Approved'), h('pre.code.diff', left.map((l) => h(`span.diff-line${l.changed ? '.del' : ''}`, l.text)))),
        h('div', h('div.diff-label', 'In the file'), h('pre.code.diff', right.map((l) => h(`span.diff-line${l.changed ? '.add' : ''}`, l.text)))))));
  } else if (r.status === 'removed') {
    parts.push(h('p', 'Approving stops it: ', mono(a.schedule.when), ', ', a.task, '.'));
  } else {
    parts.push(h('details.details', h('summary', 'The file'), h('pre.code.agent-json', fileJSON(disk))));
  }
  const approve = async (andRun) => {
    if (a.risky && a.risky.length && r.status !== 'removed') {
      const ok = await confirmDialog({ title: `Approve ${a.name}?`, message: `It may: ${a.risky.join('; ')}. Approve it only if you or someone you trust wrote it.`, confirm: 'Approve' });
      if (!ok) return;
    }
    try {
      await post(`${base(a.brain, a.name)}/approve`, { schedule: r.status === 'removed' ? null : disk });
    } catch (e) {
      if (e.status === 409) { toast(e.message, 'warn'); await schedulesResource.refresh(); return; }
      toastError(e);
      return;
    }
    toast(r.status === 'removed' ? `${a.name} is removed` : `${a.name} is approved`, 'ok', { timeout: 2500 });
    await schedulesResource.refresh();
    if (andRun) await runSoon(a);
    if (after && r.status === 'removed') after();
  };
  parts.push(h('div.actions',
    btn(r.status === 'removed' ? 'Approve the removal' : 'Approve', { kind: 'primary', icon: 'check', onClick: () => approve(false) }),
    r.status !== 'removed' ? btn('Approve & run now', { icon: 'play', onClick: () => approve(true) }) : null,
    r.status !== 'removed' ? btn('Delete the file', { kind: 'danger', icon: 'trash', onClick: () => removeAgent(a, after) }) : null));
  return h('section.review-panel', parts);
}

/** The buttons of agent a: Run now, Pause or Resume, Edit, Duplicate, Delete. */
function agentActions(a, { onDeleted, small = true } = {}) {
  const pendingNew = a.state === 'pending';
  return h('div.actions',
    pendingNew ? null : btn('Run now', { small, kind: 'primary', icon: 'play', disabled: a.running, title: a.running ? 'Its run is still going' : 'Start a run now, whatever its schedule', onClick: () => act(`${base(a.brain, a.name)}/run`, `Started ${a.name}`) }),
    pendingNew ? null : (a.paused
      ? btn('Resume', { small, icon: 'refresh', onClick: () => act(`${base(a.brain, a.name)}/resume`, `Resumed ${a.name}`) })
      : btn('Pause', { small, icon: 'pause', onClick: () => act(`${base(a.brain, a.name)}/pause`, `Paused ${a.name}`) })),
    a.review || a.problem && pendingNew ? null : btn('Edit', { small, icon: 'edit', href: link(a.brain, a.name, 'edit') }),
    a.review && a.review.status === 'invalid' ? null : btn('Duplicate', { small, icon: 'copy', href: link(a.brain, a.name, 'duplicate') }),
    a.from_memory ? null : btn('Delete…', { small, kind: 'ghost', icon: 'trash', onClick: () => removeAgent(a, onDeleted) }));
}

/** One agent of the list. */
function agentCard(open, toggleOpen) {
  return (a) => {
    const reviewing = a.review && open.has(`${a.brain}/${a.name}`);
    return h(`article.card.agent-card.state-${a.state}`,
      h('div.agent-head',
        h('div.agent-title',
          h('a', { href: link(a.brain, a.name) }, h('strong', a.name)),
          stateBadge(a),
          a.running ? badge('running', 'info') : null,
          a.review && a.state !== 'pending' ? badge('change pending', 'warn') : null,
          a.from_memory ? badge('memory.consolidate', 'neutral') : null),
        a.review && a.review.status !== 'invalid'
          ? btn(reviewing ? 'Hide review' : 'Review & approve', { small: true, kind: reviewing ? '' : 'primary', icon: 'check', onClick: () => toggleOpen(a) })
          : null),
      h('p.muted.small', a.schedule.description || a.task),
      facts(a),
      mayDo(a),
      programNote(a),
      a.problem ? h('p.small.warn-text', a.problem) : null,
      reviewing ? reviewPanel(a) : null,
      agentActions(a));
  };
}

function reminderRow(r) {
  return h('tr',
    h('td', r.text),
    h('td', mono(r.brain)),
    h('td', mono(r.when)),
    h('td', r.next ? h('span', { title: runTime(r.next) }, inTime(r.next)) : h('span.muted', 'done')),
    h('td', r.chat ? threadEl(r.chat) : h('span.muted', 'the brain’s chat')),
    h('td', r.source === 'bridge' ? h('span.muted.small', 'standing intent') : mono(r.source)),
    h('td.right', r.source === 'bridge' ? btn('Remove', { small: true, kind: 'danger', icon: 'trash', onClick: async () => {
      const ok = await confirmDialog({ title: 'Remove this reminder?', message: r.text, confirm: 'Remove', danger: true });
      if (!ok) return;
      try {
        await del(`/api/brains/${enc(r.brain)}/intents/${enc(r.id)}`);
        await schedulesResource.refresh();
      } catch (e) { toastError(e); }
    } }) : null));
}

function renderList(root, ctx) {
  const offNote = h('div');
  const pendingBox = h('div');
  const filters = { brain: '', state: '', q: '' };
  const brainSel = select([['', 'All brains']], '');
  const stateSel = select([['', 'All states'], ...STATES.map(([k, label]) => [k, label])], '');
  const search = textInput({ type: 'search', placeholder: 'Search agents' });
  const toolbar = h('div.toolbar.agent-filters', field({ label: 'Brain', control: brainSel }), field({ label: 'State', control: stateSel }), field({ label: 'Search', control: search }));
  const groupsBox = h('div', spinner());
  const remindersBox = h('div');
  const open = new Set();
  let data = null;

  mount(root,
    pageHeader('Schedules', 'Each brain’s scheduled agents: tasks a brain runs by itself on a schedule, or programs on your machines. Each is a file in the brain’s .brain/schedules folder, and runs once you approved it.',
      btn('Add agent', { kind: 'primary', icon: 'plus', href: '#/schedules/new' })),
    offNote, pendingBox,
    h('section.section', h('h2.section-title', 'Agents'), toolbar, groupsBox),
    h('section.section', h('h2.section-title', 'Reminders'), h('p.muted.small', 'Reminders with a time that the brains keep: the bridge’s standing intents, and those in a brain’s own reminders file, which the brain fires itself.'), remindersBox));

  const toggleOpen = (a) => {
    const key = `${a.brain}/${a.name}`;
    if (open.has(key)) open.delete(key); else open.add(key);
    paintAgents();
  };
  const card = agentCard(open, toggleOpen);
  const groups = new Map(); // brain → its section's body

  function paintAgents() {
    if (!data) return;
    const agents = data.agents || [];
    if (!agents.length) {
      groups.clear();
      paint(groupsBox, 'none', () => h('div.card', empty('No scheduled agents yet',
        'An agent is a task a brain runs by itself — a morning briefing, a weekly memory clean-up, a daily code review — or a program on one of your machines. Add one: the wizard walks you through what it does, when, and what it may do.',
        btn('Add agent', { kind: 'primary', icon: 'plus', href: '#/schedules/new' }))));
      return;
    }
    const shown = filterAgents(agents, filters);
    const grouped = groupByBrain(shown);
    if (!grouped.length) {
      groups.clear();
      paint(groupsBox, 'nomatch', () => h('div.card', empty('No agent matches', 'Change the filters above to see the others.')));
      return;
    }
    paint(groupsBox, `groups:${grouped.map((g) => g.brain).join(',')}`, () => {
      groups.clear();
      return grouped.map((g) => {
        const body = h('div.agent-list');
        groups.set(g.brain, { body, head: h('div.agent-group-head') });
        return h('section.agent-group', groups.get(g.brain).head, body);
      });
    });
    for (const g of grouped) {
      const { body, head } = groups.get(g.brain);
      const pending = g.agents.filter((a) => a.review && a.review.status !== 'invalid').length;
      paint(head, `${g.name}|${g.agents.length}|${pending}`, () => [
        h('h3', g.name, ' ', h('code.mono.muted', g.brain)),
        h('span.muted.small', `${g.agents.length} agent${g.agents.length === 1 ? '' : 's'}`),
        pending ? badge(`${pending} waiting for approval`, 'warn') : null]);
      reconcile(body, g.agents, (a) => `${a.brain}/${a.name}|${open.has(`${a.brain}/${a.name}`)}|${JSON.stringify(a)}`, card);
    }
  }

  brainSel.addEventListener('change', () => { filters.brain = brainSel.value; paintAgents(); });
  stateSel.addEventListener('change', () => { filters.state = stateSel.value; paintAgents(); });
  search.addEventListener('input', () => { filters.q = search.value; paintAgents(); });

  ctx.follow(schedulesResource, (d) => {
    if (!d) return;
    data = d;
    paint(offNote, d.enabled, (on) => (on ? null : callout('warn', 'Scheduled agents are off', h('p', 'schedules.enabled is off in the settings: no agent runs on its schedule. Run now still works.'))));
    const agents = d.agents || [];
    const brains = groupByBrain(agents).map((g) => [g.brain, g.name]);
    paint(brainSel, JSON.stringify(brains), () => [h('option', { value: '' }, 'All brains'), ...brains.map(([id, name]) => h('option', { value: id }, name))]);
    brainSel.value = filters.brain;
    const pending = agents.filter((a) => a.review && a.review.status !== 'invalid');
    paint(pendingBox, pending.map((a) => `${a.brain}/${a.name}:${JSON.stringify(a.review)}`).join(','), () => (pending.length
      ? h('section.section.pending-agents',
        h('h2.section-title', `Waiting for your approval (${pending.length})`),
        h('p.muted', 'These agents’ files were written or changed outside this page — by a brain, or by hand. Check what each may do, then approve it.'),
        pending.map((a) => h('article.card.agent-card.state-pending', h('div.agent-title', h('a', { href: link(a.brain, a.name) }, h('strong', a.name)), mono(a.brain), stateBadge(a)), facts(a), reviewPanel(a))))
      : null));
    paintAgents();
    const reminders = d.reminders || [];
    paint(remindersBox, JSON.stringify(reminders), () => (reminders.length
      ? h('div.card.flush.table-wrap', h('table.table',
        h('thead', h('tr', h('th', 'Reminder'), h('th', 'Brain'), h('th', 'When'), h('th', 'Next'), h('th', 'Chat'), h('th', 'Kept in'), h('th.right', ''))),
        h('tbody', reminders.map(reminderRow))))
      : h('div.card', empty('No reminders', 'A brain sets one when you ask it to remind you of something at a time; it shows here until it fired.'))));
  }, 5000);
}

function runRow(brain, name, selected) {
  return (r) => {
    let cost = '';
    if (r.cost_usd) cost = `$${r.cost_usd.toFixed(4)}`;
    else if (r.input_tokens || r.output_tokens) cost = `${r.input_tokens || 0} + ${r.output_tokens || 0} tokens`;
    else if (r.exit_code !== undefined && r.exit_code !== null) cost = `exit ${r.exit_code}`;
    return h(`tr${r.id === selected ? '.selected' : ''}`,
      h('td', h('a', { href: link(brain, name, r.id) }, timeAgo(r.started)), h('div.muted.small', r.trigger === 'manual' ? `by hand (${r.by || 'dashboard'})` : r.trigger)),
      h('td.right', r.duration_ms ? fmtDuration(r.duration_ms / 1000) : ''),
      h('td', outcomeBadge(r.status), r.error ? h('div.muted.small', r.error) : null),
      h('td', cost),
      h('td', r.sent ? threadEl(r.sent_to) : h('span.muted', '—')));
  };
}

/** Everything about agent a, field by field. */
function details(a) {
  const s = a.schedule || {};
  let what = a.task;
  if (s.prompt) what = h('div', h('div', 'Its prompt:'), h('pre.code.agent-prompt', s.prompt));
  return h('section.card.section',
    h('div.card-head', h('h2', 'Details'), a.review || a.from_memory ? null : btn('Edit', { small: true, icon: 'edit', href: link(a.brain, a.name, 'edit') })),
    kv([
      ['Brain', h('a', { href: `#/brains/${enc(a.brain)}` }, a.brain_name || a.brain)],
      ['File', a.file ? mono(a.file, { copy: true }) : h('span.muted', 'memory.consolidate in brain.json')],
      ['Description', s.description || null],
      ['Does', what],
      ['Template vars', s.vars && Object.keys(s.vars).length ? h('pre.code', varsText(s.vars)) : null],
      ['When', mono(s.when)],
      ['Next run', a.next ? `${runTime(a.next)} (${inTime(a.next)})` : (a.state === 'pending' ? 'after approval' : '—')],
      ['Last run', a.last_run ? h('span', outcomeBadge(a.last_run.status), ' ', timeAgo(a.last_run.started), a.last_run.duration_ms ? ` · took ${fmtDuration(a.last_run.duration_ms / 1000)}` : '') : 'never'],
      ['State', h('span', stateBadge(a), s.enabled === false ? ' runs only when you start it' : '')],
      ['May do', h('ul.plain-list', permissionsOf(s, a.chat).map((x) => h('li', x)))],
      ['Timeout', s.timeout_minutes ? `${s.timeout_minutes} min` : 'run_timeout_minutes'],
      ['Missed while the bridge was down', s.missed === 'skip' ? 'skipped' : 'run once when it is back'],
      ['Jitter', s.jitter_minutes !== undefined ? `${s.jitter_minutes} min` : 'schedules.jitter_minutes'],
      ['Problem', a.problem ? h('span.warn-text', a.problem) : null],
    ]),
    a.file ? h('details.details', h('summary', 'The file'), h('pre.code.agent-json', fileJSON(s))) : null);
}

function renderAgent(root, ctx, brain, name, run) {
  const head = h('div');
  const reviewBox = h('div');
  const detailBox = h('div', spinner());
  const runsBody = h('tbody');
  const logBox = h('div');
  let agent = null;
  mount(root,
    h('a.back-link', { href: '#/schedules' }, icon('left'), 'Schedules'),
    head, reviewBox, detailBox,
    h('section.card.section', h('h2', 'Runs'),
      h('div.table-wrap', h('table.table',
        h('thead', h('tr', h('th', 'Started'), h('th.right', 'Took'), h('th', 'Result'), h('th', 'Cost'), h('th', 'Message sent'))),
        runsBody)),
      logBox));
  ctx.setTitle(`Schedules: ${name}`);
  const gone = () => ctx.navigate('#/schedules');
  const runs = resource(`${base(brain, name)}/runs`);
  ctx.follow(runs, (list) => {
    if (!list || !list.length) {
      runsBody.replaceChildren(h('tr', h('td', { colspan: 5 }, h('span.muted', 'No runs yet: Run now starts one.'))));
      return;
    }
    reconcile(runsBody, list, (r) => `${r.id}|${r.status}|${r.sent}`, runRow(brain, name, run));
  }, () => (agent && agent.running ? 2000 : 8000));
  if (run) {
    const logRes = resource(`${base(brain, name)}/runs/${enc(run)}/log`, { missing: null });
    ctx.follow(logRes, (l) => {
      paint(logBox, l ? `${l.log.length}|${l.cut}` : 'none', () => (l
        ? h('div', h('h3', `Log of run ${run}`, l.running ? ' (running)' : ''), l.cut ? h('p.muted.small', 'The start of the log is left out.') : null, h('pre.code.schedule-log', l.log))
        : callout('info', 'No log', h('p', 'This run’s log aged out, or the run never started.'))));
    }, () => (agent && agent.running ? 2000 : 30000));
  }
  ctx.follow(schedulesResource, (d) => {
    if (!d) return;
    agent = (d.agents || []).find((a) => a.brain === brain && a.name === name) || null;
    if (!agent) {
      paint(head, 'missing', () => callout('warn', 'No such agent', h('p', `Brain ${brain} has no scheduled agent ${name}.`), h('div.actions', btn('All agents', { href: '#/schedules' }))));
      mount(detailBox);
      mount(reviewBox);
      return;
    }
    const a = agent;
    paint(head, JSON.stringify([a.state, a.running, a.paused, a.review && a.review.status]), () => h('header.page-header',
      h('div.page-heading', h('h1', a.name, ' ', stateBadge(a), a.running ? badge('running', 'info') : null), h('p.page-sub', 'Brain ', mono(a.brain), a.schedule.description ? ` · ${a.schedule.description}` : '')),
      agentActions(a, { onDeleted: gone, small: false })));
    paint(reviewBox, JSON.stringify(a.review || null), () => (a.review ? h('section.card.section', reviewPanel(a, { after: gone })) : null));
    paint(detailBox, JSON.stringify(a), () => details(a));
  }, () => (agent && agent.running ? 2000 : 5000));
  ctx.focusAt(root.querySelector('h1'));
}

function renderWizard(root, ctx, { mode, brain, name }) {
  const step = { current: mode === 'new' ? 'what' : 'when' };
  const templates = [];
  const brains = [];
  const repos = [];
  const nodes = [];
  const note = h('div');
  const body = h('div', spinner());
  const title = { new: 'Add a scheduled agent', edit: `Edit ${name}`, duplicate: `Duplicate ${name}` }[mode];
  mount(root,
    h('header.page-header', h('div.page-heading',
      h('a.back-link', { href: brain ? link(brain, name) : '#/schedules' }, icon('left'), brain ? name : 'Schedules'),
      h('h1', title),
      h('p.page-sub', mode === 'edit'
        ? 'Change what you need; Review shows the file before it is written. Saving approves it, as you are the one writing it.'
        : 'Step by step: what it does, where, when, and what it may do. Nothing is written until you save on the last step.'))),
    note, body);
  ctx.setTitle(title);

  const save = async () => {
    const v = form.form() && form.form().value();
    if (!v || v.invalid) { toast(v ? v.invalid : 'Not complete yet', 'warn'); return; }
    if (mode !== 'edit' && agents.some((a) => a.brain === v.brain && a.name === v.schedule.name)) {
      toast(`Brain ${v.brain} has an agent ${v.schedule.name} already: give this one another name.`, 'warn');
      return;
    }
    try {
      await put(base(v.brain, v.schedule.name), v.schedule);
      form.saved();
      toast(mode === 'edit' ? 'Saved' : 'Saved and approved: it runs on its schedule', 'ok', { timeout: 3000 });
      await schedulesResource.refresh();
      ctx.navigate(link(v.brain, v.schedule.name));
    } catch (e) { toastError(e); }
  };
  const form = liveForm({
    build: (d, changed) => agentWizard({ templates, brains, repos, nodes, mode, step, onSave: save, preview: (when) => get(`/api/schedules/preview?when=${enc(when)}`) })(d, changed),
    edits: (d) => d.schedule,
    staleNote: { title: 'This agent changed elsewhere', text: 'Its file changed while you edited it here: saving now would overwrite that change.', action: 'Load the agent again' },
  });
  ctx.dirty = () => form.dirty();

  let agents = [];
  let haveBrains = false;
  let haveAgents = false;
  const ready = () => {
    if (!haveBrains || !haveAgents) return;
    if (mode === 'new') {
      if (!form.form()) { mount(body, form.el); form.set({ schedule: null }); }
      return;
    }
    const a = agents.find((x) => x.brain === brain && x.name === name);
    if (!a) {
      mount(body, callout('warn', 'No such agent', h('p', `Brain ${brain} has no scheduled agent ${name}.`), h('div.actions', btn('All agents', { href: '#/schedules' }))));
      return;
    }
    if (!form.form()) mount(body, form.el);
    paint(note, mode === 'edit' && !!a.review, (pending) => (pending ? callout('warn', 'A change to this agent waits for your approval', 'Saving here writes your version over it.') : null));
    form.set({ brain, name, schedule: a.schedule });
  };
  ctx.follow(brainsResource, (list) => {
    brains.splice(0, brains.length, ...(list || []).filter((b) => b.id).map((b) => ({ id: b.id, name: b.name })));
    haveBrains = true;
    ready();
  }, 30000);
  ctx.follow(schedulesResource, (d) => {
    if (!d) return;
    templates.splice(0, templates.length, ...(d.templates || []));
    agents = d.agents || [];
    haveAgents = true;
    ready();
  }, 10000);
  ctx.follow(githubResource, (g) => {
    const list = [];
    for (const i of (g && g.installations) || []) list.push(...(i.repos || []));
    repos.splice(0, repos.length, ...new Set(list));
  }, 60000);
  ctx.follow(nodesResource, (d) => {
    nodes.splice(0, nodes.length, ...((d && d.nodes) || []));
  }, 30000);
  ctx.focusAt(root.querySelector('h1'));
}

export default {
  title: 'Schedules',
  render(root, ctx) {
    const { brain, name, run } = ctx.params;
    if (brain === 'new') renderWizard(root, ctx, { mode: 'new' });
    else if (brain && name && (run === 'edit' || run === 'duplicate')) renderWizard(root, ctx, { mode: run, brain, name });
    else if (brain && name) renderAgent(root, ctx, brain, name, run);
    else renderList(root, ctx);
  },
};
