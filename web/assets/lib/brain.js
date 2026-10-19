// Brain ids, defaults and routing in words. Pure, so unit-tested.

import { plural } from './format.js';

export const BRAIN_ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/;
export const EFFORTS = [['', 'Inherit'], ['low', 'Low'], ['medium', 'Medium'], ['high', 'High'], ['xhigh', 'Extra high'], ['max', 'Max']];
// What a brain does with untagged group messages (brain.json "observe").
export const OBSERVE = [['history', 'Keep them in the history'], ['run', 'Also run the brain on each, observe-only']];
export const DEFAULT_BACKUP_EXCLUDES = ['node_modules/', '*.log', '.DS_Store', '.venv/', '__pycache__/'];

/** A brain id suggested from a name: lowercase, dashes, at most 40 characters. */
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

/** A brain as a choice in a list: its name, with the id when that differs. */
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
 */
export function backupOwner(id, brains) {
  if (id === BRIDGE_BACKUP_ID) return 'Bridge settings';
  const b = (brains || []).find((x) => x.id === id);
  return b ? (b.name || b.id) : id;
}

// What a brain's issue kinds (and status, which names the worst of them)
// are called: short on a pill, long where the brain is not named around it.
const ISSUES = {
  drift: ['Changed on disk', 'brain.json changed on disk'],
  missing: ['Folder missing', 'Brain folder missing'],
  invalid: ['Invalid', 'Invalid brain configuration'],
  unapproved: ['Not approved', 'Brain not approved'],
};

/** The name of an issue kind (long unless short is set); an unknown kind as it is. */
export function issueLabel(kind, { short = false } = {}) {
  const names = ISSUES[kind];
  return names ? names[short ? 0 : 1] : String(kind || '');
}

/**
 * A brain's chats in words. fallback: the bridge marks it in /api/brains
 * as the brain the chats no other brain claims go to, which without a
 * default brain is the first one; noDMBrain: no brain handles direct
 * messages, so those go to the fallback brain too (see hasDMBrain).
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

const routingOf = (b) => (b && b.manifest && b.manifest.routing) || {};

/**
 * Whether a brain of the /api/brains list handles direct messages. Its
 * approved brain.json says so, whether it runs or not: a DM brain that
 * cannot run hands them to no other brain.
 */
export function hasDMBrain(brains) {
  return (brains || []).some((b) => routingOf(b).handles_dms);
}

/** The fallback brain of the /api/brains list (see routingSummary), or null. */
function fallbackOf(brains) {
  return (brains || []).find((b) => b.fallback) || null;
}

/** The Chats page's choice for a group that names no brain, which the fallback brain answers. */
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
