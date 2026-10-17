// A page's context. Everything a page starts — polls, requests, timers,
// listeners — belongs to it and ends when the user leaves, so nothing keeps
// fetching or painting into a page that is gone.

import { makePoll } from './poll.js';

/**
 * createPage({params, replaceHash}) returns the ctx a page's render(root,
 * ctx) gets. replaceHash(hash) changes the address without leaving the page;
 * the router supplies it.
 */
export function createPage({ params = {}, replaceHash = () => {} } = {}) {
  const abort = new AbortController();
  const polls = [];
  const timers = new Set();
  const cleanups = [];
  const ctx = {
    params,
    /** Aborted when the page goes: pass it to one-off requests that paint into the page. */
    signal: abort.signal,
    dead: false,
    dirty: null, // () => boolean; when true, leaving asks for confirmation

    /** Polls fn(signal) every interval ms until the page goes; pass signal to its requests. */
    poll(fn, interval = 3000) {
      const p = makePoll(fn, interval);
      polls.push(p);
      return p;
    },

    /** setTimeout that never fires after the page is gone. */
    timeout(fn, ms) {
      const t = setTimeout(() => { timers.delete(t); fn(); }, ms);
      timers.add(t);
      return t;
    },

    /** A function that runs fn ms after its last call, unless the page is gone by then. */
    debounce(fn, ms) {
      let t = null;
      return (...args) => {
        if (t !== null) { clearTimeout(t); timers.delete(t); }
        t = ctx.timeout(() => { t = null; fn(...args); }, ms);
      };
    },

    /** addEventListener on something that outlives the page (window, document). */
    listen(target, type, fn, opts) {
      target.addEventListener(type, fn, opts);
      cleanups.push(() => target.removeEventListener(type, fn, opts));
    },

    /** Runs fn when the page goes, e.g. an unsubscribe. */
    onCleanup(fn) { cleanups.push(fn); },

    navigate(hash) { location.hash = hash; },

    replaceHash(hash) { if (!ctx.dead) replaceHash(hash); },

    destroy() {
      if (ctx.dead) return;
      ctx.dead = true;
      abort.abort();
      polls.forEach((p) => p.stop());
      timers.forEach((t) => clearTimeout(t));
      timers.clear();
      cleanups.forEach((fn) => { try { fn(); } catch (e) { console.error(e); } });
    },
  };
  return ctx;
}
