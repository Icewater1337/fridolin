// Brain ids, defaults and routing in words. Pure, so unit-tested.

import { plural } from './format.js';

export const BRAIN_ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/;
export const EFFORTS = [['', 'Inherit'], ['low', 'Low'], ['medium', 'Medium'], ['high', 'High'], ['xhigh', 'Extra high'], ['max', 'Max']];
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

export function routingSummary(routing) {
  if (!routing) return 'No chats';
  const parts = [];
  if (routing.handles_dms) parts.push('Direct messages');
  if (routing.default) parts.push('Default for other chats');
  const n = (routing.groups || []).length;
  if (n) parts.push(plural(n, 'group'));
  return parts.length ? parts.join(' · ') : 'No chats routed yet';
}
