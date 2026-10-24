// What the Nodes page says about a node's capabilities, as the bridge's
// Capabilities.Summary says it in the CLI and !fleet.

/** "exec (uname, df), fs (2 roots, 1 writable), host, notify, fride, backup (1 folder)", or "nothing". */
export function capSummary(c) {
  const parts = [];
  if (c && c.exec && (c.exec.allow || []).length) {
    parts.push(`exec (${c.exec.allow.map((r) => r.name).join(', ')})`);
  }
  if (c && c.exec && (c.exec.accept_from_core || []).length) {
    parts.push(`exec from the core (${c.exec.accept_from_core.join(', ')})`);
  }
  const roots = (c && c.fs && c.fs.roots) || [];
  if (roots.length) {
    const w = roots.filter((r) => r.write).length;
    parts.push(`fs (${roots.length} root${roots.length === 1 ? '' : 's'}${w ? `, ${w} writable` : ''})`);
  }
  if (c && c.host) parts.push('host');
  if (c && c.notify) parts.push('notify');
  if (c && c.fride) parts.push('fride');
  if (c && c.backup) {
    const n = (c.backup.folders || []).length;
    parts.push(n ? `backup (${n} folder${n === 1 ? '' : 's'})` : 'backup');
  }
  if (c && c.update) parts.push('update');
  for (const k of ['agent', 'terminal']) {
    const n = (c && c[k] && c[k].roots) ? c[k].roots.length : 0;
    if (n) parts.push(`${k} (${n} folder${n === 1 ? '' : 's'})`);
  }
  if (c && c.core_host) parts.push('core-host');
  return parts.length ? parts.join(', ') : 'nothing';
}

/** How an exec rule reads: "git status|log", "hostname (no arguments)", "go (any arguments)". */
export function ruleText(r) {
  if (r.exact) return `${[r.name, ...(r.args || [])].join(' ')} (exactly)`;
  if (r.args === null || r.args === undefined) return `${r.name} (any arguments)`;
  if (!r.args.length) return `${r.name} (no arguments)`;
  return `${r.name} ${r.args.join(' | ')}`;
}

/** How a node's last update from the core went, in a few words; null for none. */
export function updateText(u) {
  if (!u || !u.state) return null;
  switch (u.state) {
    case 'sending': return `sending ${u.to}…`;
    case 'restarting': return `restarting into ${u.to}…`;
    case 'ok': return `updated from ${u.from} to ${u.to}`;
    case 'failed': return `the update to ${u.to} failed: ${u.error || 'no reason given'}`;
    default: return u.state;
  }
}

/** The OS as people name it. */
export function osName(goos) {
  return { darwin: 'macOS', windows: 'Windows', linux: 'Linux' }[goos] || goos || '?';
}

/** "4 GB of 16 GB free" style figures from bytes. */
export function gb(n) {
  if (!n) return '';
  return `${(n / (1 << 30)).toFixed(n < 10 * (1 << 30) ? 1 : 0)} GB`;
}

/** Tags typed into one box, as the bridge takes them. */
export function parseTags(text) {
  return [...new Set(String(text || '').split(/[\s,]+/).map((t) => t.trim().toLowerCase()).filter(Boolean))];
}

// The escape sequences a shell prints for colours, cursor moves and window
// titles, which a plain text view leaves out.
const ANSI = /\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[()][0-9A-Za-z]|[=>78MDEc])/g;

/**
 * termText appends a terminal's output to the text shown so far, as a
 * line-oriented view: escape sequences out, a carriage return starts its
 * line over, a backspace takes back a character, other control characters
 * out. It keeps at most max characters (the end).
 */
export function termText(shown, chunk, max = 200000) {
  let out = shown || '';
  const s = String(chunk || '').replace(ANSI, '').replace(/\r\n/g, '\n');
  for (const ch of s) {
    if (ch === '\r') {
      out = out.slice(0, out.lastIndexOf('\n') + 1);
    } else if (ch === '\b') {
      if (out && !out.endsWith('\n')) out = out.slice(0, -1);
    } else if (ch === '\n' || ch === '\t' || ch >= ' ') {
      if (ch !== '\x7f') out += ch;
    }
  }
  return out.length > max ? out.slice(out.length - max) : out;
}

/**
 * base64 → UTF-8 text (Go's []byte arrives as base64). Pass the same
 * decoder for every chunk of one stream, so a character split between two
 * chunks comes out whole.
 */
export function fromBase64(b64, decoder = new TextDecoder()) {
  if (!b64) return '';
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return decoder.decode(bytes, { stream: true });
}

/** The node of a's list a command agent names, by id or by name; null for none. */
export function nodeNamed(nodes, ref) {
  const list = nodes || [];
  return list.find((n) => n.id === ref) || list.find((n) => (n.name || '').toLowerCase() === String(ref || '').toLowerCase()) || null;
}

const sameArgs = (a, b) => (a || []).length === (b || []).length && (a || []).every((x, i) => x === b[i]);

// Whether a rule allows argv, as the node checks it (ExecRule.Matches).
function matches(r, argv) {
  if (!argv.length || argv[0] !== r.name) return false;
  if (r.exact) return sameArgs(r.args, argv.slice(1));
  if (r.args === null || r.args === undefined) return true;
  const res = r.args.map((p) => { try { return new RegExp(`^(?:${p})$`); } catch { return null; } }).filter(Boolean);
  return argv.slice(1).every((x) => res.some((re) => re.test(x)));
}

const under = (folder, path) => {
  const norm = (s) => String(s || '').replace(/\\/g, '/').replace(/\/+$/, '');
  const f = norm(folder);
  return f !== '' && norm(path).startsWith(`${f}/`);
};

/**
 * How a command agent's program stands on node n (a GET /api/nodes
 * entry), before the agent is saved: {state, text}, state being allowed,
 * waiting (saving adds it), refused, not-accepted or no-node — as the
 * bridge's programOn says it for a saved agent.
 */
export function programOnNode(n, argv) {
  if (!n) return { state: 'no-node', text: 'There is no such node: the Nodes page lists them.' };
  const name = n.name || n.id;
  if (!argv || !argv.length || !argv[0]) return { state: 'refused', text: 'Name the program first.' };
  const eff = (n.effective && n.effective.exec) || {};
  const own = (eff.allow || []).filter((r) => !r.core);
  if (own.some((r) => matches(r, argv))) return { state: 'allowed', text: `Node ${name}’s node.json allows ${argv[0]}.` };
  if ((eff.allow || []).some((r) => r.core && matches(r, argv))) return { state: 'allowed', text: `Node ${name} runs it already, for another approved agent.` };
  const declared = (n.declared && n.declared.exec && n.declared.exec.accept_from_core) || [];
  const folders = eff.accept_from_core || [];
  if (!declared.length) return { state: 'not-accepted', text: `Node ${name} accepts no programs from the core. On ${name}, add the program’s folder to exec.accept_from_core in its node.json, or allow the program there (docs/NODES.md, “Programs from the core”).` };
  if (!folders.length) return { state: 'not-accepted', text: `Node ${name} accepts programs from ${declared.join(', ')}, which waits for your approval on the Nodes page.` };
  if (!folders.some((f) => under(f, argv[0]))) return { state: 'not-accepted', text: `${argv[0]} is in none of the folders node ${name} accepts programs from (${folders.join(', ')}).` };
  return { state: 'waiting', text: `Saving adds it to node ${name}’s allowlist, with exactly these arguments; the node checks it is there, is no shell, and stays inside ${folders.join(', ')}.` };
}
