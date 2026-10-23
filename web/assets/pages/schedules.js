// Schedules: every brain's scheduled agents with their next and last run,
// Run now, Pause and Resume; #/schedules/<brain>/<agent> edits one and
// shows its runs, #/schedules/<brain>/<agent>/<run> a run's log, and
// #/schedules/new adds one. The reminders (standing intents with a time)
// are listed below the agents.

import { del, post, put } from '../app/api.js';
import { resource, brainsResource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { fmtDuration } from '../lib/format.js';
import { inTime, scheduleOf, taskKind, varsText } from '../lib/schedules.js';
import { btn } from '../components/buttons.js';
import { callout, empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { confirmDialog } from '../components/dialog.js';
import { field, nextId, select, textInput, toggle } from '../components/fields.js';
import { liveForm } from '../components/live-form.js';
import { outcomeBadge, threadEl } from '../components/policy.js';
import { saveBar } from '../components/save-bar.js';
import { toast, toastError } from '../components/toast.js';

const schedulesResource = resource('/api/schedules');
const enc = encodeURIComponent;
const base = (brain, name) => `/api/schedules/${enc(brain)}/${enc(name)}`;
const link = (brain, name, run) => `#/schedules/${enc(brain)}/${enc(name)}${run ? `/${enc(run)}` : ''}`;

const REPORTS = [['worth', 'Only when worth reporting'], ['always', 'Always'], ['silent', 'Silent: history and log only']];
const KINDS = [['template', 'A built-in template'], ['prompt', 'A prompt'], ['file', 'A file in the brain (schedules/<name>.md)'], ['command', 'A program on a node']];
const RISKS = [['', 'Default (write)'], ['read', 'read'], ['write', 'write'], ['irreversible', 'irreversible']];
const MISSED = [['', 'Run once on start (default)'], ['skip', 'Skip it']];

function stateBadge(a) {
  if (a.running) return outcomeBadge('running');
  if (a.problem) return outcomeBadge('cannot run');
  if (a.paused) return outcomeBadge('stopped');
  if (!a.enabled) return outcomeBadge('stopped');
  return outcomeBadge('ok');
}

function stateText(a) {
  if (a.running) return 'running';
  if (a.paused) return 'paused';
  if (!a.enabled) return 'off';
  return 'on';
}

async function act(path, done, body) {
  try {
    await post(path, body);
    toast(done, 'ok', { timeout: 2000 });
    await schedulesResource.refresh();
  } catch (e) {
    toastError(e);
  }
}

function agentRow(a) {
  const last = a.last_run;
  return h('tr',
    h('td', h('a', { href: link(a.brain, a.name) }, h('strong', a.name)), h('div.muted.small', h('code.mono', a.brain), a.from_memory ? ' · memory.consolidate' : '')),
    h('td', h('code.mono', a.schedule.when), h('div.muted.small', a.task)),
    h('td', a.report === 'silent' ? h('span.muted', 'silent') : [h('span', a.report === 'always' ? 'always to ' : 'when worth it, to '), a.chat ? threadEl(a.chat) : h('span.muted', 'no chat')]),
    h('td', stateBadge(a), h('div.muted.small', stateText(a)), a.problem ? h('div.small.warn-text', a.problem) : null),
    h('td', a.next ? h('span', { title: new Date(a.next).toLocaleString() }, inTime(a.next)) : h('span.muted', '—')),
    h('td', last ? [outcomeBadge(last.status), h('div.muted.small', timeAgo(last.started))] : h('span.muted', 'never')),
    h('td.right', h('div.actions',
      btn('Run now', { small: true, icon: 'play', disabled: a.running, onClick: () => act(`${base(a.brain, a.name)}/run`, `Started ${a.name}`) }),
      a.paused
        ? btn('Resume', { small: true, icon: 'refresh', onClick: () => act(`${base(a.brain, a.name)}/resume`, `Resumed ${a.name}`) })
        : btn('Pause', { small: true, icon: 'pause', onClick: () => act(`${base(a.brain, a.name)}/pause`, `Paused ${a.name}`) }))));
}

function reminderRow(r) {
  return h('tr',
    h('td', r.text),
    h('td', h('code.mono', r.brain)),
    h('td', h('code.mono', r.when)),
    h('td', r.next ? inTime(r.next) : h('span.muted', 'done')),
    h('td', threadEl(r.chat)),
    h('td.right', btn('Remove', { small: true, kind: 'danger', icon: 'trash', onClick: async () => {
      const ok = await confirmDialog({ title: 'Remove this reminder?', message: r.text, confirm: 'Remove', danger: true });
      if (!ok) return;
      try {
        await del(`/api/brains/${enc(r.brain)}/intents/${enc(r.id)}`);
        await schedulesResource.refresh();
      } catch (e) { toastError(e); }
    } })));
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

/** The edit form of an agent (or a new one, with brains to pick from). */
function buildForm(templates, brains) {
  return (data, changed) => {
    const s = data.schedule || {};
    const isNew = !data.name;
    const ids = {};
    const input = (key, value, attrs = {}) => {
      ids[key] = textInput({ value: value ?? '', ...attrs });
      return ids[key];
    };
    const area = (key, value, rows = 3, attrs = {}) => {
      ids[key] = h('textarea', { id: nextId(key), rows, ...attrs });
      ids[key].value = value ?? '';
      return ids[key];
    };
    const sel = (key, options, value) => {
      ids[key] = select(options, value ?? '');
      return ids[key];
    };
    const enabled = toggle({ checked: s.enabled !== false, label: 'Runs on its schedule' });
    const github = toggle({ checked: (s.secrets || []).includes('github'), label: 'Hand these runs GH_TOKEN (the github secret)' });
    const ownTools = toggle({ checked: Array.isArray(s.disallowed_tools), label: 'Its own list of blocked tools' });
    const brainSel = isNew ? sel('brain', brains.map((b) => [b.id, b.name || b.id]), data.brain || (brains[0] && brains[0].id)) : null;
    const kind = sel('kind', KINDS, taskKind(s));
    const tmpl = sel('template', templates.map((t) => [t.name, t.name]), s.template || (templates[0] && templates[0].name));
    const tmplHelp = h('div.help');
    const showTemplate = () => {
      const t = templates.find((x) => x.name === ids.template.value);
      tmplHelp.replaceChildren(t ? `${t.description}${(t.vars || []).length ? ` Vars: ${t.vars.map((v) => (v.required ? `${v.name} (needed)` : `${v.name}=${v.default}`)).join(', ')}.` : ''}` : '');
    };
    tmpl.addEventListener('change', () => {
      showTemplate();
      // A new agent takes the template's suggestions.
      const t = templates.find((x) => x.name === tmpl.value);
      if (isNew && t) {
        if (!ids.when.value && t.when) ids.when.value = t.when;
        if (t.report) ids.report.value = t.report;
        if (t.risk) ids.risk.value = t.risk;
        if ((t.secrets || []).includes('github')) github.input.checked = true;
      }
    });
    showTemplate();
    const groups = {
      template: h('div.form-grid', field({ label: 'Template', control: tmpl }), tmplHelp),
      prompt: field({ label: 'Prompt', control: area('prompt', s.prompt, 6), wide: true }),
      file: field({ label: 'Task file', help: 'Relative to the brain folder; empty is schedules/<name>.md.', control: input('promptFile', s.prompt_file, { placeholder: 'schedules/<name>.md' }) }),
      command: h('div.form-grid',
        field({ label: 'Node', help: 'Its id or name; its exec allowlist and the approval of its commands apply.', control: input('node', s.command && s.command.node) }),
        field({ label: 'Program and arguments', help: 'One per line, no shell.', control: area('argv', s.command ? (s.command.argv || []).join('\n') : '', 3) }),
        field({ label: 'Working folder', control: input('dir', s.command && s.command.dir) })),
    };
    const brainOnly = h('div.form-grid',
      field({ label: 'Vars', help: 'name=value, one a line: they fill {{name}} in the task.', control: area('vars', varsText(s.vars), 3) }),
      field({ label: 'Risk budget', control: sel('risk', RISKS, s.risk) }),
      field({ label: 'Steps approved with the schedule', help: 'One a line; its runs take them without asking.', control: area('preapproved', (s.preapproved || []).join('\n'), 2) }),
      field({ label: 'Secrets', control: github }),
      field({ label: 'Tools', help: 'Off, the brain’s list applies.', control: h('div', ownTools, area('tools', (s.disallowed_tools || []).join('\n'), 2, { placeholder: 'WebFetch' })) }));
    const showKind = () => {
      for (const [k, el] of Object.entries(groups)) el.hidden = k !== kind.value;
      brainOnly.hidden = kind.value === 'command';
      changed();
    };
    kind.addEventListener('change', showKind);
    const el = h('div.schedule-form',
      h('div.form-grid',
        isNew ? field({ label: 'Brain', control: brainSel }) : null,
        isNew ? field({ label: 'Name', help: 'Lower-case letters, digits and -.', control: input('name', '', { placeholder: 'morning-briefing' }) }) : null,
        field({ label: 'When', help: '"every 6h", "daily 07:00", "weekly mon 09:00" or a cron expression ("cron 0 7 * * 1-5"), in the bridge’s time zone.', control: input('when', s.when, { placeholder: 'daily 07:00' }) }),
        field({ label: 'Description', control: input('description', s.description) }),
        field({ label: 'State', control: enabled })),
      field({ label: 'What it does', control: kind }),
      ...Object.values(groups),
      brainOnly,
      h('div.form-grid',
        field({ label: 'Results', control: sel('report', REPORTS, s.report || 'worth') }),
        field({ label: 'Chat', help: 'A JID; empty is the brain’s reply chat.', control: input('chat', s.chat) }),
        field({ label: 'Timeout (minutes)', help: '0 is run_timeout_minutes, which it can only shorten.', control: input('timeout', s.timeout_minutes || '', { type: 'number', min: 0 }) }),
        field({ label: 'Missed while the bridge was down', control: sel('missed', MISSED, s.missed) }),
        field({ label: 'Jitter (minutes)', help: 'Empty is schedules.jitter_minutes.', control: input('jitter', s.jitter_minutes ?? '', { type: 'number', min: 0 }) })));
    showKind();
    const fields = () => ({
      name: isNew ? ids.name.value : data.name,
      when: ids.when.value, description: ids.description.value, enabled: enabled.input.checked,
      kind: kind.value, template: tmpl.value, prompt: ids.prompt.value, promptFile: ids.promptFile.value,
      node: ids.node.value, argv: ids.argv.value, dir: ids.dir.value,
      vars: ids.vars.value, risk: ids.risk.value, preapproved: ids.preapproved.value, github: github.input.checked,
      ownTools: ownTools.input.checked, tools: ids.tools.value,
      report: ids.report.value, chat: ids.chat.value, timeout: ids.timeout.value, missed: ids.missed.value, jitter: ids.jitter.value,
    });
    return {
      el,
      brain: () => (isNew ? brainSel.value : data.brain),
      value: () => {
        try { return scheduleOf(fields()); } catch (e) { return { invalid: e.message }; }
      },
    };
  };
}

export default {
  title: 'Schedules',
  render(root, ctx) {
    const { brain, name, run } = ctx.params;
    const adding = brain === 'new';
    const selected = brain && name ? { brain, name } : null;
    const offNote = h('div');
    const agentsBox = h('div', spinner());
    const agentsBody = h('tbody');
    const agentsTable = h('div.card.flush.table-wrap', h('table.table',
      h('thead', h('tr', h('th', 'Agent'), h('th', 'When'), h('th', 'Results'), h('th', 'State'), h('th', 'Next'), h('th', 'Last run'), h('th.right', ''))),
      agentsBody));
    const remindersBox = h('div');
    const detail = h('div');

    mount(root,
      pageHeader('Schedules', 'Each brain’s scheduled agents: tasks a brain runs by itself on a schedule, or programs on a node. They are kept in the brain’s brain.json; one a brain adds itself waits for your approval on its page.',
        btn('New agent', { kind: 'primary', icon: 'plus', href: '#/schedules/new' })),
      offNote, detail,
      h('section.section', h('h2.section-title', 'Agents'), agentsBox),
      h('section.section', h('h2.section-title', 'Reminders'), remindersBox));

    let templates = [];
    let brains = [];
    const form = liveForm({
      build: (d, changed) => buildForm(templates, brains)(d, changed),
      edits: (d) => d.schedule,
      onChange: ({ dirty }) => { if (dirty) bar.show(); else bar.hide(); },
      staleNote: { title: 'This agent changed elsewhere', text: 'Its brain.json changed while you edited it here: saving now would overwrite that change.', action: 'Load the agent again' },
    });
    ctx.dirty = () => form.dirty();
    const save = async () => {
      const built = form.form();
      const s = built.value();
      if (s.invalid) { toast(s.invalid); return; }
      const target = built.brain();
      try {
        await put(base(target, s.name), s);
        form.saved();
        bar.hide();
        toast('Saved to brain.json', 'ok', { timeout: 2000 });
        await schedulesResource.refresh();
        if (adding) ctx.navigate(link(target, s.name));
      } catch (e) { toastError(e); }
    };
    const bar = saveBar({ save: btn('Save', { kind: 'primary', icon: 'check', onClick: save }), onDiscard: () => form.discard() });

    if (adding) {
      mount(detail, h('section.card.section',
        h('h2', 'New agent'),
        h('p.muted', 'Saved into the brain’s brain.json, and approved with it, as every edit here is.'),
        form.el, h('div.actions', btn('Add agent', { kind: 'primary', icon: 'plus', onClick: save }))));
      ctx.follow(brainsResource, (list) => {
        brains = (list || []).filter((b) => b.id);
        if (!form.form() && templates.length) form.set({ schedule: {} });
      }, 10000);
    }

    let agent = null;
    const runsBody = h('tbody');
    const logBox = h('div');
    // While the list has not got the agent (it changed, or the list is
    // older than the agent), the card waits behind a note.
    const gone = h('div');
    if (selected) {
      const head = h('div');
      mount(detail, gone, h('section.card.section', head, form.el, bar,
        h('h3', 'Runs'),
        h('div.table-wrap', h('table.table',
          h('thead', h('tr', h('th', 'Started'), h('th.right', 'Took'), h('th', 'Result'), h('th', 'Cost'), h('th', 'Message sent'))),
          runsBody)),
        logBox));
      ctx.setTitle(`Schedules: ${name}`);
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
      paint(head, '', () => h('div.toolbar',
        h('div', h('h2', name), h('p.muted', 'Brain ', h('code.mono', brain))),
        h('div.actions',
          btn('Run now', { icon: 'play', onClick: () => act(`${base(brain, name)}/run`, `Started ${name}`).then(() => runs.refresh()) }),
          btn('Remove…', { kind: 'danger', icon: 'trash', onClick: async () => {
            const ok = await confirmDialog({ title: `Remove ${name}?`, message: 'It goes out of the brain’s brain.json; its history stays until it ages out.', confirm: 'Remove', danger: true });
            if (!ok) return;
            try {
              await del(base(brain, name));
              form.saved();
              await schedulesResource.refresh();
              ctx.navigate('#/schedules');
            } catch (e) { toastError(e); }
          } }))));
    }

    ctx.follow(schedulesResource, (d) => {
      if (!d) return;
      templates = d.templates || [];
      paint(offNote, d.enabled, (on) => (on ? null : callout('warn', 'Scheduled agents are off', h('p', 'schedules.enabled is off in the settings: no agent runs on its schedule. Run now still works.'))));
      const agents = d.agents || [];
      if (!agents.length) {
        paint(agentsBox, 'none', () => h('div.card', empty('No scheduled agents', 'A brain gets them in its brain.json: New agent adds one, from a built-in template (the morning briefing, memory consolidation, code reviews) or your own prompt.')));
      } else {
        paint(agentsBox, 'list', () => agentsTable);
        reconcile(agentsBody, agents, (a) => `${a.brain}/${a.name}|${JSON.stringify(a)}`, agentRow);
      }
      const reminders = d.reminders || [];
      paint(remindersBox, reminders.length ? reminders.map((r) => `${r.id}:${r.next}`).join(',') : 'none', () => (reminders.length
        ? h('div.card.flush.table-wrap', h('table.table',
          h('thead', h('tr', h('th', 'Reminder'), h('th', 'Brain'), h('th', 'When'), h('th', 'Next'), h('th', 'Chat'), h('th.right', ''))),
          h('tbody', reminders.map(reminderRow))))
        : h('div.card', empty('No reminders', 'A brain sets one when you ask it to remind you of something at a time; it shows here until it fired.'))));
      if (adding && !form.form() && brains.length) form.set({ schedule: {} });
      if (selected) {
        agent = agents.find((a) => a.brain === brain && a.name === name) || null;
        paint(gone, !agent, (missing) => (missing ? callout('warn', 'No such agent', h('p', `Brain ${brain} has no scheduled agent ${name}.`), h('div.actions', btn('All agents', { href: '#/schedules' }))) : null));
        if (agent) form.set({ brain, name, schedule: agent.schedule });
      }
    }, 5000);

    if (selected || adding) ctx.focusAt(detail.querySelector('h2'));
  },
};
