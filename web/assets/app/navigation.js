// The router: renders the page the address names into the shell, through
// the guard that asks before unsaved changes are left (nav-guard.js), and
// moves the keyboard focus to a page the person went to. It is apart from
// main.js, which starts the whole app as it loads, so that a test can
// drive it (web/test/navigation.test.js).

import { h, mount } from '../lib/dom.js';
import { btn } from '../components/buttons.js';
import { closeAllDialogs } from '../components/dialog.js';
import { createPage } from './page.js';
import { pageFailed } from './shell.js';
import { guardNavigation } from './nav-guard.js';
import { matchRoute, activeNav, docTitle } from './router.js';

/**
 * createNavigation({win, shell, routes, confirm, onAddress}) renders the
 * route of routes that win's address names into shell (createShell's).
 * confirm() asks whether to leave unsaved changes, resolving true to
 * leave. onAddress() sees each new address first and returns true when
 * it took care of it (a #token=… link), which renders nothing. Returns
 * {start(), pause(), resume(), paused(), dirty(), go(hash),
 * replace(hash)}: start renders the page the dashboard opened on, pause
 * leaves the page for the connect screen and renders nothing until
 * resume, and go and replace are the guard's.
 */
export function createNavigation({ win, shell, routes, confirm, onAddress = () => false }) {
  let current = null; // the page context in use
  let paused = false; // the connect screen is shown instead

  const dirty = () => !!(current && current.dirty && current.dirty());
  const nav = guardNavigation({
    win,
    isDirty: dirty,
    confirm,
    onDiscard: () => { if (current) current.dirty = null; },
    onNavigate: () => { if (!onAddress()) render({ focus: true }); },
  });

  // focus moves the keyboard focus to the new page once it rendered: to
  // its heading (shell.focusPage), or to the part the page names with
  // ctx.focusAt, e.g. the section a Settings link points to. Following a
  // link and coming back from the connect screen do that; opening the
  // dashboard leaves the focus where the browser puts it.
  async function render({ focus }) {
    if (paused) return;
    const match = matchRoute(routes, win.location.hash);
    if (current) current.destroy();
    closeAllDialogs();
    let placed = null; // where the focus was put
    // A page that names its part late moves the focus only while it is
    // still where it was put: the person may have moved on.
    const place = (el) => {
      if (!focus || ctx.dead || (placed && document.activeElement !== placed)) return;
      placed = shell.focusPage(el);
    };
    const ctx = createPage({
      params: match ? match.params : {},
      navigate: (hash) => nav.go(hash),
      replaceHash: (hash) => nav.replace(hash),
      setTitle(title) { document.title = docTitle(title); },
      focusAt: (el) => place(el),
    });
    current = ctx;
    const root = h('div.page', { 'data-page': match ? match.route.id : 'not-found' });
    mount(shell.page, root);
    shell.setActive(activeNav(match && match.route));
    shell.closeNav();
    win.scrollTo(0, 0);
    const done = () => { if (!placed) place(); };
    if (!match) {
      document.title = docTitle('Not found');
      mount(root, h('div.empty', h('div.empty-title', 'Page not found'), h('p', 'There is nothing at this address.'), btn('Go to the overview', { href: '#/overview', kind: 'primary' })));
      done();
      return;
    }
    let page;
    try {
      page = (await match.route.load()).default;
    } catch (e) {
      if (ctx.dead) return;
      console.error(e);
      document.title = docTitle('Error');
      mount(root, pageFailed(e));
      done();
      return;
    }
    if (ctx.dead) return;
    ctx.setTitle(typeof page.title === 'function' ? page.title(ctx.params) : page.title);
    try {
      page.render(root, ctx);
    } catch (e) {
      console.error(e);
      mount(root, pageFailed(e));
    }
    done();
  }

  return {
    start() { render({ focus: false }); },
    pause() {
      paused = true;
      if (current) { current.destroy(); current = null; }
    },
    resume() {
      paused = false;
      render({ focus: true });
    },
    paused: () => paused,
    dirty,
    go: nav.go,
    replace: nav.replace,
  };
}
