// Entry point: token handling, the router that renders a page into the app
// shell, the polls every page needs, and the "Connect to your bridge" screen
// shown on 401.

import { setToken, getToken, on as onApi } from './api.js';
import { ignoredResource } from './resources.js';
import { pollStatus, setIgnoredCount, loadGroups, chatName } from './store.js';
import { h, mount } from '../lib/dom.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { confirmDialog, closeAllDialogs } from '../components/dialog.js';
import { setChatNames } from '../components/chat.js';
import { keepTimesFresh } from '../components/common.js';
import { restartBridge } from './restart.js';
import { makePoll, pausePollsWhen, resumePolls, retryPolls } from './poll.js';
import { createPage } from './page.js';
import { createShell, pageFailed } from './shell.js';
import { connectScreen } from './connect.js';
import { guardNavigation } from './nav-guard.js';
import { ROUTES } from './routes.js';
import { matchRoute, activeNav, docTitle, tokenFromHash } from './router.js';

// ── navigation ───────────────────────────────────────────────────────────

let current = null; // the page context in use

// Every address change goes through the guard, which asks before a page
// with unsaved changes is left and keeps the history intact either way.
const nav = guardNavigation({
  win: window,
  isDirty: () => !!(current && current.dirty && current.dirty()),
  confirm: () => confirmDialog({ title: 'Discard changes?', message: 'You have unsaved changes on this page. Leave without saving?', confirm: 'Discard changes', danger: true }),
  onDiscard: () => { if (current) current.dirty = null; },
  onNavigate: () => {
    if (takeTokenFromHash()) {
      if (blocked) unblock();
      return;
    }
    renderRoute();
  },
});

// ── token from the URL fragment ──────────────────────────────────────────

function takeTokenFromHash() {
  const t = tokenFromHash(location.hash);
  if (t === null) return false;
  setToken(t);
  nav.replace('#/overview');
  return true;
}
takeTokenFromHash();

// ── shell and connect screen ─────────────────────────────────────────────

const app = document.getElementById('app');
const shell = createShell({ onRestart: () => restartBridge() });
let blocked = false; // true while the connect screen is shown

pausePollsWhen(() => document.hidden || blocked);
document.addEventListener('visibilitychange', () => {
  if (document.hidden) return;
  resumePolls();
  retryPolls();
});
window.addEventListener('online', () => retryPolls());

function showShell() {
  app.className = 'shell';
  mount(app, shell.nodes);
}

function showConnect(reason) {
  if (blocked) return;
  blocked = true;
  if (current) { current.destroy(); current = null; }
  app.className = 'connect';
  connectScreen(app, { reason, onConnected: unblock });
}

function unblock() {
  blocked = false;
  showShell();
  if (!location.hash || location.hash === '#' || location.hash.startsWith('#token=')) nav.replace('#/overview');
  renderRoute();
  resumePolls();
  loadGroups(true);
}

onApi('unauthorized', () => {
  showConnect(getToken() ? 'The saved token was not accepted — the bridge may have a new token. Open the dashboard again with the command below.' : '');
});

// Polls wait longer after each failure; the first answer brings them back.
onApi('reachable', (ok) => {
  shell.setOffline(!ok);
  if (ok) retryPolls();
});

// ── routing ──────────────────────────────────────────────────────────────

async function renderRoute() {
  if (blocked) return;
  const match = matchRoute(ROUTES, location.hash);
  if (current) current.destroy();
  closeAllDialogs();
  const ctx = createPage({
    params: match ? match.params : {},
    navigate: (hash) => nav.go(hash),
    replaceHash: (hash) => nav.replace(hash),
    setTitle(title) { document.title = docTitle(title); },
  });
  current = ctx;
  const root = h('div.page', { 'data-page': match ? match.route.id : 'not-found' });
  mount(shell.page, root);
  shell.setActive(activeNav(match && match.route));
  shell.closeNav();
  window.scrollTo(0, 0);
  if (!match) {
    document.title = docTitle('Not found');
    mount(root, h('div.empty', h('div.empty-title', 'Page not found'), h('p', 'There is nothing at this address.'), btn('Go to the overview', { href: '#/overview', kind: 'primary' })));
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
}

window.addEventListener('beforeunload', (e) => {
  if (current && current.dirty && current.dirty()) {
    e.preventDefault();
    e.returnValue = '';
  }
});

// ── polls every page needs ───────────────────────────────────────────────

setChatNames(chatName);
keepTimesFresh();

let lastWaMode = '';
pollStatus((s) => {
  const mode = s.whatsapp && s.whatsapp.mode;
  if (mode === 'connected' && lastWaMode !== 'connected') loadGroups(true);
  lastWaMode = mode;
});

ignoredResource.follow((list) => setIgnoredCount(Array.isArray(list) ? list.length : 0), 10000);

makePoll(async () => { await loadGroups(); }, 60000);

// ── boot ─────────────────────────────────────────────────────────────────

if (!location.hash || location.hash === '#') nav.replace('#/overview');
if (!blocked) {
  showShell();
  renderRoute();
}
// Tells theme.js's boot watch that the app started.
document.documentElement.dataset.ready = '1';

// Surface unexpected script errors instead of failing silently.
window.addEventListener('unhandledrejection', (e) => {
  const err = e.reason;
  if (err && (err.status === 401 || err.status === 0 || err.name === 'AbortError')) return;
  console.error(err);
  if (err && err.message) toast(err.message, 'error');
});
