// Pure helpers of the Schedules page: a scheduled agent between its file
// (.brain/schedules/<name>.json) and the wizard's fields, how its times
// and its permissions read, and how the list is filtered and grouped.

/** @import { Schedule, ScheduleAgent, SchedulesResponse } from '../../types/api.js' */

/**
 * The edit form's fields as scheduleOf reads them: texts as typed (lists
 * one a line, vars and permissions name=value lines), and the switches.
 * @typedef {object} ScheduleFields
 * @property {string} name
 * @property {string} when
 * @property {string} description
 * @property {boolean} enabled
 * @property {string} kind template, prompt, command or file
 * @property {string} template
 * @property {string} prompt
 * @property {string} promptFile
 * @property {string} node
 * @property {string} argv
 * @property {string} dir
 * @property {string | Record<string, string>} vars name=value lines, or the wizard's {name: value}
 * @property {string} risk
 * @property {string} preapproved
 * @property {string} githubRepos
 * @property {string} githubPermissions
 * @property {boolean} ownTools
 * @property {string} tools
 * @property {string} report
 * @property {string} chat
 * @property {string} timeout
 * @property {string} missed
 * @property {string} jitter
 */

/**
 * The wizard's "When" step: a mode and what it takes. hours may be the
 * number typed, as text.
 * @typedef {{mode: string, hours?: number | string, time?: string, day?: string, cron?: string}} WhenPreset
 */

/**
 * The kind of task an agent has, as the form's select names it.
 * @param {Partial<Schedule>} s
 */
export function taskKind(s) {
  if (s.command) return 'command';
  if (s.template) return 'template';
  if (s.prompt) return 'prompt';
  return 'file';
}

/**
 * vars ({name: value}) as the form's lines, name=value.
 * @param {Record<string, string> | null | undefined} vars
 */
export function varsText(vars) {
  return Object.entries(vars || {}).map(([k, v]) => `${k}=${v}`).join('\n');
}

/**
 * The form's name=value lines as vars; blank lines are left out. An
 * object is taken as it is, less its empty values.
 * @param {string | Record<string, string>} text
 */
function parseVars(text) {
  /** @type {Record<string, string>} */
  const out = {};
  if (text && typeof text === 'object') {
    for (const [k, v] of Object.entries(text)) if (String(v ?? '').trim()) out[k] = String(v).trim();
    return out;
  }
  for (const line of String(text || '').split('\n')) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf('=');
    if (i <= 0) throw new Error(`"${t}" is not name=value`);
    out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}

/**
 * The non-blank lines of text, trimmed.
 * @param {string} text
 */
function lines(text) {
  return String(text || '').split('\n').map((l) => l.trim()).filter(Boolean);
}

/**
 * The fields of an agent as the wizard keeps them: vars as {name: value}.
 * @typedef {Omit<ScheduleFields, 'vars'> & {vars: Record<string, string>}} WizardFields
 */

/**
 * The wizard's fields of schedule s, the inverse of scheduleOf.
 * @param {Partial<Schedule>} [s]
 * @returns {WizardFields}
 */
export function fieldsOf(s = {}) {
  return {
    name: s.name || '', when: s.when || '', description: s.description || '', enabled: s.enabled !== false,
    kind: taskKind(s), template: s.template || '', prompt: s.prompt || '', promptFile: s.prompt_file || '',
    node: s.command ? s.command.node || '' : '', argv: s.command ? (s.command.argv || []).join('\n') : '', dir: s.command ? s.command.dir || '' : '',
    vars: { ...(s.vars || {}) }, risk: s.risk || '', preapproved: (s.preapproved || []).join('\n'),
    githubRepos: s.github ? (s.github.repos || []).join('\n') : '', githubPermissions: s.github ? varsText(s.github.permissions) : '',
    ownTools: Array.isArray(s.disallowed_tools), tools: (s.disallowed_tools || []).join('\n'),
    report: s.report || '', chat: s.chat || '', timeout: s.timeout_minutes ? String(s.timeout_minutes) : '',
    missed: s.missed || '', jitter: s.jitter_minutes === undefined || s.jitter_minutes === null ? '' : String(s.jitter_minutes),
  };
}

// The order of the fields in the file the bridge writes (the Go type's).
/** @type {(keyof Schedule)[]} */
const FILE_ORDER = ['name', 'when', 'enabled', 'description', 'prompt', 'prompt_file', 'template', 'command', 'vars', 'report', 'chat', 'risk', 'disallowed_tools', 'timeout_minutes', 'missed', 'jitter_minutes', 'preapproved', 'github'];
/** @param {Record<string, string>} o */
const sortedObj = (o) => Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]]));

/**
 * The file the bridge writes for schedule s, byte for byte.
 * @param {Partial<Schedule>} s
 */
export function fileJSON(s) {
  /** @type {Record<string, unknown>} */
  const out = {};
  for (const k of FILE_ORDER) {
    const v = s[k];
    if (v === undefined) continue;
    if (k === 'command' && s.command) out[k] = { node: s.command.node, argv: s.command.argv, ...(s.command.dir ? { dir: s.command.dir } : {}) };
    else if (k === 'vars' && s.vars) out[k] = sortedObj(s.vars);
    else if (k === 'github' && s.github) out[k] = { repos: s.github.repos, permissions: sortedObj(s.github.permissions || {}) };
    else out[k] = v;
  }
  return `${JSON.stringify(out, null, 2)}\n`;
}

/**
 * The agent the wizard's fields stand for: f holds the fields' values
 * (strings, booleans; vars as name=value lines or an object). It throws
 * an Error that says what is missing. Fields the agent's kind does not take are
 * left out, and so are defaults, so that an entry says only what it sets.
 * @param {ScheduleFields} f
 * @returns {Schedule}
 */
export function scheduleOf(f) {
  /** @type {Schedule} */
  const s = { name: f.name.trim(), when: f.when.trim() };
  if (!f.enabled) s.enabled = false;
  if (f.description.trim()) s.description = f.description.trim();
  if (!/^[a-z0-9][a-z0-9-]{0,39}$/.test(s.name)) throw new Error('Give it a name: 1–40 lower-case letters, digits or -.');
  if (!s.when) throw new Error('Say when it runs.');
  switch (f.kind) {
    case 'template': s.template = f.template; break;
    case 'prompt': s.prompt = f.prompt; break;
    case 'command':
      if (!f.node.trim() || !lines(f.argv).length) throw new Error('A command needs its node and its program.');
      s.command = { node: f.node.trim(), argv: lines(f.argv), ...(f.dir.trim() ? { dir: f.dir.trim() } : {}) };
      break;
    default: if (f.promptFile.trim()) s.prompt_file = f.promptFile.trim();
  }
  if (f.kind !== 'command') {
    const vars = parseVars(f.vars);
    if (Object.keys(vars).length) s.vars = vars;
    if (f.risk) s.risk = f.risk;
    if (f.ownTools) s.disallowed_tools = lines(f.tools);
    const pre = lines(f.preapproved);
    if (pre.length) s.preapproved = pre;
    const repos = String(f.githubRepos || '').split(/[\s,]+/).filter(Boolean);
    if (repos.length) s.github = { repos: [...new Set(repos)], permissions: parseVars(f.githubPermissions) };
  }
  if (f.report) s.report = f.report;
  if (f.chat.trim()) s.chat = f.chat.trim();
  const timeout = Number(f.timeout);
  if (timeout > 0) s.timeout_minutes = timeout;
  if (f.missed) s.missed = f.missed;
  if (f.jitter !== '') s.jitter_minutes = Number(f.jitter);
  return s;
}

/**
 * "in 2 h 5 min", "in 3 min", "now": how far ahead ts is.
 * @param {string | number | Date} ts
 * @param {number} [now]
 */
export function inTime(ts, now = Date.now()) {
  const ms = new Date(ts).getTime() - now;
  if (!Number.isFinite(ms)) return '';
  if (ms < 60000) return 'now';
  const min = Math.round(ms / 60000);
  if (min < 60) return `in ${min} min`;
  const hours = Math.floor(min / 60);
  if (hours < 48) return `in ${hours} h${min % 60 ? ` ${min % 60} min` : ''}`;
  return `in ${Math.round(hours / 24)} days`;
}

const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

/**
 * The preset a schedule's when string stands for, as the wizard's "When"
 * step shows it: {mode: 'every', hours} for "every Nh", {mode: 'daily',
 * time} for "daily HH:MM", {mode: 'weekly', day, time} for "weekly DAY
 * HH:MM", and {mode: 'cron', cron} for anything else, a cron expression
 * or an every with minutes or days.
 * @param {string} when
 * @returns {WhenPreset}
 */
export function presetOf(when) {
  const w = String(when || '').trim().toLowerCase();
  let m = /^every (\d+)h$/.exec(w);
  if (m) return { mode: 'every', hours: Number(m[1]) };
  m = /^daily (\d{1,2}:\d{2})$/.exec(w);
  if (m) return { mode: 'daily', time: m[1].padStart(5, '0') };
  m = /^weekly ([a-z]{3}) (\d{1,2}:\d{2})$/.exec(w);
  if (m && DAYS.includes(m[1])) return { mode: 'weekly', day: m[1], time: m[2].padStart(5, '0') };
  return { mode: 'cron', cron: w.replace(/^cron /, '') };
}

/**
 * The when string of a preset, the inverse of presetOf.
 * @param {WhenPreset} p
 */
export function whenOf(p) {
  switch (p.mode) {
    case 'every': return `every ${Math.max(1, Number(p.hours) || 1)}h`;
    case 'daily': return `daily ${p.time || '07:00'}`;
    case 'weekly': return `weekly ${p.day || 'mon'} ${p.time || '09:00'}`;
    default: {
      const c = String(p.cron || '').trim();
      // A cron expression, or what the bridge reads besides: every 30m.
      return /^every /.test(c) || /^(daily|weekly) /.test(c) ? c : (c ? `cron ${c}` : '');
    }
  }
}

/** The weekdays as the weekly preset takes them. */
export const WEEKDAYS = DAYS;

/** The states an agent is listed in, with their labels and badge kinds. */
/** @type {[ScheduleAgent['state'], string, string][]} */
export const STATES = [
  ['pending', 'Pending approval', 'warn'],
  ['active', 'Active', 'ok'],
  ['paused', 'Paused', 'neutral'],
  ['failing', 'Failing', 'bad'],
  ['disabled', 'Disabled', 'neutral'],
];

/**
 * An agent's state label and badge kind.
 * @param {{state: string}} a
 */
export function stateOf(a) {
  const s = STATES.find(([k]) => k === a.state) || STATES[1];
  return { key: s[0], label: s[1], kind: s[2] };
}

/**
 * What filterAgents and groupByBrain read of an agent.
 * @typedef {{brain: string, name: string, state: string, brain_name?: string, task?: string, review?: object, schedule?: {description?: string}}} AgentLike
 */

/**
 * The agents filter keeps: of brain (all for ''), in state, matching q.
 * @template {AgentLike} A
 * @param {A[]} agents
 * @param {{brain?: string, state?: string, q?: string}} [filter]
 * @returns {A[]}
 */
export function filterAgents(agents, { brain = '', state = '', q = '' } = {}) {
  const needle = q.trim().toLowerCase();
  return agents.filter((a) => (!brain || a.brain === brain)
    && (!state || a.state === state || (state === 'pending' && a.review))
    && (!needle || [a.name, a.brain, a.brain_name, a.task, a.schedule && a.schedule.description].some((v) => String(v || '').toLowerCase().includes(needle))));
}

/**
 * agents grouped by brain, in order: [{brain, name, agents}].
 * @template {AgentLike} A
 * @param {A[]} agents
 */
export function groupByBrain(agents) {
  /** @type {{brain: string, name: string, agents: A[]}[]} */
  const out = [];
  for (const a of agents) {
    let g = out.find((x) => x.brain === a.brain);
    if (!g) {
      g = { brain: a.brain, name: a.brain_name || a.brain, agents: [] };
      out.push(g);
    }
    g.agents.push(a);
  }
  return out;
}

/** @type {Record<string, string>} */
const RISK_TEXT = {
  read: 'Only looks: it reads files, the web and its tools, and changes nothing.',
  write: 'May change files in the brain’s folder, as a normal run does. Irreversible steps still ask you first.',
  irreversible: 'May take steps that cannot be undone (send, publish, file issues). Each still asks you, unless it is approved with the agent below.',
};

/**
 * The risk budget s runs with, in plain words.
 * @param {string | undefined} risk
 */
export function riskText(risk) {
  return RISK_TEXT[risk || 'write'];
}

/**
 * What agent s may do, as plain sentences: its risk budget, the steps
 * approved with it, its GitHub access, the node it runs on and where its
 * results go (chat: the chat they go to, '' for the brain's own).
 * @param {Partial<Schedule>} s
 * @param {string} [chat]
 */
export function permissionsOf(s, chat = '') {
  const out = [];
  if (s.command) {
    out.push(`Runs ${(s.command.argv || []).join(' ')} on node ${s.command.node}${s.command.dir ? ` in ${s.command.dir}` : ''}, no shell; only what that node’s allowlist and your approval of its commands let it.`);
  } else {
    out.push(`Risk ${s.risk || 'write'}: ${riskText(s.risk)}`);
    for (const p of s.preapproved || []) out.push(`Approved with it, taken without asking: ${p}.`);
    if (s.github) {
      const perms = Object.entries(s.github.permissions || {}).map(([k, v]) => `${k} ${v}`).join(', ') || 'contents read';
      out.push(`GitHub: ${perms} on ${(s.github.repos || []).join(', ')}, with a token made for each run and ended with it.`);
    }
    if (Array.isArray(s.disallowed_tools)) {
      out.push(s.disallowed_tools.length ? `Tools blocked: ${s.disallowed_tools.join(', ')}.` : 'May use every tool, those the brain blocks included.');
    }
  }
  const report = s.report || 'worth';
  if (report === 'silent') out.push('Results: none sent; they are in its history and log.');
  else out.push(`Results: ${report === 'always' ? 'every result' : 'only what is worth reporting'}, and failures, go to ${chat || 'the brain’s reply chat'}.`);
  return out;
}

/**
 * A run's time as the Schedules page shows it: "Fri, 24 Oct, 06:00".
 * @param {string | number | Date} ts
 */
export function runTime(ts) {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
}

/**
 * What the Schedules page says about the zone the bridge reads times of
 * day in (GET /api/schedules' timezone): its text, and whether to warn,
 * which it does when the zone is UTC only because timezone is unset.
 * @param {SchedulesResponse['timezone'] | null | undefined} tz
 * @returns {{text: string, warn: boolean} | null}
 */
export function zoneNote(tz) {
  if (!tz || !tz.name) return null;
  if (tz.source === 'config') return { text: `Times of day are ${tz.name}.`, warn: false };
  const utc = ['UTC', 'Etc/UTC', 'UTC0', 'GMT', 'Etc/GMT'].includes(tz.name);
  if (utc) return { text: 'Times of day are UTC: the time zone is unset in the settings, and the bridge runs in UTC. Set it under Settings → Advanced → Time zone.', warn: true };
  const name = tz.name === 'Local' ? 'the bridge’s own time zone' : tz.name;
  return { text: `Times of day are ${name} (the bridge’s; the time zone is unset in the settings).`, warn: false };
}
