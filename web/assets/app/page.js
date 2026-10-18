// A page's context. Everything a page starts — polls, requests, timers,
// listeners, store subscriptions, the resources it follows — belongs to it
// and ends when the user leaves, so nothing keeps fetching or painting into
// a page that is gone.

import { makePoll } from './poll.js';

/**
 * createPage({params, navigate, replaceHash, setTitle, focusAt}) returns
 * the ctx a page's render(root, ctx) gets. The router supplies
 * navigate(hash), which goes to another page as a link does (asking first
 * about unsaved changes), replaceHash(hash), which changes the address
 * without leaving the page, setTitle(title), which names the page in the
 * browser tab, and focusAt(heading), which puts the focus there instead
 * of on the page's h1 when the router moves it to the page.
 */
export function createPage({ params = {}, navigate = (hash) => { location.hash = hash; }, replaceHash = () => {}, setTitle = () => {}, focusAt = () => {} } = {}) {
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

    /**
     * resource.follow(fn, interval) until the page goes: fn gets the data
     * now (when known) and whenever it changes; interval (ms, or a function
     * returning them) is how fresh this page wants it.
     */
    follow(resource, fn, interval = 3000) {
      cleanups.push(resource.follow(fn, interval));
    },

    /** store.subscribe(select, fn) until the page goes: fn gets the value now and on every change. */
    watch(store, select, fn) {
      cleanups.push(store.subscribe(select, fn));
    },

    /** Runs fn when the page goes, e.g. an unsubscribe. */
    onCleanup(fn) { cleanups.push(fn); },

    navigate(hash) { if (!ctx.dead) navigate(hash); },

    replaceHash(hash) { if (!ctx.dead) replaceHash(hash); },

    /** Renames the page once it knows better than its route, e.g. a brain's name. */
    setTitle(title) { if (!ctx.dead) setTitle(title); },

    /**
     * The heading of the part the address names, e.g. a Settings section:
     * the focus goes there rather than to the page's h1, if the router
     * moves it to this page (after a link, not when the dashboard opens).
     */
    focusAt(heading) { if (!ctx.dead) focusAt(heading); },

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
