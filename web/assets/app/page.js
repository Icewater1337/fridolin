// A page's context. Everything a page starts — polls, requests, timers,
// listeners, store subscriptions — belongs to it and ends when the user
// leaves, so nothing keeps fetching or painting into a page that is gone.

import { makePoll } from './poll.js';

/**
 * createPage({params, replaceHash, setTitle}) returns the ctx a page's
 * render(root, ctx) gets. The router supplies replaceHash(hash), which
 * changes the address without leaving the page, and setTitle(title), which
 * names the page in the browser tab.
 */
export function createPage({ params = {}, replaceHash = () => {}, setTitle = () => {} } = {}) {
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

    /** store.subscribe(select, fn) until the page goes: fn gets the value now and on every change. */
    watch(store, select, fn) {
      cleanups.push(store.subscribe(select, fn));
    },

    /** Runs fn when the page goes, e.g. an unsubscribe. */
    onCleanup(fn) { cleanups.push(fn); },

    navigate(hash) { location.hash = hash; },

    replaceHash(hash) { if (!ctx.dead) replaceHash(hash); },

    /** Renames the page once it knows better than its route, e.g. a brain's name. */
    setTitle(title) { if (!ctx.dead) setTitle(title); },

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
