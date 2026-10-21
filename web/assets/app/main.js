// Entry point: signing in from a login link, the app shell and its router
// (navigation.js), the polls every page needs, and the "Connect to your
// bridge" screen shown on 401.

import { signIn, on as onApi } from './api.js';
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
import { credentialFromHash } from './router.js';

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
    if (!credentialFromHash(location.hash)) return false;
    signInFromHash().then((ok) => {
      if (ok) {
        if (view.paused()) unblock();
      } else if (!view.paused()) {
        toast(signInProblem, 'error');
      }
    });
    return true;
  },
});

// ── signing in from the URL fragment ─────────────────────────────────────

// Why the last link did not sign in, for the connect screen.
let signInProblem = '';

// Signs in with a #login=… link (`fridolin open`) or an older #token=… one.
// The fragment leaves the address first, so the browser keeps it nowhere. A
// link the bridge refuses leaves the session this browser had as it was:
// a site that opened the dashboard cannot sign it out, or in as someone
// else, with a link of its own. Reports whether it signed in.
async function signInFromHash() {
  const credential = credentialFromHash(location.hash);
  if (!credential) return false;
  view.replace('#/overview');
  try {
    await signIn(credential);
    signInProblem = '';
    return true;
  } catch (e) {
    signInProblem = e.status === 401 ? 'That link was already used or has expired. Run fridolin open, or send !dashboard in your WhatsApp chat, for a new one.' : e.message;
    return false;
  }
}
await signInFromHash();

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
  if (!location.hash || location.hash === '#' || credentialFromHash(location.hash)) view.replace('#/overview');
  view.resume();
  resumePolls();
  loadGroups(true);
}

onApi('unauthorized', () => {
  showConnect(signInProblem);
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
