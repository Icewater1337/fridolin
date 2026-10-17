// Entry point: token handling, the app shell (sidebar / top bar), the hash
// router, page-scoped polling that pauses in background tabs, and the
// "Connect to your bridge" screen shown on 401.

import { get, setToken, getToken, on as onApi } from './api.js';
import { store, setStatus, onStatus, onRestartChange, loadGroups, setIgnoredCount, onIgnoredCount } from './state.js';
import { h, mount } from '../lib/dom.js';
import { plural } from '../lib/format.js';
import { waKind, waLabel } from '../lib/whatsapp.js';
import { icon } from '../components/icons.js';
import { toast, toastError } from '../components/toast.js';
import { btn, copyButton } from '../components/buttons.js';
import { confirmDialog, closeAllDialogs } from '../components/dialog.js';
import { textInput } from '../components/fields.js';
import { setChatNames } from '../components/chat.js';
import { restartBridge } from './restart.js';

import { ROUTES } from './routes.js';
import { BRAND, matchRoute, routeHref, activeNav, docTitle } from './router.js';

// ── token from the URL fragment ──────────────────────────────────────────

function takeTokenFromHash() {
  if (location.hash.startsWith('#token=')) {
    const t = decodeURIComponent(location.hash.slice('#token='.length));
    setToken(t);
    history.replaceState(null, '', `${location.pathname}${location.search}#/overview`);
    return true;
  }
  return false;
}
takeTokenFromHash();

// ── theme ────────────────────────────────────────────────────────────────

const THEMES = ['auto', 'light', 'dark'];
function getTheme() {
  try { return localStorage.getItem('bridgeTheme') || 'auto'; } catch { return 'auto'; }
}
function applyTheme(t) {
  if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);
  else document.documentElement.removeAttribute('data-theme');
  try { localStorage.setItem('bridgeTheme', t); } catch { /* ignore */ }
}
function themeButton() {
  const b = h('button.icon-btn.theme-toggle', { type: 'button' });
  const paint = () => {
    const t = getTheme();
    const label = { auto: 'Theme: follows your system', light: 'Theme: light', dark: 'Theme: dark' }[t];
    b.title = `${label} (click to change)`;
    b.setAttribute('aria-label', b.title);
    mount(b, icon(t === 'auto' ? 'auto' : t === 'light' ? 'sun' : 'moon'));
  };
  b.addEventListener('click', () => {
    const next = THEMES[(THEMES.indexOf(getTheme()) + 1) % THEMES.length];
    applyTheme(next);
    document.querySelectorAll('.theme-toggle').forEach((x) => x.dispatchEvent(new Event('repaint')));
  });
  b.addEventListener('repaint', paint);
  paint();
  return b;
}

// ── polling ──────────────────────────────────────────────────────────────

let blocked = false; // true while the connect screen is shown
const allPolls = new Set();

/**
 * Runs fn now and then every interval ms (a number or a function returning
 * one). Pauses while the tab is hidden or the connect screen is up; runs
 * never overlap. Errors other than 401/offline become (deduplicated) toasts.
 */
function makePoll(fn, interval) {
  const p = { timer: null, running: false, stopped: false, waiting: false, lastErr: '' };
  const next = () => (typeof interval === 'function' ? interval() : interval);
  const run = async () => {
    clearTimeout(p.timer);
    p.timer = null;
    if (p.stopped) return;
    if (document.hidden || blocked) { p.waiting = true; return; }
    p.waiting = false;
    p.running = true;
    try {
      await fn();
      p.lastErr = '';
    } catch (e) {
      if (e && e.status !== 401 && e.status !== 0 && e.name !== 'AbortError') {
        const m = e.message || String(e);
        if (m !== p.lastErr) toastError(e);
        p.lastErr = m;
      }
      if (e && e.status === undefined && e.name !== 'AbortError') console.error(e);
    } finally {
      p.running = false;
    }
    if (!p.stopped) p.timer = setTimeout(run, next());
  };
  p.kick = () => { if (!p.running && !p.stopped) run(); };
  p.stop = () => { p.stopped = true; clearTimeout(p.timer); allPolls.delete(p); };
  allPolls.add(p);
  run();
  return p;
}

document.addEventListener('visibilitychange', () => {
  if (!document.hidden) allPolls.forEach((p) => { if (p.waiting) p.kick(); });
});

function resumeAll() {
  allPolls.forEach((p) => { if (p.waiting || !p.running) p.kick(); });
}

// ── routing ──────────────────────────────────────────────────────────────

let current = null; // the page context in use
let shell = null;
let lastHash = '';

function makeCtx(params) {
  const polls = [];
  const cleanups = [];
  const ctx = {
    params,
    dead: false,
    dirty: null, // () => boolean; when true, leaving asks for confirmation
    poll(fn, interval = 3000) {
      const p = makePoll(async () => { if (!ctx.dead) await fn(); }, interval);
      polls.push(p);
      return p;
    },
    onCleanup(fn) { cleanups.push(fn); },
    navigate(hash) { location.hash = hash; },
    destroy() {
      ctx.dead = true;
      polls.forEach((p) => p.stop());
      cleanups.forEach((fn) => { try { fn(); } catch (e) { console.error(e); } });
    },
  };
  return ctx;
}

async function renderRoute() {
  if (!shell) return;
  const match = matchRoute(ROUTES, location.hash);
  if (current) current.destroy();
  closeAllDialogs();
  const ctx = makeCtx(match ? match.params : {});
  current = ctx;
  const root = h('div.page', { 'data-page': match ? match.route.id : 'not-found' });
  mount(shell.page, root);
  shell.setActive(activeNav(match && match.route));
  shell.closeNav();
  window.scrollTo(0, 0);
  lastHash = location.hash;
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
    mount(root, h('div.callout.bad', h('div.callout-body', h('strong', 'This page failed to load'), h('div', e.message || String(e)))));
    return;
  }
  if (ctx.dead) return;
  document.title = docTitle(typeof page.title === 'function' ? page.title(ctx.params) : page.title);
  try {
    page.render(root, ctx);
  } catch (e) {
    console.error(e);
    mount(root, h('div.callout.bad', h('div.callout-body', h('strong', 'This page failed to load'), h('div', e.message || String(e)))));
  }
}

window.addEventListener('hashchange', async () => {
  if (takeTokenFromHash()) {
    if (blocked) unblock();
    return;
  }
  if (current && current.dirty && current.dirty() && location.hash !== lastHash) {
    const target = location.hash;
    history.replaceState(null, '', lastHash || '#/overview');
    const ok = await confirmDialog({ title: 'Discard changes?', message: 'You have unsaved changes on this page. Leave without saving?', confirm: 'Discard changes', danger: true });
    if (!ok) return;
    current.dirty = null;
    location.hash = target;
    return;
  }
  renderRoute();
});

window.addEventListener('beforeunload', (e) => {
  if (current && current.dirty && current.dirty()) {
    e.preventDefault();
    e.returnValue = '';
  }
});

// ── shell ────────────────────────────────────────────────────────────────

function buildShell() {
  const app = document.getElementById('app');
  app.className = 'shell';
  const links = new Map();
  const nav = h('nav.nav', { 'aria-label': 'Main' });
  for (const route of ROUTES) {
    if (!route.nav) continue;
    const badgeEl = h('span.nav-badge', { hidden: true });
    const a = h('a.nav-link', { href: routeHref(route), hidden: route.nav.devOnly ? true : null }, icon(route.nav.icon), h('span', route.nav.label), badgeEl);
    a.badgeEl = badgeEl;
    links.set(route.id, a);
    nav.appendChild(a);
  }
  const menuBtn = h('button.icon-btn.menu-btn', { type: 'button', 'aria-label': 'Menu', 'aria-expanded': 'false' }, icon('menu'));
  const waDot = h('span.dot');
  const waText = h('span', 'Connecting…');
  const waLink = h('a.sidebar-status', { href: '#/overview', title: 'WhatsApp connection' }, waDot, waText);
  const version = h('span.version.muted');
  const sidebar = h('aside.sidebar',
    h('div.brand-row',
      h('a.brand', { href: '#/overview' },
        h('img.brand-logo', { src: '/assets/logo-mark.webp', alt: '', width: 34, height: 34 }),
        h('span.brand-text', h('span.brand-name', BRAND), h('span.brand-sub', 'WhatsApp × Claude'))),
      h('div.brand-tools', themeButton(), menuBtn)),
    nav,
    h('div.sidebar-foot', waLink, h('div.foot-row', version, themeButton())));
  const banners = h('div.banners');
  const offline = h('div.banner.bad', { hidden: true, role: 'alert' }, icon('alert'), h('span', 'Cannot reach the bridge. Retrying…'));
  const restartBanner = h('div.banner.warn', { hidden: true });
  banners.append(offline, restartBanner);
  const page = h('main.main', { id: 'main' });
  mount(app, sidebar, h('div.content', banners, page));

  menuBtn.addEventListener('click', () => {
    const open = !sidebar.classList.contains('nav-open');
    sidebar.classList.toggle('nav-open', open);
    menuBtn.setAttribute('aria-expanded', String(open));
  });

  const paintRestart = (fields) => {
    restartBanner.hidden = !fields.length;
    if (!fields.length) return;
    mount(restartBanner, icon('refresh'),
      h('span', 'Restart the bridge to apply: ', h('code.mono', fields.join(', '))),
      btn('Restart now', { kind: 'primary', small: true, onClick: () => restartBridge() }));
  };
  onRestartChange(paintRestart);
  paintRestart(store.restartRequired);

  shell = {
    page,
    setActive(id) {
      links.forEach((a, key) => {
        if (key === id) a.setAttribute('aria-current', 'page');
        else a.removeAttribute('aria-current');
      });
    },
    closeNav() { sidebar.classList.remove('nav-open'); menuBtn.setAttribute('aria-expanded', 'false'); },
    setOffline(off) { offline.hidden = !off; },
    paintStatus(s) {
      const devLink = links.get('dev');
      devLink.hidden = !s.dev_mode;
      version.textContent = s.version ? (/^\d/.test(s.version) ? `v${s.version}` : String(s.version)) : '';
      const wa = s.whatsapp || {};
      const kind = waKind(wa.mode);
      waDot.className = `dot ${kind}`;
      waText.textContent = waLabel(wa.mode);
      if (s.dev_mode) waText.textContent = 'Dev mode (no WhatsApp)';
      const issues = (s.issues || []).length;
      const ob = links.get('brains').badgeEl;
      ob.hidden = !issues;
      ob.textContent = String(issues);
      ob.title = issues ? plural(issues, 'brain issue') : '';
    },
    setChatsBadge(n) {
      const b = links.get('chats').badgeEl;
      b.hidden = !n;
      b.textContent = String(n);
      b.title = n ? `${plural(n, 'chat')} waiting for access` : '';
    },
  };
  if (store.status) shell.paintStatus(store.status);
  shell.setChatsBadge(store.ignoredCount);
}

// ── connect screen ───────────────────────────────────────────────────────

function showConnect(reason) {
  if (blocked) return;
  blocked = true;
  if (current) { current.destroy(); current = null; }
  shell = null;
  const app = document.getElementById('app');
  app.className = 'connect';
  const input = textInput({ placeholder: 'Paste the token (or the whole dashboard link)', class: 'mono', 'aria-label': 'Dashboard token' });
  const errorEl = h('p.form-error', { hidden: true });
  const form = h('form.connect-form', input, btn('Connect', { kind: 'primary', type: 'submit', icon: 'key' }));
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    let t = input.value.trim();
    const m = t.match(/#token=([A-Za-z0-9]+)/);
    if (m) t = m[1];
    if (!t) { input.focus(); return; }
    const submitBtn = form.querySelector('button');
    submitBtn.disabled = true;
    const prev = getToken();
    setToken(t);
    try {
      await get('/api/status');
      unblock();
    } catch (err) {
      setToken(prev);
      errorEl.hidden = false;
      errorEl.textContent = err.status === 401 ? 'That token was not accepted. Check that you copied all of it.' : err.message;
      blocked = true;
    } finally {
      submitBtn.disabled = false;
    }
  });
  const cmd = 'fridolin open';
  mount(app, h('div.connect-card.card',
    h('div.connect-head', h('img.brand-logo', { src: '/assets/logo-mark.webp', alt: '', width: 56, height: 56 }), h('div', h('h1', 'Connect to your bridge'), h('p.muted', 'The dashboard needs your bridge’s access token.'))),
    reason ? h('div.callout.warn', icon('alert', 'callout-icon'), h('div.callout-body', reason)) : null,
    h('ol.steps-list',
      h('li', h('div', 'In a terminal on this computer, run'), h('div.code-line', h('code.mono', cmd), copyButton(cmd)), h('div.muted.small', 'It opens this dashboard with the token filled in.')),
      h('li', h('div', 'Or paste the token from ', h('code.mono', '~/.fridolin/dashboard.token'), ' (in your data folder, if you moved it):'), form, errorEl)),
    h('div.connect-foot.muted.small', 'The token keeps programs on this computer — including your brains — from changing the bridge’s settings. It never leaves this machine.')));
  setTimeout(() => input.focus(), 50);
}

function unblock() {
  blocked = false;
  buildShell();
  if (!location.hash || location.hash === '#' || location.hash.startsWith('#token=')) history.replaceState(null, '', '#/overview');
  renderRoute();
  resumeAll();
  loadGroups(true);
}

onApi('unauthorized', () => {
  showConnect(getToken() ? 'The saved token was not accepted — the bridge may have a new token. Open the dashboard again with the command below.' : '');
});

onApi('reachable', (ok) => { if (shell) shell.setOffline(!ok); });

// ── global pollers ───────────────────────────────────────────────────────

setChatNames((jid) => store.groupNames.get(jid));

onStatus((s) => { if (shell) shell.paintStatus(s); });
onIgnoredCount((n) => { if (shell) shell.setChatsBadge(n); });

let lastWaMode = '';
makePoll(async () => {
  const s = await get('/api/status');
  setStatus(s);
  const mode = s.whatsapp && s.whatsapp.mode;
  if (mode === 'connected' && lastWaMode !== 'connected') loadGroups(true);
  lastWaMode = mode;
}, 3000);

makePoll(async () => {
  const list = await get('/api/chats/ignored');
  setIgnoredCount(Array.isArray(list) ? list.length : 0);
}, 10000);

makePoll(async () => { await loadGroups(); }, 60000);

// ── boot ─────────────────────────────────────────────────────────────────

if (!location.hash || location.hash === '#') history.replaceState(null, '', '#/overview');
if (!blocked) {
  buildShell();
  renderRoute();
}

// Surface unexpected script errors instead of failing silently.
window.addEventListener('unhandledrejection', (e) => {
  const err = e.reason;
  if (err && (err.status === 401 || err.status === 0 || err.name === 'AbortError')) return;
  console.error(err);
  if (err && err.message) toast(err.message, 'error');
});
