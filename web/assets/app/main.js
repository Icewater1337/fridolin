// Entry point: token handling, the app shell and its router
// (navigation.js), the polls every page needs, and the "Connect to your
// bridge" screen shown on 401.

import { setToken, getToken, on as onApi } from './api.js';
import { ignoredResource } from './resources.js';
import { pollStatus, setIgnoredCount, loadGroups, chatName } from './store.js';
import { mount } from '../lib/dom.js';
import { toast } from '../components/toast.js';
import { confirmDialog } from '../components/dialog.js';
import { setChatNames } from '../components/chat.js';
import { keepTimesFresh } from '../components/common.js';
import { restartBridge } from './restart.js';
import { makePoll, pausePollsWhen, resumePolls, retryPolls } from './poll.js';
import { createShell } from './shell.js';
import { connectScreen } from './connect.js';
import { createNavigation } from './navigation.js';
import { ROUTES } from './routes.js';
import { tokenFromHash } from './router.js';

// ── shell, router and connect screen ─────────────────────────────────────

const app = document.getElementById('app');
const shell = createShell({ onRestart: () => restartBridge() });

// Every address change goes through the guard, which asks before a page
// with unsaved changes is left and keeps the history intact either way.
const view = createNavigation({
  win: window,
  shell,
  routes: ROUTES,
  confirm: () => confirmDialog({ title: 'Discard changes?', message: 'You have unsaved changes on this page. Leave without saving?', confirm: 'Discard changes', danger: true }),
  onAddress: () => {
    if (!takeTokenFromHash()) return false;
    if (view.paused()) unblock();
    return true;
  },
});

// ── token from the URL fragment ──────────────────────────────────────────

function takeTokenFromHash() {
  const t = tokenFromHash(location.hash);
  if (t === null) return false;
  setToken(t);
  view.replace('#/overview');
  return true;
}
takeTokenFromHash();

// The connect screen pauses the router and the polls until it closes.
pausePollsWhen(() => document.hidden || view.paused());
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
  if (view.paused()) return;
  view.pause();
  app.className = 'connect';
  connectScreen(app, { reason, onConnected: unblock });
}

function unblock() {
  showShell();
  if (!location.hash || location.hash === '#' || location.hash.startsWith('#token=')) view.replace('#/overview');
  view.resume();
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

window.addEventListener('beforeunload', (e) => {
  if (view.dirty()) {
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

if (!location.hash || location.hash === '#') view.replace('#/overview');
if (!view.paused()) {
  showShell();
  view.start();
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
