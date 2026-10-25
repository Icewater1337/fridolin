// A browser window for the router's tests (nav-guard.test.js,
// navigation.test.js) with just enough history: entries with a hash and a
// state, a position, and the events a browser fires, later, as a browser
// does.

export function fakeWindow(start = ['#/overview']) {
  const listeners = { hashchange: [], popstate: [], click: [] };
  const entries = start.map((hash) => ({ hash, state: null }));
  let at = entries.length - 1;
  const fire = (type, e = { type }) => [...listeners[type]].forEach((fn) => fn(e));
  const later = (fn) => setImmediate(fn);
  const history = {
    get state() { return entries[at].state; },
    replaceState(state, _title, url) { entries[at] = { hash: url === undefined ? entries[at].hash : url, state }; },
    go(n) {
      later(() => {
        const to = at + n;
        if (to < 0 || to >= entries.length || n === 0) return;
        const from = entries[at].hash;
        at = to;
        fire('popstate');
        if (entries[at].hash !== from) fire('hashchange');
      });
    },
  };
  const location = {
    get hash() { return entries[at].hash; },
    set hash(v) {
      if (v === entries[at].hash) return;
      entries.splice(at + 1);
      entries.push({ hash: v, state: null });
      at += 1;
      later(() => { fire('popstate'); fire('hashchange'); });
    },
  };
  const on = (type, fn) => listeners[type].push(fn);
  const win = { history, location, document: { addEventListener: on }, addEventListener: on, scrollTo() {} };
  return {
    win,
    hashes: () => entries.map((e) => e.hash),
    at: () => at,
    /**
     * A click on a link: the browser follows it unless a listener (or,
     * with handled, the link's own handler after them) prevented that.
     * The rest of opts (metaKey and the like) goes into the event.
     * @param {string} href
     * @param {{handled?: boolean} & Partial<Pick<MouseEvent, 'button' | 'metaKey' | 'ctrlKey' | 'shiftKey' | 'altKey'>>} [opts]
     */
    click(href, { handled = false, ...mods } = {}) {
      const a = { getAttribute: (k) => (k === 'href' ? href : null) };
      // closest('a[href^="…"]') finds the link when its href starts so.
      const closest = (sel) => { const m = sel.match(/^a\[href\^="([^"]*)"\]$/); return m && href.startsWith(m[1]) ? a : null; };
      const e = { type: 'click', button: 0, ...mods, target: { closest }, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
      fire('click', e);
      if (handled) e.preventDefault();
      if (!e.defaultPrevented) location.hash = href;
    },
  };
}
