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

/** @import { GetResponse } from '../../types/api.js' */
/** @import { Poll } from './poll.js' */

/**
 * Data the dashboard follows: see createResource.
 * @template T
 * @typedef {object} Resource
 * @property {(fn: (data: T) => void, interval?: number | (() => number)) => () => void} follow
 * @property {() => Promise<void>} refresh
 * @property {() => T | undefined} data
 */

/**
 * One follow() of a resource: what it calls, and how fresh it wants the data.
 * @template T
 * @typedef {{fn: (data: T) => void, interval: number | (() => number)}} Follower
 */

/**
 * createResource(load) → {follow(fn, interval), refresh(), data()}.
 * load(signal) fetches the data. follow calls fn(data) with what is known
 * already, and again after every answer that differs from the one before
 * (the same object, or one equal as JSON, does not); it fetches at once
 * when nothing is known yet or the data is older than interval ms (a
 * number, or a function returning one), and returns the function that
 * stops following. refresh() fetches now, dropping a fetch in flight, and
 * resolves once the followers have the answer.
 * @template T
 * @param {(signal: AbortSignal) => Promise<T>} load
 * @returns {Resource<T>}
 */
export function createResource(load) {
  /** @type {Set<Follower<T>>} */
  const followers = new Set();
  /** @type {Poll | null} */
  let poll = null;
  let known = false;
  /** @type {T} */
  let value;
  let sig = '';
  let at = 0; // when the last answer came
  let loading = false; // a fetch is on its way, and its answer goes to every follower

  const each = (/** @type {Follower<T>} */ f) => (typeof f.interval === 'function' ? f.interval() : f.interval);
  const interval = () => Math.min(...[...followers].map(each));
  const tell = (/** @type {Follower<T>} */ f) => { try { f.fn(value); } catch (e) { console.error(e); } };

  /** @param {AbortSignal} signal */
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
      /** @type {Follower<T>} */
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

/**
 * resource()'s options: timeout (ms) for a slow endpoint, missing for
 * what a 404 stands for.
 * @typedef {{timeout?: number, missing?: unknown}} ResourceOptions
 */

/** @type {Map<string, {r: Resource<any>, options: ResourceOptions}>} */
const all = new Map(); // path → {r: the resource, options: the ones it was made with}

/**
 * The one resource for GET path (with its query); the options of the
 * first call make it. options.timeout is for a slow endpoint;
 * options.missing, when given, is what a 404 stands for (null for a brain
 * that is not there, say) instead of an error. A later call may leave
 * the options out, to refresh the resource say, but one that gives
 * another missing throws: every follower gets the same answers, so a 404
 * cannot be data for one and an error for another.
 * @template {string} P
 * @overload
 * @param {P} path
 * @param {{timeout?: number, missing: null}} options
 * @returns {Resource<GetResponse<P> | null>}
 */
/**
 * A missing of null gets the overload above, since a null literal
 * would make M any while strictNullChecks is off.
 * @template {string} P
 * @template M
 * @overload
 * @param {P} path
 * @param {{timeout?: number, missing: M}} options
 * @returns {Resource<GetResponse<P> | M>}
 */
/**
 * @template {string} P
 * @overload
 * @param {P} path
 * @param {{timeout?: number}} [options]
 * @returns {Resource<GetResponse<P>>}
 */
/**
 * @param {string} path
 * @param {ResourceOptions} [options]
 * @returns {Resource<any>}
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
/**
 * @param {string} path
 * @param {ResourceOptions} options
 * @returns {(signal: AbortSignal) => Promise<any>}
 */
function revalidating(path, { timeout, ...options }) {
  /** @type {{data: any, etag: string} | null} */
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
