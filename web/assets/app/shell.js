// The app shell: sidebar, top banners and the area pages render into. It
// is built once and paints itself from the store, so going through the
// connect screen and back adds no second set of listeners.

import { h, mount } from '../lib/dom.js';
import { plural } from '../lib/format.js';
import { waKind, waLabel } from '../lib/whatsapp.js';
import { icon } from '../components/icons.js';
import { btn } from '../components/buttons.js';
import { callout } from '../components/common.js';
import { ROUTES } from './routes.js';
import { BRAND, routeHref } from './router.js';
import { store } from './store.js';

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

/**
 * createShell({onRestart, onReload}) builds the shell. It returns {nodes,
 * page, setActive(navId), closeNav(), focusPage(heading),
 * setOffline(bool)}: mount nodes into #app, render pages into page.
 * focusPage returns the element it focused. onRestart runs from the
 * restart banner's button, onReload (location.reload) from the update
 * banner's.
 */
export function createShell({ onRestart, onReload = () => location.reload() }) {
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
  const updateBanner = h('div.banner.info', { hidden: true },
    icon('info'), h('span', 'Fridolin was updated. Reload the page to use the new dashboard.'),
    btn('Reload', { kind: 'primary', small: true, onClick: () => onReload() }));
  banners.append(offline, restartBanner, updateBanner);
  const page = h('main.main', { id: 'main', tabindex: '-1' });

  // The keyboard's way into a page: the heading given, else the page's
  // own, or the page area until it has one. A screen reader then reads
  // the new page from there, not on from the link that was followed.
  const focusPage = (target) => {
    const heading = target || page.querySelector('h1');
    if (heading) heading.setAttribute('tabindex', '-1');
    const el = heading || page;
    el.focus({ preventScroll: true });
    return el;
  };
  // Past the sidebar's links, for keyboard users; shown once focused.
  const skip = h('a.skip-link', { href: '#main' }, 'Skip to content');
  skip.addEventListener('click', (e) => { e.preventDefault(); focusPage(); });

  menuBtn.addEventListener('click', () => {
    const open = !sidebar.classList.contains('nav-open');
    sidebar.classList.toggle('nav-open', open);
    menuBtn.setAttribute('aria-expanded', String(open));
  });

  const setBadge = (id, n, title) => {
    const b = links.get(id).badgeEl;
    b.hidden = !n;
    b.textContent = String(n);
    b.title = n ? title : '';
  };

  store.subscribe((s) => s.status, (s) => {
    if (!s) return;
    links.get('dev').hidden = !s.dev_mode;
    version.textContent = s.version ? (/^\d/.test(s.version) ? `v${s.version}` : String(s.version)) : '';
    const wa = s.whatsapp || {};
    waDot.className = `dot ${waKind(wa.mode)}`;
    waText.textContent = s.dev_mode ? 'Dev mode (no WhatsApp)' : waLabel(wa.mode);
    const issues = (s.issues || []).length;
    setBadge('brains', issues, plural(issues, 'brain issue'));
  });

  store.subscribe((s) => s.ignoredCount, (n) => setBadge('chats', n, `${plural(n, 'chat')} waiting for access`));

  // The bridge reports what waits for a restart, so every tab shows the
  // banner, and it goes once the restart (or undoing the change) happened.
  store.subscribe((s) => ((s.status && s.status.restart_required) || []).join(', '), (fields) => {
    restartBanner.hidden = !fields;
    if (!fields) { mount(restartBanner); return; }
    mount(restartBanner, icon('refresh'),
      h('span', 'Restart the bridge to apply: ', h('code.mono', fields)),
      btn('Restart now', { kind: 'primary', small: true, onClick: () => onRestart() }));
  });

  // Pages load their modules on first visit. Once the bridge runs another
  // version, a page opened next would come from the new build but use the
  // old build's components, which this tab has loaded already.
  let startVersion = '';
  store.subscribe((s) => (s.status && s.status.version) || '', (v) => {
    if (!v) return;
    if (!startVersion) startVersion = v;
    updateBanner.hidden = v === startVersion;
  });

  return {
    nodes: [skip, sidebar, h('div.content', banners, page)],
    page,
    setActive(id) {
      links.forEach((a, key) => {
        if (key === id) a.setAttribute('aria-current', 'page');
        else a.removeAttribute('aria-current');
      });
    },
    closeNav() { sidebar.classList.remove('nav-open'); menuBtn.setAttribute('aria-expanded', 'false'); },
    focusPage,
    setOffline(off) { offline.hidden = !off; },
  };
}

/**
 * What a page shows when it failed to load or to render. Mixed versions
 * after an update are one cause, so it offers a reload (location.reload).
 */
export function pageFailed(e, onReload = () => location.reload()) {
  return callout('bad', 'This page failed to load',
    h('div', (e && e.message) || String(e)),
    h('p.muted.small', 'If Fridolin was just updated, reloading the page loads the new version of all of it.'),
    h('div.actions', btn('Reload page', { kind: 'primary', icon: 'refresh', small: true, onClick: () => onReload() })));
}
