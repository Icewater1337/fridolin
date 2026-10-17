// Shared state: the latest /api/status (polled by app.js for every page),
// group names for nicer chat labels, and a pending-restart flag.

import { get } from './api.js';

export const store = {
  status: null,
  groups: null, // [{jid, name, participants, allowed, brain}] or null when unknown
  groupsError: '',
  groupNames: new Map(),
  restartRequired: [], // settings that only apply after a restart
  ignoredCount: 0,
};

const statusSubs = new Set();
const ignoredSubs = new Set();

export function onIgnoredCount(fn) {
  ignoredSubs.add(fn);
  return () => ignoredSubs.delete(fn);
}

export function setIgnoredCount(n) {
  store.ignoredCount = n;
  ignoredSubs.forEach((fn) => fn(n));
}
const restartSubs = new Set();

/** Calls fn(status) whenever a fresh status arrives (and now, if one is cached). */
export function onStatus(fn) {
  statusSubs.add(fn);
  if (store.status) fn(store.status);
  return () => statusSubs.delete(fn);
}

export function setStatus(s) {
  store.status = s;
  statusSubs.forEach((fn) => { try { fn(s); } catch (e) { console.error(e); } });
}

export function onRestartChange(fn) {
  restartSubs.add(fn);
  return () => restartSubs.delete(fn);
}

export function setRestartRequired(fields) {
  const merged = new Set([...(store.restartRequired || []), ...(fields || [])]);
  store.restartRequired = fields === null ? [] : [...merged];
  restartSubs.forEach((fn) => fn(store.restartRequired));
}

let groupsLoading = null;
let groupsAt = 0;

/** Loads the WhatsApp groups list (cached for a minute). Never throws. */
export async function loadGroups(force = false) {
  if (groupsLoading) return groupsLoading;
  if (!force && store.groups && Date.now() - groupsAt < 60000) return store.groups;
  groupsLoading = (async () => {
    try {
      const groups = await get('/api/whatsapp/groups');
      store.groups = Array.isArray(groups) ? groups : [];
      store.groupsError = '';
      for (const g of store.groups) if (g.name) store.groupNames.set(g.jid, g.name);
    } catch (e) {
      store.groupsError = e.message || 'WhatsApp is not connected';
      if (force) store.groups = null;
    } finally {
      groupsAt = Date.now();
      groupsLoading = null;
    }
    return store.groups;
  })();
  return groupsLoading;
}

/** Remembers names seen elsewhere (e.g. the waiting-chats list). */
export function rememberName(jid, name) {
  if (jid && name && !store.groupNames.has(jid)) store.groupNames.set(jid, name);
}
