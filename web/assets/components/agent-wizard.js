// The guided way to add, edit or duplicate a scheduled agent: six steps,
// each on its own — what it does (a template, a prompt or a program on a
// node), which brain and name, when (presets, or cron for experts, with
// the next three runs), its details (the template's vars, the prompt or
// the program), what it may do in plain words, and the exact file that
// will be written. Built for liveForm: agentWizard(opts)(data, changed)
// returns {el, value(), brain()}.

import { get } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { nodeNamed, programOnNode } from '../lib/nodes.js';
import { WEEKDAYS, fieldsOf, fileJSON, inTime, permissionsOf, presetOf, riskText, runTime, scheduleOf, whenOf } from '../lib/schedules.js';
import { btn } from './buttons.js';
import { callout } from './common.js';
import { field, nextId, select, textInput, toggle } from './fields.js';
import { icon } from './icons.js';
import { errorMessage } from '../lib/errors.js';

/** @import { Child } from '../lib/dom.js' */

const STEPS = [['what', 'What'], ['brain', 'Brain'], ['when', 'When'], ['details', 'Details'], ['permissions', 'Permissions'], ['review', 'Review']];

// What the built-in templates' vars mean.
const VAR_HELP = {
  repos: 'The GitHub repositories it reviews, owner/name, separated by spaces.',
  max_issues: 'The most issues it files in one run; the rest stay in its notes.',
  label: 'The label its issues get (created once if it is missing).',
  greeting: 'The first words of the message.',
};

// The built-in templates' names as the cards show them.
const TITLES = {
  'morning-briefing': 'Morning briefing',
  'memory-consolidation': 'Memory consolidation',
  'code-review-security': 'Code review: security',
  'code-review-bugs': 'Code review: bugs',
  'code-review-simplify': 'Code review: simplify',
};

const CUSTOM = [
  { key: 'prompt', title: 'Custom prompt', text: 'Your own task, in your words: the brain runs it on the schedule, with its memory, tools and sandbox.' },
  { key: 'command', title: 'Run a command on a node', text: 'A program on one of your machines (a node), no Claude run: a scraper, a backup, a check. Only what the node’s allowlist lets it run.' },
];

const REPORTS = [['worth', 'Only when worth reporting', 'The run says whether it found something; nothing is sent otherwise. Failures are always sent.'],
  ['always', 'Every time', 'Every result goes to the chat.'],
  ['silent', 'Never', 'Nothing is sent: the result is in its history and log here.']];

/** A radio group of cards: options [[value, title, text]]. */
function choiceCards(name, options, value, onPick) {
  return h('div.choice-cards', { role: 'radiogroup' }, options.map(([v, title, text]) => {
    const input = h('input', { type: 'radio', name, value: v, checked: v === value, id: nextId(name) });
    input.addEventListener('change', () => { if (input.checked) onPick(v); });
    return h('label.choice-card', { for: input.id }, input, h('span.choice-title', title), text ? h('span.choice-text', text) : null);
  }));
}

/**
 * agentWizard({templates, brains, repos, nodes, mode, step, onSave, preview})
 * returns liveForm's build: mode is 'new', 'edit' or 'duplicate'; step is
 * {current} kept across rebuilds; repos are the GitHub repositories the
 * app is installed on; onSave() saves the value; preview(when) answers
 * {next, error} (GET /api/schedules/preview).
 */
export function agentWizard({ templates, brains, repos = [], nodes = [], mode = 'new', step = { current: 'what' }, onSave, preview = (when) => get(`/api/schedules/preview?when=${encodeURIComponent(when)}`) }) {
  return (data, changed) => {
    const f = fieldsOf(data.schedule || {});
    let brain = data.brain || (brains[0] && brains[0].id) || '';
    if (mode === 'duplicate') f.name = f.name ? `${f.name}-copy`.slice(0, 40) : '';
    if (mode === 'new' && !data.schedule) f.kind = '';
    const edit = mode === 'edit';
    const tmpl = () => templates.find((t) => t.name === f.template);
    const set = (k, v) => { f[k] = v; changed(); };

    const stepper = h('ol.stepper.wizard-steps');
    const body = h('div.wizard-body');
    const nav = h('div.actions.wizard-actions');
    const el = h('div.agent-wizard', stepper, body, nav);

    const value = () => scheduleOf(f);
    const valid = () => { try { value(); return ''; } catch (e) { return errorMessage(e); } };

    // What each step needs before the next.
    const missing = (key) => {
      switch (key) {
        case 'what': return f.kind ? '' : 'Choose what it does.';
        case 'brain': return !brain ? 'Choose a brain.' : (/^[a-z0-9][a-z0-9-]{0,39}$/.test(f.name) ? '' : 'Give it a name: lower-case letters, digits and -.');
        case 'when': return f.when.trim() ? '' : 'Say when it runs.';
        case 'details': {
          if (f.kind === 'prompt' && !f.prompt.trim()) return 'Write its task.';
          if (f.kind === 'command' && (!f.node.trim() || !f.argv.trim())) return 'Choose the node and the program.';
          const t = f.kind === 'template' ? tmpl() : null;
          const need = t ? (t.vars || []).filter((v) => v.required && !String(f.vars[v.name] || '').trim()) : [];
          return need.length ? `Fill in ${need.map((v) => v.name).join(', ')}.` : '';
        }
        default: return '';
      }
    };

    const go = (key) => { step.current = key; paint(); };

    // How the program stands on its node: allowed, added on saving, or
    // what the node must opt into first.
    const argvOf = () => f.argv.split('\n').map((x) => x.trim()).filter(Boolean);
    const programNow = () => programOnNode(nodeNamed(nodes, f.node.trim()), argvOf());
    const programCallout = () => {
      const p = programNow();
      const tone = p.state === 'allowed' ? 'ok' : p.state === 'waiting' ? 'info' : 'warn';
      const title = { allowed: `Node ${f.node} runs it`, waiting: `Saving adds it to node ${f.node}`, 'no-node': 'No such node' }[p.state] || `Node ${f.node || '…'} would not run it`;
      return callout(tone, title, p.text);
    };

    function paintStepper() {
      const idx = STEPS.findIndex(([k]) => k === step.current);
      mount(stepper, STEPS.map(([key, label], i) => {
        const b = h('button.step-btn', { type: 'button', 'aria-current': i === idx ? 'step' : null }, h('span.step-num', i < idx ? icon('check') : String(i + 1)), h('span.step-label', label));
        b.addEventListener('click', () => go(key));
        return h(`li.${i < idx ? 'done' : i === idx ? 'current' : 'todo'}`, b);
      }));
    }

    // ── 1. what it does ──
    function whatStep() {
      const pick = (kind, template) => {
        f.kind = kind;
        f.template = template || '';
        const t = tmpl();
        // A new agent takes the template's suggestions.
        if (t && !edit) {
          if (t.when) f.when = t.when;
          f.report = t.report || '';
          f.risk = t.risk || '';
          if (!f.name || mode === 'new') f.name = t.name.replace(/^code-review-/, 'review-');
          if (!f.description) f.description = t.description || '';
          f.vars = Object.fromEntries((t.vars || []).filter((v) => v.default).map((v) => [v.name, v.default]));
          if (t.github) f.githubPermissions = Object.entries(t.github).map(([k, v]) => `${k}=${v}`).join('\n');
        }
        changed();
        go('brain');
      };
      const cards = [
        ...templates.map((t) => ({ kind: 'template', template: t.name, title: TITLES[t.name] || t.name, text: t.description, chosen: f.kind === 'template' && f.template === t.name })),
        ...CUSTOM.map((c) => ({ kind: c.key, title: c.title, text: c.text, chosen: f.kind === c.key || (c.key === 'prompt' && f.kind === 'file') })),
      ];
      return [
        h('h2', 'What should it do?'),
        h('p.muted', 'Start from a built-in template, your own prompt, or a program on one of your machines.'),
        h('div.template-cards', cards.map((c) => {
          const b = h(`button.template-card${c.chosen ? '.chosen' : ''}`, { type: 'button', 'aria-pressed': c.chosen ? 'true' : 'false' }, h('strong', c.title), h('span.muted.small', c.text));
          b.addEventListener('click', () => pick(c.kind, c.template));
          return b;
        })),
      ];
    }

    // ── 2. brain, name, description ──
    function brainStep() {
      const sel = select(brains.map((b) => [b.id, b.name ? `${b.name} (${b.id})` : b.id]), brain, { disabled: edit });
      sel.addEventListener('change', () => { brain = sel.value; changed(); });
      const name = textInput({ value: f.name, placeholder: 'morning-briefing', disabled: edit, pattern: '[a-z0-9][a-z0-9-]{0,39}' });
      name.addEventListener('input', () => set('name', name.value.trim()));
      const desc = textInput({ value: f.description, placeholder: 'What it is for, in one line' });
      desc.addEventListener('input', () => set('description', desc.value));
      return [
        h('h2', 'Which brain runs it?'),
        h('p.muted', 'The agent runs as this brain: with its memory, skills, sandbox and chats. Its file goes into the brain’s folder.'),
        h('div.form-grid',
          field({ label: 'Brain', control: sel }),
          field({ label: 'Name', help: edit ? 'A name cannot change: duplicate the agent to give it another.' : 'Lower-case letters, digits and -; it names the file .brain/schedules/<name>.json.', control: name }),
          field({ label: 'Description', control: desc, wide: true })),
      ];
    }

    // ── 3. when ──
    function whenStep() {
      const p = presetOf(f.when || '');
      if (!f.when) Object.assign(p, { mode: 'daily', time: '07:00' });
      const out = h('div.when-preview', { 'aria-live': 'polite' });
      let timer = null;
      const update = (quiet) => {
        f.when = whenOf(p);
        if (!quiet) changed();
        clearTimeout(timer);
        timer = setTimeout(async () => {
          const when = f.when;
          if (!when) { mount(out, h('p.muted', 'Enter a schedule.')); return; }
          try {
            const r = await preview(when);
            if (when !== f.when) return;
            mount(out, r.error
              ? callout('bad', 'Not a schedule the bridge reads', r.error)
              : h('div', h('div.field-label', 'Next runs'), h('ol.next-runs', (r.next || []).map((t) => h('li', h('strong', runTime(t)), h('span.muted', ` · ${inTime(t)}`)))),
                h('p.muted.small', 'In the bridge’s time zone; each run may start up to a few minutes later (jitter), so agents due at once do not all start together.')));
          } catch (e) {
            mount(out, h('p.muted', `Cannot preview: ${errorMessage(e)}`));
          }
        }, 250);
      };
      const hours = textInput({ type: 'number', min: 1, max: 168, value: p.hours || 6 });
      hours.addEventListener('input', () => { p.hours = hours.value; update(); });
      const daily = textInput({ type: 'time', value: p.mode === 'daily' ? p.time : '07:00' });
      daily.addEventListener('input', () => { p.time = daily.value; update(); });
      const day = select(WEEKDAYS.map((d) => [d, d]), p.day || 'mon');
      const weeklyTime = textInput({ type: 'time', value: p.mode === 'weekly' ? p.time : '09:00' });
      const weekly = () => { p.day = day.value; p.time = weeklyTime.value; update(); };
      day.addEventListener('change', weekly);
      weeklyTime.addEventListener('input', weekly);
      const cron = textInput({ value: p.mode === 'cron' ? p.cron : '', placeholder: '0 7 * * 1-5', class: 'mono' });
      cron.addEventListener('input', () => { p.cron = cron.value; update(); });
      const detail = { every: field({ label: 'Every … hours', control: hours }), daily: field({ label: 'At', control: daily }), weekly: h('div.form-grid', field({ label: 'On', control: day }), field({ label: 'At', control: weeklyTime })), cron: field({ label: 'Cron expression', help: 'minute hour day month weekday, as cron reads it: 0 7 * * 1-5 is 07:00 on weekdays. “every 30m” works here too.', control: cron }) };
      const show = () => { for (const [k, d] of Object.entries(detail)) d.hidden = k !== p.mode; };
      const modes = choiceCards('when-mode', [['every', 'Every few hours'], ['daily', 'Daily'], ['weekly', 'Weekly'], ['cron', 'Cron (expert)']], p.mode, (m) => {
        p.mode = m;
        if (m === 'every') p.hours = hours.value;
        if (m === 'daily') p.time = daily.value;
        if (m === 'weekly') { p.day = day.value; p.time = weeklyTime.value; }
        if (m === 'cron') p.cron = cron.value;
        show();
        update();
      });
      show();
      update(true);
      return [h('h2', 'When does it run?'), modes, ...Object.values(detail), out];
    }

    // ── 4. details ──
    function repoPicker(value, onChange) {
      const chosen = new Set(String(value || '').split(/[\s,]+/).filter(Boolean));
      const other = textInput({ value: [...chosen].filter((r) => !repos.includes(r)).join(' '), placeholder: 'owner/name', class: 'mono' });
      const emit = () => {
        const list = [...repos.filter((r) => chosen.has(r)), ...other.value.split(/[\s,]+/).filter(Boolean)];
        onChange([...new Set(list)].join(' '));
      };
      other.addEventListener('input', emit);
      return h('div.repo-picker',
        repos.length ? h('div.repo-list', repos.map((r) => {
          const box = h('input', { type: 'checkbox', checked: chosen.has(r), id: nextId('repo') });
          box.addEventListener('change', () => { if (box.checked) chosen.add(r); else chosen.delete(r); emit(); });
          return h('label.check-row', { for: box.id }, box, h('code.mono', r));
        })) : h('p.muted.small', 'Connect GitHub (Integrations → GitHub) to pick from the repositories its app is installed on.'),
        field({ label: repos.length ? 'Others' : 'Repositories', control: other }));
    }

    function detailsStep() {
      if (f.kind === 'command') {
        const node = nodes.length ? select([['', 'Choose a node'], ...nodes.map((n) => /** @type {[string, string]} */ ([n.name || n.id, n.name ? `${n.name}${n.online === false ? ' (offline)' : ''}` : n.id]))], f.node) : textInput({ value: f.node, placeholder: 'mac-mini' });
        node.addEventListener('change', () => set('node', node.value));
        node.addEventListener('input', () => set('node', node.value));
        const argv = h('textarea.mono', { rows: 3, id: nextId('argv'), placeholder: '/Users/you/bin/scrape.sh\n--quiet' });
        argv.value = f.argv;
        const status = h('div.program-status');
        const repaint = () => { if (f.node.trim() && argvOf().length) mount(status, programCallout()); else mount(status); };
        argv.addEventListener('input', () => { set('argv', argv.value); repaint(); });
        node.addEventListener('change', repaint);
        node.addEventListener('input', repaint);
        repaint();
        const dir = textInput({ value: f.dir, placeholder: 'the node’s default', class: 'mono' });
        dir.addEventListener('input', () => set('dir', dir.value));
        return [h('h2', 'Which program, where?'),
          h('div.form-grid',
            field({ label: 'Node', help: 'One of your machines that joined as a node. It runs the program when its node.json allows it, or when the program is in a folder it accepts programs from (exec.accept_from_core): saving the agent then adds it.', control: node }),
            field({ label: 'Program and arguments', help: 'One per line, no shell: the first line is the program.', control: argv, wide: true }),
            field({ label: 'Working folder', control: dir })),
          status];
      }
      if (f.kind === 'prompt' || f.kind === 'file') {
        const inFile = toggle({ checked: f.kind === 'file', label: 'Keep the task in a file of the brain instead', onChange: (on) => { set('kind', on ? 'file' : 'prompt'); paint(); } });
        const prompt = h('textarea', { rows: 8, id: nextId('prompt'), placeholder: 'Look in inbox/ for new files and tell me what needs me.' });
        prompt.value = f.prompt;
        prompt.addEventListener('input', () => set('prompt', prompt.value));
        const file = textInput({ value: f.promptFile, placeholder: `schedules/${f.name || '<name>'}.md`, class: 'mono' });
        file.addEventListener('input', () => set('promptFile', file.value));
        return [h('h2', 'What is its task?'),
          h('p.muted', 'Written as you would ask the brain in a chat. It runs in a chat of its own, so it sees what it reported before.'),
          inFile,
          f.kind === 'file'
            ? field({ label: 'Task file', help: 'Relative to the brain folder; empty is schedules/<name>.md. The brain may edit it, as it may its prompt.', control: file })
            : field({ label: 'Task', control: prompt, wide: true })];
      }
      const t = tmpl();
      const vars = (t && t.vars) || [];
      return [h('h2', `Settings of ${f.template}`),
        t ? h('p.muted', t.description) : null,
        vars.length ? h('div.form-grid', vars.map((v) => {
          const help = `${VAR_HELP[v.name] || `Fills {{${v.name}}} in the task.`}${v.default ? ` Default: ${v.default}.` : ''}`;
          if (v.name === 'repos') {
            return field({ label: `${v.name}${v.required ? ' (needed)' : ''}`, help, wide: true, control: repoPicker(f.vars.repos, (val) => {
              f.vars.repos = val;
              // The grant follows the reviewed repositories.
              f.githubRepos = val.split(/\s+/).filter(Boolean).join('\n');
              changed();
            }) });
          }
          const input = textInput({ value: f.vars[v.name] ?? '', placeholder: v.default || '' });
          input.addEventListener('input', () => { f.vars[v.name] = input.value; changed(); });
          return field({ label: `${v.name}${v.required ? ' (needed)' : ''}`, help, control: input });
        })) : h('p.muted', 'This template has nothing to set.')];
    }

    // ── 5. permissions and results ──
    function permissionsStep() {
      const s = (() => { try { return value(); } catch { return null; } })();
      const plain = h('ul.plain-list', (s ? permissionsOf(s) : []).map((t) => h('li', t)));
      const refreshPlain = () => { try { mount(plain, permissionsOf(value()).map((t) => h('li', t))); } catch { /* still incomplete */ } };
      const setP = (k, v) => { set(k, v); refreshPlain(); };
      /** @type {Child[]} */
      const parts = [h('h2', 'What may it do?'), h('p.muted', 'In plain words, what this agent is allowed to do. You approve it as you save it.')];
      if (f.kind !== 'command') {
        parts.push(h('h3', 'Risk level'), choiceCards('risk', [['read', 'Read', riskText('read')], ['write', 'Write (default)', riskText('write')], ['irreversible', 'Irreversible', riskText('irreversible')]], f.risk || 'write', (r) => setP('risk', r === 'write' ? '' : r)));
        const pre = h('textarea', { rows: 2, id: nextId('pre'), placeholder: 'file GitHub issues, labelled bug, on you/project' });
        pre.value = f.preapproved;
        pre.addEventListener('input', () => setP('preapproved', pre.value));
        parts.push(field({ label: 'Steps approved with it', help: 'One a line. Its runs take these without asking you; every other irreversible step still asks in your chat.', control: pre, wide: true }));
        const ghOn = toggle({ checked: !!f.githubRepos.trim(), label: 'Its runs get GitHub access' });
        const ghBox = h('div.github-grant');
        const paintGh = () => {
          ghBox.hidden = !ghOn.input.checked;
          const perms = {};
          for (const line of f.githubPermissions.split('\n')) { const [k, v] = line.split('='); if (k && k.trim()) perms[k.trim()] = (v || '').trim(); }
          const names = [...new Set(['contents', 'issues', 'pull_requests', ...Object.keys(perms)])];
          mount(ghBox,
            field({ label: 'Repositories', wide: true, control: repoPicker(f.githubRepos, (val) => setP('githubRepos', val.split(/\s+/).filter(Boolean).join('\n'))) }),
            h('div.form-grid', names.map((n) => {
              const sel = select([['', 'none'], ['read', 'read'], ['write', 'write']], perms[n] || '');
              sel.addEventListener('change', () => {
                perms[n] = sel.value;
                setP('githubPermissions', Object.entries(perms).filter(([, v]) => v).map(([k, v]) => `${k}=${v}`).join('\n'));
              });
              return field({ label: n.replace('_', ' '), control: sel });
            })),
            h('p.muted.small', 'Each run gets a token for exactly these repositories and permissions, made as it starts and ended with it. No other run gets it.'));
        };
        ghOn.input.addEventListener('change', () => { if (!ghOn.input.checked) { f.githubRepos = ''; changed(); refreshPlain(); } paintGh(); });
        paintGh();
        parts.push(h('h3', 'GitHub'), ghOn, ghBox);
      } else {
        parts.push(callout('info', `Runs on node ${f.node || '…'}`, 'A program, not a Claude run: no risk level or tools apply. The node runs only what its allowlist names, with exactly these arguments when the core adds it, and refuses everything in a lockdown.'), programCallout());
      }
      const chat = textInput({ value: f.chat, placeholder: 'the brain’s reply chat', class: 'mono' });
      chat.addEventListener('input', () => setP('chat', chat.value));
      parts.push(h('h3', 'Where results go'), choiceCards('report', REPORTS, f.report || 'worth', (r) => setP('report', r === 'worth' ? '' : r)),
        field({ label: 'Chat', help: 'A chat’s JID (…@s.whatsapp.net or …@g.us); empty is the brain’s reply chat.', control: chat }));
      const enabled = toggle({ checked: f.enabled, label: 'Runs on its schedule (off: only Run now)', onChange: (on) => setP('enabled', on) });
      const timeout = textInput({ type: 'number', min: 0, value: f.timeout, placeholder: 'run_timeout_minutes' });
      timeout.addEventListener('input', () => setP('timeout', timeout.value));
      const missed = select([['', 'Run it once when the bridge is back'], ['skip', 'Skip it, wait for the next time']], f.missed);
      missed.addEventListener('change', () => setP('missed', missed.value));
      const jitter = textInput({ type: 'number', min: 0, value: f.jitter, placeholder: 'schedules.jitter_minutes' });
      jitter.addEventListener('input', () => setP('jitter', jitter.value));
      const ownTools = toggle({ checked: f.ownTools, label: 'Its own list of blocked tools (off: the brain’s)', onChange: (on) => setP('ownTools', on) });
      const tools = h('textarea.mono', { rows: 2, id: nextId('tools'), placeholder: 'WebFetch' });
      tools.value = f.tools;
      tools.addEventListener('input', () => setP('tools', tools.value));
      parts.push(h('details.details', h('summary', 'More settings'),
        h('div.form-grid',
          field({ label: 'State', control: enabled }),
          field({ label: 'Timeout (minutes)', help: 'It can only shorten run_timeout_minutes.', control: timeout }),
          field({ label: 'Missed while the bridge was down', control: missed }),
          field({ label: 'Jitter (minutes)', control: jitter }),
          f.kind !== 'command' ? field({ label: 'Tools', control: h('div', ownTools, tools), wide: true }) : null)));
      parts.push(h('h3', 'In short'), plain);
      return parts;
    }

    // ── 6. review ──
    function reviewStep() {
      let s;
      try { s = value(); } catch (e) {
        return [h('h2', 'Review'), callout('warn', 'Not complete yet', errorMessage(e))];
      }
      const file = `.brain/schedules/${s.name}.json`;
      const b = brains.find((x) => x.id === brain);
      return [
        h('h2', 'Review and save'),
        h('p', 'Brain ', h('strong', b ? b.name || b.id : brain), ' runs ', h('strong', s.name), ' ', h('code.mono', s.when), '.'),
        h('h3', 'It may'), h('ul.plain-list', permissionsOf(s).map((t) => h('li', t))),
        h('h3', 'The file'), h('p.muted', 'This is exactly what is written to ', h('code.mono', file), ' in the brain’s folder. Saving approves it at once, as you are the one writing it.'),
        h('pre.code.agent-json', fileJSON(s)),
      ];
    }

    const RENDER = { what: whatStep, brain: brainStep, when: whenStep, details: detailsStep, permissions: permissionsStep, review: reviewStep };

    function paint() {
      paintStepper();
      mount(body, RENDER[step.current]());
      const idx = STEPS.findIndex(([k]) => k === step.current);
      const hint = h('span.muted.small.wizard-hint', { role: 'status' });
      const back = idx > 0 ? btn('Back', { icon: 'left', onClick: () => go(STEPS[idx - 1][0]) }) : null;
      const next = idx < STEPS.length - 1
        ? btn('Next', { kind: 'primary', icon: 'right', onClick: () => { const m = missing(step.current); if (m) { mount(hint, m); return; } go(STEPS[idx + 1][0]); } })
        : btn(edit ? 'Save changes' : 'Save and approve', { kind: 'primary', icon: 'check', disabled: !!valid(), onClick: () => onSave && onSave() });
      mount(nav, back, hint, next);
    }
    paint();

    return {
      el,
      brain: () => brain,
      value: () => {
        try { return { brain, schedule: value() }; } catch (e) { return { brain, invalid: errorMessage(e), fields: { ...f, vars: { ...f.vars } } }; }
      },
    };
  };
}
