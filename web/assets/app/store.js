// The state pages share, in one small store: the latest /api/status (polled
// for every page), the WhatsApp groups and the names of chats, and how many
// chats wait for access.

import { get } from './api.js';
import { statusResource } from './resources.js';

/** @import { Status, WhatsAppGroup } from '../../types/api.js' */

/**
 * A store of state S: see createStore.
 * @template S
 * @typedef {object} Store
 * @property {() => S} get
 * @property {(patch: Partial<S>) => void} set
 * @property {<V>(select: (state: S) => V, fn: (value: V) => void) => () => void} subscribe
 */

/**
 * One subscribe() of a store: what it selects, what it calls, and what it
 * was last called with.
 * @template S
 * @typedef {{select: (state: S) => unknown, fn: (value: any) => void, called: boolean, last: unknown}} Sub
 */

/**
 * createStore(initial) → {get(), set(patch), subscribe(select, fn)}.
 * subscribe calls fn(select(state)) at once and again whenever that value
 * changes (compared with Object.is), and returns the unsubscribe function.
 * @template {object} S
 * @param {S} initial
 * @returns {Store<S>}
 */
export function createStore(initial) {
  let state = initial;
  /** @type {Set<Sub<S>>} */
  const subs = new Set();
  const call = (/** @type {Sub<S>} */ sub) => {
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

/**
 * What the store holds.
 * @typedef {object} State
 * @property {Status | null} status the latest /api/status, null until it arrives
 * @property {WhatsAppGroup[] | null} groups null when unknown
 * @property {string} groupsError
 * @property {Map<string, string>} names chat JID → name; replaced (not changed) when a name is learnt
 * @property {number} ignoredCount chats waiting for access
 */

export const store = createStore(/** @type {State} */ ({
  status: null, // the latest /api/status, null until it arrives
  groups: null, // [{jid, name, participants, allowed, brain}], or null when unknown
  groupsError: '',
  names: new Map(), // chat JID → name; replaced (not changed) when a name is learnt
  ignoredCount: 0, // chats waiting for access
}));

/** @param {Status} status */
export function setStatus(status) { store.set({ status }); }

/**
 * Follows /api/status into the store for every page (the app starts it
 * once); then(status) runs after each new answer. Returns {stop()}.
 * @param {(status: Status) => void} [then]
 */
export function pollStatus(then = () => {}) {
  const stop = statusResource.follow((status) => {
    setStatus(status);
    then(status);
  }, 3000);
  return { stop };
}

/** @param {number} ignoredCount */
export function setIgnoredCount(ignoredCount) { store.set({ ignoredCount }); }

/**
 * Shows a save's restart_required at once, and asks for the status again:
 * a poll that started before the save would land with the old list and
 * hide the restart banner until the next poll.
 * @param {string[] | null | undefined} fields
 */
export function setRestartRequired(fields) {
  const { status } = store.get();
  if (status) store.set({ status: { ...status, restart_required: fields || [] } });
  statusResource.refresh();
}

/**
 * The name of a chat, '' when unknown.
 * @param {string} jid
 */
export function chatName(jid) { return store.get().names.get(jid) || ''; }

/** @param {[string, string][]} pairs [jid, name] */
function learnNames(pairs) {
  const known = store.get().names;
  const fresh = pairs.filter(([jid, name]) => jid && name && known.get(jid) !== name);
  if (fresh.length) store.set({ names: new Map([...known, ...fresh]) });
}

/** @type {Promise<WhatsAppGroup[] | null> | null} */
let groupsLoading = null;
let groupsAt = 0;

/**
 * Loads the WhatsApp groups list (cached for a minute). Never throws.
 * force skips the minute. The bridge answers from the list it keeps,
 * which group events refresh; fresh has it ask WhatsApp instead, for a
 * change that came without one, after a load already on its way.
 * @returns {Promise<WhatsAppGroup[] | null>}
 */
export async function loadGroups(force = false, { fresh = false } = {}) {
  if (groupsLoading) {
    if (!fresh) return groupsLoading;
    await groupsLoading;
    return loadGroups(force, { fresh });
  }
  const { groups } = store.get();
  if (!force && !fresh && groups && Date.now() - groupsAt < 60000) return groups;
  groupsLoading = (async () => {
    try {
      const list = await get(fresh ? '/api/whatsapp/groups?fresh=1' : '/api/whatsapp/groups');
      store.set({ groups: Array.isArray(list) ? list : [], groupsError: '' });
      learnNames(store.get().groups.map((g) => /** @type {[string, string]} */ ([g.jid, g.name])));
    } catch (e) {
      store.set({ groupsError: (e instanceof Error && e.message) || 'WhatsApp is not connected', ...(force ? { groups: null } : {}) });
    } finally {
      groupsAt = Date.now();
      groupsLoading = null;
    }
    return store.get().groups;
  })();
  return groupsLoading;
}

/**
 * Remembers names seen elsewhere (e.g. the waiting-chats list).
 * @param {string} jid
 * @param {string} name
 */
export function rememberName(jid, name) {
  if (!store.get().names.has(jid)) learnNames([[jid, name]]);
}
