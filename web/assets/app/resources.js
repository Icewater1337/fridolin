// What the bridge's GET endpoints answer, fetched once for the whole
// dashboard. The shell and every page that shows the same data follow the
// same resource, so no page asks for what the shell or another part of the
// page asks for already. A resource polls only while something follows it,
// as often as its most eager follower wants, and tells its followers only
// when the answer changed.

import { get } from './api.js';
import { makePoll } from './poll.js';

/**
 * createResource(load) → {follow(fn, interval), refresh(), data()}.
 * load(signal) fetches the data. follow calls fn(data) with what is known
 * already, and again after every answer that differs from the one before
 * (compared as JSON); it fetches at once when nothing is known yet or the
 * data is older than interval ms (a number, or a function returning one),
 * and returns the function that stops following. refresh() fetches now,
 * dropping a fetch in flight, and resolves once the followers have the
 * answer.
 */
export function createResource(load) {
  const followers = new Set();
  let poll = null;
  let known = false;
  let value;
  let sig = '';
  let at = 0; // when the last answer came
  let loading = false; // a fetch is on its way, and its answer goes to every follower

  const each = (f) => (typeof f.interval === 'function' ? f.interval() : f.interval);
  const interval = () => Math.min(...[...followers].map(each));
  const tell = (f) => { try { f.fn(value); } catch (e) { console.error(e); } };

  async function run(signal) {
    loading = true;
    let next;
    try {
      next = await load(signal);
    } finally {
      loading = false;
    }
    if (signal.aborted) return; // a refresh asked again meanwhile
    at = Date.now();
    const s = JSON.stringify(next);
    if (known && s === sig) return;
    known = true;
    value = next;
    sig = s;
    [...followers].forEach(tell);
  }

  return {
    follow(fn, every = 3000) {
      const f = { fn, interval: every };
      followers.add(f);
      if (known) tell(f);
      if (!poll) poll = makePoll(run, interval);
      else if (!loading && (!known || Date.now() - at > each(f))) poll.kick();
      return () => {
        if (!followers.delete(f) || followers.size) return;
        poll.stop();
        poll = null;
      };
    },
    refresh() { return poll ? poll.kick() : Promise.resolve(); },
    data() { return known ? value : undefined; },
  };
}

const all = new Map();

/** The one resource for GET path (with its query); options.timeout for a slow endpoint. */
export function resource(path, { timeout } = {}) {
  let r = all.get(path);
  if (!r) {
    r = createResource((signal) => get(path, { signal, timeout }));
    all.set(path, r);
  }
  return r;
}

export const statusResource = resource('/api/status');
export const brainsResource = resource('/api/brains');
export const configResource = resource('/api/config');
export const ignoredResource = resource('/api/chats/ignored');
