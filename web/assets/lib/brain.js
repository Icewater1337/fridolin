// Brain ids, defaults and routing in words. Pure, so unit-tested.

import { plural } from './format.js';

/** @import { Brain, BrainManifest, BrainSandboxRuntime, Routing, SandboxRuntime } from '../../types/api.js' */

export const BRAIN_ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/;
/** @type {[string, string][]} [value, label] */
export const EFFORTS = [['', 'Inherit'], ['low', 'Low'], ['medium', 'Medium'], ['high', 'High'], ['xhigh', 'Extra high'], ['max', 'Max']];
// What a brain does with untagged group messages (brain.json "observe").
/** @type {[string, string][]} [value, label] */
export const OBSERVE = [['history', 'Keep them in the history'], ['run', 'Also run the brain on each, observe-only']];
// Whether the brain asks you before an irreversible step (brain.json "approvals").
/** @type {[string, string][]} [value, label] */
export const APPROVALS = [['ask', 'Ask me first, with a poll'], ['auto', 'Act without asking']];
export const DEFAULT_BACKUP_EXCLUDES = ['node_modules/', '*.log', '.DS_Store', '.venv/', '__pycache__/'];

// Where a brain's runs happen (config.json and brain.json "sandbox",
// docs/SANDBOXES.md). The UI never says bubblewrap: on a native Mac it is
// sandbox-exec, and either way it is Fridolin's own sandbox.
/** @type {Record<SandboxRuntime, string>} */
export const RUNTIME_NAMES = { bubblewrap: 'Fridolin’s sandbox', 'docker-sandbox': 'Docker Sandbox' };
// What a docker-sandbox brain does while its sandbox is not available.
/** @type {[string, string][]} [value, label] */
export const SANDBOX_FALLBACKS = [['bubblewrap', 'Fall back to Fridolin’s sandbox'], ['refuse', 'Refuse']];
// The most extra hosts a brain may reach (brain.json sandbox.egress).
export const MAX_EGRESS = 50;

/**
 * The name of a runtime; bubblewrap for one unset or unknown.
 * @param {string | null | undefined} rt
 */
export function runtimeName(rt) {
  return rt === 'docker-sandbox' ? RUNTIME_NAMES['docker-sandbox'] : RUNTIME_NAMES.bubblewrap;
}

/**
 * Where a brain's runs happen, from its runtime view in /api/brains:
 * state docker (in a Docker Sandbox), fridolin (in Fridolin's sandbox, as
 * set), fallback (a Docker Sandbox is set but not available, so
 * Fridolin's sandbox runs it) or refused (not available, and the brain
 * refuses rather than fall back); text says it in a sentence, pill in a
 * word or two. null without a view, from a bridge that has none.
 * @param {BrainSandboxRuntime | null | undefined} rs
 * @returns {{state: 'docker' | 'fridolin' | 'fallback' | 'refused', text: string, pill: string} | null}
 */
export function runtimeState(rs) {
  if (!rs) return null;
  const why = (/** @type {string} */ s) => (rs.reason ? `${s}: ${rs.reason}` : s);
  if (rs.configured !== 'docker-sandbox') return { state: 'fridolin', text: 'Runs in Fridolin’s sandbox', pill: 'Fridolin’s sandbox' };
  if (rs.effective === 'docker-sandbox' && rs.ready !== false) {
    return { state: 'docker', text: rs.node ? `Runs in a Docker Sandbox on ${rs.node}` : 'Runs in a Docker Sandbox', pill: 'Docker Sandbox' };
  }
  if (rs.fallback === 'refuse' && rs.effective !== 'bubblewrap') {
    return { state: 'refused', text: why('Docker Sandbox set, but runs are refused until it is back'), pill: 'Refusing runs' };
  }
  return { state: 'fallback', text: why('Docker Sandbox set, but runs in Fridolin’s sandbox now'), pill: 'Fallback' };
}

/**
 * Why an extra host is not one sbx takes, or "": a host or pattern such
 * as api.github.com, *.wikipedia.org or **:443, with an optional port,
 * and nothing else.
 * @param {string} host
 */
export function egressProblem(host) {
  const v = String(host || '').trim();
  if (!v) return '';
  if (/:\/\/|\//.test(v)) return 'Only the host, without https:// or a path';
  if (!/^[A-Za-z0-9*._-]+(:(\d{1,5}|\*))?$/.test(v)) return 'A host such as api.github.com or *.wikipedia.org, with :port if needed';
  return '';
}

/**
 * How brain.json disk weakens the sandbox of the approved brain.json a,
 * one sentence each: a Docker Sandbox given up, falling back where it
 * refused, or more hosts to reach. defaultRuntime is config.json's
 * sandbox.runtime, which a brain.json without its own inherits.
 * @param {Partial<BrainManifest> | null | undefined} a
 * @param {Partial<BrainManifest> | null | undefined} disk
 * @param {string | null | undefined} defaultRuntime
 * @returns {string[]}
 */
export function sandboxWeaker(a, disk, defaultRuntime) {
  const rt = (/** @type {Partial<BrainManifest> | null | undefined} */ m) => (m && m.sandbox && m.sandbox.runtime) || defaultRuntime || 'bubblewrap';
  const refuses = (/** @type {Partial<BrainManifest> | null | undefined} */ m) => !!(m && m.sandbox && m.sandbox.fallback === 'refuse');
  const egress = (/** @type {Partial<BrainManifest> | null | undefined} */ m) => (m && m.sandbox && m.sandbox.egress) || [];
  const out = [];
  if (rt(a) === 'docker-sandbox' && rt(disk) !== 'docker-sandbox') {
    out.push('It would run in Fridolin’s sandbox instead of a Docker Sandbox.');
  } else if (rt(disk) === 'docker-sandbox' && refuses(a) && !refuses(disk)) {
    out.push('While its Docker Sandbox is not available, it would run in Fridolin’s sandbox instead of refusing.');
  }
  const before = new Set(egress(a));
  const added = egress(disk).filter((x) => !before.has(x));
  if (added.length) out.push(`It could also reach ${added.join(', ')}.`);
  return out;
}

/**
 * A brain id suggested from a name: lowercase, dashes, at most 40 characters.
 * @param {unknown} s
 */
export function slugify(s) {
  let out = '';
  let dash = false;
  for (const ch of String(s).toLowerCase()) {
    if (/[a-z0-9]/.test(ch)) { out += ch; dash = false; } else if (/[-_ .]/.test(ch)) {
      if (!dash && out) { out += '-'; dash = true; }
    }
  }
  out = out.replace(/^-+|-+$/g, '');
  if (out.length > 40) out = out.slice(0, 40).replace(/-+$/, '');
  return out || 'brain';
}

/**
 * A brain as a choice in a list: its name, with the id when that differs.
 * @param {{id: string, name?: string}} b
 */
export function brainLabel(b) {
  return b.name && b.name !== b.id ? `${b.name} (${b.id})` : b.id;
}

// The id that stands for the bridge's own settings where backups list
// brains by id: bridgeBackupID in backup.go, which says why it must not
// change. No brain can have it, since brain ids start with a letter or
// digit.
export const BRIDGE_BACKUP_ID = '_bridge';

/**
 * Whose backup id is, as a backup job or a failed backup names it: the
 * bridge's settings, or the brain of that id in brains (the id itself
 * when it is not there).
 * @param {string} id
 * @param {{id: string, name?: string}[] | null | undefined} brains
 */
export function backupOwner(id, brains) {
  if (id === BRIDGE_BACKUP_ID) return 'Bridge settings';
  const b = (brains || []).find((x) => x.id === id);
  return b ? (b.name || b.id) : id;
}

// What a brain's issue kinds (and status, which names the worst of them)
// are called: short on a pill, long where the brain is not named around it.
/** @type {Record<string, [string, string]>} */
const ISSUES = {
  drift: ['Changed on disk', 'brain.json changed on disk'],
  missing: ['Folder missing', 'Brain folder missing'],
  invalid: ['Invalid', 'Invalid brain configuration'],
  unapproved: ['Not approved', 'Brain not approved'],
};

/**
 * The name of an issue kind (long unless short is set); an unknown kind as it is.
 * @param {string | null | undefined} kind
 * @param {{short?: boolean}} [options]
 */
export function issueLabel(kind, { short = false } = {}) {
  const names = kind ? ISSUES[kind] : undefined;
  return names ? names[short ? 0 : 1] : String(kind || '');
}

/**
 * A brain's chats in words. fallback: the bridge marks it in /api/brains
 * as the brain the chats no other brain claims go to, which without a
 * default brain is the first one; noDMBrain: no brain handles direct
 * messages, so those go to the fallback brain too (see hasDMBrain).
 * @param {Partial<Routing> | null | undefined} routing
 * @param {{fallback?: boolean, noDMBrain?: boolean}} [options]
 */
export function routingSummary(routing, { fallback = false, noDMBrain = false } = {}) {
  if (!routing && !fallback) return 'No chats';
  const r = routing || {};
  const parts = [];
  if (r.handles_dms) parts.push('Direct messages');
  if (fallback) parts.push(noDMBrain ? 'Fallback for other chats and direct messages' : 'Fallback for other chats');
  else if (r.default) parts.push('Default for other chats');
  const n = (r.groups || []).length;
  if (n) parts.push(plural(n, 'group'));
  return parts.length ? parts.join(' · ') : 'No chats routed yet';
}

/** @returns {Partial<Routing>} */
const routingOf = (/** @type {Brain | null | undefined} */ b) => (b && b.manifest && b.manifest.routing) || {};

/**
 * Whether a brain of the /api/brains list handles direct messages. Its
 * approved brain.json says so, whether it runs or not: a DM brain that
 * cannot run hands them to no other brain.
 * @param {Brain[] | null | undefined} brains
 */
export function hasDMBrain(brains) {
  return (brains || []).some((b) => routingOf(b).handles_dms);
}

/**
 * The fallback brain of the /api/brains list (see routingSummary), or null.
 * @param {Brain[] | null | undefined} brains
 */
function fallbackOf(brains) {
  return (brains || []).find((b) => b.fallback) || null;
}

/**
 * The Chats page's choice for a group that names no brain, which the fallback brain answers.
 * @param {Brain[] | null | undefined} brains
 */
export function defaultBrainOption(brains) {
  const f = fallbackOf(brains);
  return f ? `Default brain (${f.name || f.id})` : 'Default brain';
}

/**
 * Who answers the chats of brain id once it is removed, from the
 * /api/brains list: brain, the fallback brain then (if id is the fallback,
 * the bridge picks another: the default brain, else the first brain that
 * runs), or null when no brain would; and dms, true when that brain would
 * start answering direct messages, which no brain left handles and which
 * it did not get before. The bridge removes the first entry with that id
 * only: when a later one, a second folder claiming the id, is left, it
 * takes the brain's place with its own brain.json, and copy is true.
 * @param {Brain[] | null | undefined} brains
 * @param {string} id
 * @returns {{brain: Brain | null, dms: boolean, copy: boolean}}
 */
export function takeover(brains, id) {
  const list = brains || [];
  const at = list.findIndex((b) => b.id === id);
  const gone = at < 0 ? null : list[at];
  const rest = list.filter((_, i) => i !== at);
  const copy = rest.find((b) => b.id === id);
  if (copy) return { brain: copy, dms: false, copy: true };
  const wasFallback = !!gone && !!gone.fallback;
  const brain = wasFallback
    ? rest.find((b) => b.runtime && routingOf(b).default) || rest.find((b) => b.runtime) || null
    : fallbackOf(rest);
  const hadDMs = wasFallback || (!!gone && !!routingOf(gone).handles_dms);
  return { brain, dms: !!brain && hadDMs && !hasDMBrain(rest), copy: false };
}
