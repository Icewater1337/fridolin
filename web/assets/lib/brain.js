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

export function routingSummary(routing) {
  if (!routing) return 'No chats';
  const parts = [];
  if (routing.handles_dms) parts.push('Direct messages');
  if (routing.default) parts.push('Default for other chats');
  const n = (routing.groups || []).length;
  if (n) parts.push(plural(n, 'group'));
  return parts.length ? parts.join(' · ') : 'No chats routed yet';
}
