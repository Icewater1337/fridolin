// What the Nodes page says about a node's capabilities, as the bridge's
// Capabilities.Summary says it in the CLI and !fleet.

/** @import { ExecCap, ExecRule, Node, NodeCapabilities, NodeUpdateStatus, ScheduleProgram } from '../../types/api.js' */

/**
 * "exec (uname, df), fs (2 roots, 1 writable), host, notify, fride, fride-host (2 instances), backup (1 folder)", or "nothing".
 * @param {NodeCapabilities | null | undefined} c
 */
export function capSummary(c) {
  const parts = [];
  if (c && c.exec && (c.exec.allow || []).length) {
    parts.push(`exec (${c.exec.allow.map((r) => r.name).join(', ')})`);
  }
  const fromCore = (c && c.exec && c.exec.accept_from_core) || [];
  if (fromCore.length) {
    parts.push(`exec from the core (${fromCore.join(', ')})`);
  }
  const roots = (c && c.fs && c.fs.roots) || [];
  if (roots.length) {
    const w = roots.filter((r) => r.write).length;
    parts.push(`fs (${roots.length} root${roots.length === 1 ? '' : 's'}${w ? `, ${w} writable` : ''})`);
  }
  if (c && c.host) parts.push('host');
  if (c && c.notify) parts.push('notify');
  if (c && c.fride) parts.push('fride');
  const host = c && c.fride_host;
  if (host) {
    const n = (host.instances || []).length;
    parts.push(host.ready ? `fride-host (${n} instance${n === 1 ? '' : 's'})` : 'fride-host (not ready)');
  }
  if (c && c.backup) {
    const n = (c.backup.folders || []).length;
    parts.push(n ? `backup (${n} folder${n === 1 ? '' : 's'})` : 'backup');
  }
  if (c && c.update) parts.push('update');
  for (const k of /** @type {const} */ (['agent', 'terminal'])) {
    const n = (c && c[k] && c[k].roots) ? c[k].roots.length : 0;
    if (n) parts.push(`${k} (${n} folder${n === 1 ? '' : 's'})`);
  }
  if (c && c.core_host) parts.push('core-host');
  return parts.length ? parts.join(', ') : 'nothing';
}

/**
 * How an exec rule reads: "git status|log", "hostname (no arguments)", "go (any arguments)".
 * @param {ExecRule} r
 */
export function ruleText(r) {
  if (r.exact) return `${[r.name, ...(r.args || [])].join(' ')} (exactly)`;
  if (r.args === null || r.args === undefined) return `${r.name} (any arguments)`;
  if (!r.args.length) return `${r.name} (no arguments)`;
  return `${r.name} ${r.args.join(' | ')}`;
}

/**
 * How a node's last update from the core went, in a few words; null for none.
 * @param {NodeUpdateStatus | null | undefined} u
 */
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

/**
 * The OS as people name it.
 * @param {string | undefined} goos
 */
export function osName(goos) {
  return /** @type {Record<string, string>} */ ({ darwin: 'macOS', windows: 'Windows', linux: 'Linux' })[goos || ''] || goos || '?';
}

/**
 * "4 GB of 16 GB free" style figures from bytes.
 * @param {number | null | undefined} n
 */
export function gb(n) {
  if (!n) return '';
  return `${(n / (1 << 30)).toFixed(n < 10 * (1 << 30) ? 1 : 0)} GB`;
}

/**
 * Tags typed into one box, as the bridge takes them.
 * @param {string | null | undefined} text
 */
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
 * @param {string} shown
 * @param {string} chunk
 * @param {number} [max]
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
 * @param {string | null | undefined} b64
 * @param {TextDecoder} [decoder]
 */
export function fromBase64(b64, decoder = new TextDecoder()) {
  if (!b64) return '';
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return decoder.decode(bytes, { stream: true });
}

/**
 * The nodes of a's list a command agent's ref could name, as the bridge
 * finds its node (nodes.ResolveIn, #251): the one with that id, else
 * those with that name in any case, a revoked one only when no other
 * has the name. One is the node it names; more name none of them.
 * @param {Node[] | null | undefined} nodes
 * @param {string} ref
 * @returns {Node[]}
 */
export function nodesNamed(nodes, ref) {
  const list = nodes || [];
  const want = String(ref || '').trim();
  if (!want) return [];
  const byID = list.find((n) => n.id === want);
  if (byID) return [byID];
  const called = list.filter((n) => (n.name || '').toLowerCase() === want.toLowerCase());
  const live = called.filter((n) => n.device !== 'revoked');
  return live.length ? live : called;
}

/**
 * How a command agent names node n of nodes: by its name when that names
 * n alone (nodesNamed), else by its id.
 * @param {Node[]} nodes
 * @param {Node} n
 */
export function nodeRef(nodes, n) {
  const found = n.name ? nodesNamed(nodes, n.name) : [];
  return found.length === 1 && found[0] === n ? n.name : n.id;
}

/**
 * How a command agent's program stands on the node ref names, before the
 * agent is saved: programOnNode, or why ref names no node, as the
 * bridge's programOn says it.
 * @param {Node[] | null | undefined} nodes
 * @param {string} ref
 * @param {string[]} argv
 * @returns {{state: ScheduleProgram['state'], text: string}}
 */
export function programOn(nodes, ref, argv) {
  const found = nodesNamed(nodes, ref);
  if (found.length > 1) {
    const which = found.map((n) => `${n.name} (${n.id})`).join(', ');
    return { state: 'no-node', text: `${found.length} nodes are called ${String(ref).trim()}: ${which}. Name one by its id.` };
  }
  return programOnNode(found[0] || null, argv);
}

/** @type {(a: string[] | null, b: string[]) => boolean} */
const sameArgs = (a, b) => (a || []).length === (b || []).length && (a || []).every((x, i) => x === b[i]);

/**
 * Whether a rule allows argv, as the node checks it (ExecRule.Matches).
 * @param {ExecRule} r
 * @param {string[]} argv
 */
function matches(r, argv) {
  if (!argv.length || argv[0] !== r.name) return false;
  if (r.exact) return sameArgs(r.args, argv.slice(1));
  if (r.args === null || r.args === undefined) return true;
  const res = r.args.map((p) => { try { return new RegExp(`^(?:${p})$`); } catch { return null; } }).filter((re) => re !== null);
  return argv.slice(1).every((x) => res.some((re) => re.test(x)));
}

/** @type {(folder: string, path: string) => boolean} */
const under = (folder, path) => {
  /** @param {string} s */
  const norm = (s) => String(s || '').replace(/\\/g, '/').replace(/\/+$/, '');
  const f = norm(folder);
  return f !== '' && norm(path).startsWith(`${f}/`);
};

/**
 * How a command agent's program stands on node n (a GET /api/nodes
 * entry), before the agent is saved: {state, text}, state being allowed,
 * waiting (saving adds it), refused, not-accepted or no-node — as the
 * bridge's programOn says it for a saved agent.
 * @param {Node | null} n
 * @param {string[]} argv
 * @returns {{state: ScheduleProgram['state'], text: string}}
 */
export function programOnNode(n, argv) {
  if (!n) return { state: 'no-node', text: 'There is no such node: the Nodes page lists them.' };
  const name = n.name || n.id;
  if (!argv || !argv.length || !argv[0]) return { state: 'refused', text: 'Name the program first.' };
  /** @type {Partial<ExecCap>} */
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
