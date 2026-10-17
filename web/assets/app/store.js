// The state pages share, in one small store: the latest /api/status (polled
// for every page), the WhatsApp groups and the names of chats, and how many
// chats wait for access.

import { get } from './api.js';
import { statusResource } from './resources.js';

/**
 * createStore(initial) → {get(), set(patch), subscribe(select, fn)}.
 * subscribe calls fn(select(state)) at once and again whenever that value
 * changes (compared with Object.is), and returns the unsubscribe function.
 */
export function createStore(initial) {
  let state = initial;
  const subs = new Set();
  const call = (sub) => {
    const v = sub.select(state);
    if (sub.called && Object.is(v, sub.last)) return;
    sub.called = true;
    sub.last = v;
    try { sub.fn(v); } catch (e) { console.error(e); }
  };
  return {
    get: () => state,
    set(patch) {
      state = { ...state, ...patch };
      [...subs].forEach(call);
    },
    subscribe(select, fn) {
      const sub = { select, fn, called: false, last: undefined };
      subs.add(sub);
      call(sub);
      return () => { subs.delete(sub); };
    },
  };
}

export const store = createStore({
  status: null, // the latest /api/status, null until it arrives
  groups: null, // [{jid, name, participants, allowed, brain}], or null when unknown
  groupsError: '',
  names: new Map(), // chat JID → name; replaced (not changed) when a name is learnt
  ignoredCount: 0, // chats waiting for access
});

export function setStatus(status) { store.set({ status }); }

/**
 * Follows /api/status into the store for every page (the app starts it
 * once); then(status) runs after each new answer. Returns {stop()}.
 */
export function pollStatus(then = () => {}) {
  const stop = statusResource.follow((status) => {
    setStatus(status);
    then(status);
  }, 3000);
  return { stop };
}

/** Asks for /api/status now, dropping a request in flight; resolves once the answer is in. */
export function refreshStatus() { return statusResource.refresh(); }

export function setIgnoredCount(ignoredCount) { store.set({ ignoredCount }); }

/**
 * Shows a save's restart_required at once, and asks for the status again:
 * a poll that started before the save would land with the old list and
 * hide the restart banner until the next poll.
 */
export function setRestartRequired(fields) {
  const { status } = store.get();
  if (status) store.set({ status: { ...status, restart_required: fields || [] } });
  refreshStatus();
}

/** The name of a chat, '' when unknown. */
export function chatName(jid) { return store.get().names.get(jid) || ''; }

function learnNames(pairs) {
  const known = store.get().names;
  const fresh = pairs.filter(([jid, name]) => jid && name && known.get(jid) !== name);
  if (fresh.length) store.set({ names: new Map([...known, ...fresh]) });
}

let groupsLoading = null;
let groupsAt = 0;

/** Loads the WhatsApp groups list (cached for a minute). Never throws. */
export async function loadGroups(force = false) {
  if (groupsLoading) return groupsLoading;
  const { groups } = store.get();
  if (!force && groups && Date.now() - groupsAt < 60000) return groups;
  groupsLoading = (async () => {
    try {
      const list = await get('/api/whatsapp/groups');
      store.set({ groups: Array.isArray(list) ? list : [], groupsError: '' });
      learnNames(store.get().groups.map((g) => [g.jid, g.name]));
    } catch (e) {
      store.set({ groupsError: e.message || 'WhatsApp is not connected', ...(force ? { groups: null } : {}) });
    } finally {
      groupsAt = Date.now();
      groupsLoading = null;
    }
    return store.get().groups;
  })();
  return groupsLoading;
}

/** Remembers names seen elsewhere (e.g. the waiting-chats list). */
export function rememberName(jid, name) {
  if (!store.get().names.has(jid)) learnNames([[jid, name]]);
}
