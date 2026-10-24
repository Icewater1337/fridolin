// What the bridge's GET endpoints answer, fetched once for the whole
// dashboard. The shell and every page that shows the same data follow the
// same resource, so no page asks for what the shell or another part of the
// page asks for already. A resource polls only while something follows it,
// as often as its most eager follower wants, and tells its followers only
// when the answer changed. It sends back the ETag of the answer it has,
// so where the bridge sends one, an unchanged answer is a 304 that brings
// nothing to download or compare.

import { getIfChanged, NOT_MODIFIED } from './api.js';
import { makePoll } from './poll.js';

/**
 * createResource(load) → {follow(fn, interval), refresh(), data()}.
 * load(signal) fetches the data. follow calls fn(data) with what is known
 * already, and again after every answer that differs from the one before
 * (the same object, or one equal as JSON, does not); it fetches at once
 * when nothing is known yet or the data is older than interval ms (a
 * number, or a function returning one), and returns the function that
 * stops following. refresh() fetches now, dropping a fetch in flight, and
 * resolves once the followers have the answer.
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
    if (known && next === value) return; // what a 304 kept
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

const all = new Map(); // path → {r: the resource, options: the ones it was made with}

/**
 * The one resource for GET path (with its query); the options of the
 * first call make it. options.timeout is for a slow endpoint;
 * options.missing, when given, is what a 404 stands for (null for a brain
 * that is not there, say) instead of an error. A later call may leave
 * the options out, to refresh the resource say, but one that gives
 * another missing throws: every follower gets the same answers, so a 404
 * cannot be data for one and an error for another.
 */
export function resource(path, options = {}) {
  let e = all.get(path);
  if (!e) {
    e = { r: createResource(revalidating(path, options)), options };
    all.set(path, e);
  } else if ('missing' in options && (!('missing' in e.options) || options.missing !== e.options.missing)) {
    throw new Error(`resource(${path}) was made with another missing`);
  }
  return e.r;
}

// The loader of resource(path). It keeps the last answer that came with
// an ETag, together with it, and sends the ETag back: a 304 stands for
// that answer. A fetch the resource dropped (a refresh asked again) may
// have brought it; the resource then compares it with what it has as
// with any other answer. A 304 answers the ETag that went out, so it
// brings the data of that one, not of an answer a dropped fetch took in
// while it was on its way.
function revalidating(path, { timeout, ...options }) {
  let last = null; // {data, etag}
  return async (signal) => {
    const sent = last;
    let answer;
    try {
      answer = await getIfChanged(path, sent ? sent.etag : '', { signal, timeout });
    } catch (e) {
      if (e && e.status === 404 && 'missing' in options) {
        last = null;
        return options.missing;
      }
      throw e;
    }
    if (answer === NOT_MODIFIED) {
      last = sent;
      return sent.data;
    }
    last = answer.etag ? answer : null;
    return answer.data;
  };
}

export const statusResource = resource('/api/status');
export const brainsResource = resource('/api/brains');
export const configResource = resource('/api/config');
export const ignoredResource = resource('/api/chats/ignored');
export const nodesResource = resource('/api/nodes');
export const schedulesResource = resource('/api/schedules');
