// A page's context. Everything a page starts — polls, requests, timers,
// listeners, store subscriptions, the resources it follows — belongs to it
// and ends when the user leaves, so nothing keeps fetching or painting into
// a page that is gone.

import { makePoll } from './poll.js';

/** @import { Poll } from './poll.js' */
/** @import { Resource } from './resources.js' */
/** @import { Store } from './store.js' */

/**
 * What the router gives createPage: the route's params and how the page
 * reaches the address bar, the tab's title and the focus.
 * @typedef {object} PageHooks
 * @property {Record<string, string>} [params]
 * @property {(hash: string) => void} [navigate]
 * @property {(hash: string) => void} [replaceHash]
 * @property {(title: string) => void} [setTitle]
 * @property {(heading: HTMLElement) => void} [focusAt]
 */

/**
 * A page's context: what render(root, ctx) gets.
 * @typedef {ReturnType<typeof createPage>} Page
 */

/**
 * A page module's default export: its title in the browser tab (or how
 * the route's params make it) and how it renders into root. A page
 * declares it with `export default /** @satisfies {PageModule} *\/ ({...})`,
 * which types render's root and ctx.
 * @typedef {object} PageModule
 * @property {string | ((params: Record<string, string>) => string)} title
 * @property {(root: HTMLElement, ctx: Page) => void} render
 */

/**
 * createPage({params, navigate, replaceHash, setTitle, focusAt}) returns
 * the ctx a page's render(root, ctx) gets. The router supplies
 * navigate(hash), which goes to another page as a link does (asking first
 * about unsaved changes), replaceHash(hash), which changes the address
 * without leaving the page, setTitle(title), which names the page in the
 * browser tab, and focusAt(heading), which puts the focus there instead
 * of on the page's h1 when the router moves it to the page.
 * @param {PageHooks} [hooks]
 */
export function createPage({ params = {}, navigate = (hash) => { location.hash = hash; }, replaceHash = () => {}, setTitle = () => {}, focusAt = () => {} } = {}) {
  const abort = new AbortController();
  /** @type {Poll[]} */
  const polls = [];
  /** @type {Set<ReturnType<typeof setTimeout>>} */
  const timers = new Set();
  /** @type {(() => void)[]} */
  const cleanups = [];
  const ctx = {
    params,
    /** Aborted when the page goes: pass it to one-off requests that paint into the page. */
    signal: abort.signal,
    dead: false,
    /** @type {(() => boolean) | null} when it returns true, leaving asks for confirmation */
    dirty: null,

    /**
     * Polls fn(signal) every interval ms until the page goes; pass signal to its requests.
     * @param {(signal: AbortSignal) => unknown} fn
     * @param {number | (() => number)} [interval]
     */
    poll(fn, interval = 3000) {
      const p = makePoll(fn, interval);
      polls.push(p);
      return p;
    },

    /**
     * setTimeout that never fires after the page is gone.
     * @param {() => void} fn
     * @param {number} ms
     */
    timeout(fn, ms) {
      const t = setTimeout(() => { timers.delete(t); fn(); }, ms);
      timers.add(t);
      return t;
    },

    /**
     * A function that runs fn ms after its last call, unless the page is gone by then.
     * @template {unknown[]} A
     * @param {(...args: A) => void} fn
     * @param {number} ms
     * @returns {(...args: A) => void}
     */
    debounce(fn, ms) {
      /** @type {ReturnType<typeof setTimeout> | null} */
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
     * @template T
     * @param {Resource<T>} resource
     * @param {(data: T) => void} fn
     * @param {number | (() => number)} [interval]
     */
    follow(resource, fn, interval = 3000) {
      cleanups.push(resource.follow(fn, interval));
    },

    /**
     * store.subscribe(select, fn) until the page goes: fn gets the value now and on every change.
     * @template S, V
     * @param {Store<S>} store
     * @param {(state: S) => V} select
     * @param {(value: V) => void} fn
     */
    watch(store, select, fn) {
      cleanups.push(store.subscribe(select, fn));
    },

    /**
     * Runs fn when the page goes, e.g. an unsubscribe.
     * @param {() => void} fn
     */
    onCleanup(fn) { cleanups.push(fn); },

    /** @param {string} hash */
    navigate(hash) { if (!ctx.dead) navigate(hash); },

    /** @param {string} hash */
    replaceHash(hash) { if (!ctx.dead) replaceHash(hash); },

    /**
     * Renames the page once it knows better than its route, e.g. a brain's name.
     * @param {string} title
     */
    setTitle(title) { if (!ctx.dead) setTitle(title); },

    /**
     * The heading of the part the address names, e.g. a Settings section:
     * the focus goes there rather than to the page's h1, if the router
     * moves it to this page (after a link, not when the dashboard opens).
     * @param {HTMLElement} heading
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
