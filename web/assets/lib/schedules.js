// Pure helpers of the Schedules page: a scheduled agent between its
// brain.json entry and the edit form, and how its times read.

/** The kind of task an agent has, as the form's select names it. */
export function taskKind(s) {
  if (s.command) return 'command';
  if (s.template) return 'template';
  if (s.prompt) return 'prompt';
  return 'file';
}

/** vars ({name: value}) as the form's lines, name=value. */
export function varsText(vars) {
  return Object.entries(vars || {}).map(([k, v]) => `${k}=${v}`).join('\n');
}

/** The form's name=value lines as vars; blank lines are left out. */
function parseVars(text) {
  const out = {};
  for (const line of String(text || '').split('\n')) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf('=');
    if (i <= 0) throw new Error(`"${t}" is not name=value`);
    out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}

/** The non-blank lines of text, trimmed. */
function lines(text) {
  return String(text || '').split('\n').map((l) => l.trim()).filter(Boolean);
}

/**
 * The brain.json entry the form's fields stand for: f holds the fields'
 * values (strings, booleans). Fields the agent's kind does not take are
 * left out, and so are defaults, so that an entry says only what it sets.
 */
export function scheduleOf(f) {
  const s = { name: f.name.trim(), when: f.when.trim() };
  if (!f.enabled) s.enabled = false;
  if (f.description.trim()) s.description = f.description.trim();
  switch (f.kind) {
    case 'template': s.template = f.template; break;
    case 'prompt': s.prompt = f.prompt; break;
    case 'command': s.command = { node: f.node.trim(), argv: lines(f.argv), ...(f.dir.trim() ? { dir: f.dir.trim() } : {}) }; break;
    default: if (f.promptFile.trim()) s.prompt_file = f.promptFile.trim();
  }
  if (f.kind !== 'command') {
    const vars = parseVars(f.vars);
    if (Object.keys(vars).length) s.vars = vars;
    if (f.risk) s.risk = f.risk;
    if (f.ownTools) s.disallowed_tools = lines(f.tools);
    const pre = lines(f.preapproved);
    if (pre.length) s.preapproved = pre;
    if (f.github) s.secrets = ['github'];
  }
  if (f.report) s.report = f.report;
  if (f.chat.trim()) s.chat = f.chat.trim();
  const timeout = Number(f.timeout);
  if (timeout > 0) s.timeout_minutes = timeout;
  if (f.missed) s.missed = f.missed;
  if (f.jitter !== '') s.jitter_minutes = Number(f.jitter);
  return s;
}

/** "in 2 h 5 min", "in 3 min", "now": how far ahead ts is. */
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
