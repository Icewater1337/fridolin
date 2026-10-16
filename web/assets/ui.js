// Small DOM toolkit: an element builder that never parses HTML, icons,
// toasts, dialogs, time formatting and form controls.

import { store } from './state.js';

// ── element builder ──────────────────────────────────────────────────────

const PROPS = new Set(['value', 'checked', 'disabled', 'selected', 'hidden', 'indeterminate', 'readOnly', 'multiple', 'open']);

/**
 * h('div.card.wide', {onclick, title}, child, 'text', [more]) builds an
 * element. Strings become text nodes, so untrusted text is always safe.
 */
export function h(tag, attrs, ...children) {
  const parts = tag.split('.');
  let name = parts.shift() || 'div';
  let id = '';
  if (name.includes('#')) [name, id] = name.split('#');
  const el = document.createElement(name || 'div');
  if (id) el.id = id;
  if (parts.length) el.className = parts.join(' ');
  if (attrs !== null && attrs !== undefined && (typeof attrs !== 'object' || attrs instanceof Node || Array.isArray(attrs))) {
    children.unshift(attrs);
    attrs = null;
  }
  if (attrs) setAttrs(el, attrs);
  append(el, children);
  return el;
}

export function setAttrs(el, attrs) {
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') {
      el.className = [el.className, v].filter(Boolean).join(' ');
    } else if (k === 'text') {
      el.textContent = String(v);
    } else if (k === 'style' && typeof v === 'object') {
      Object.assign(el.style, v);
    } else if (k === 'dataset') {
      Object.assign(el.dataset, v);
    } else if (k.startsWith('on') && typeof v === 'function') {
      el.addEventListener(k.slice(2).toLowerCase(), v);
    } else if (PROPS.has(k)) {
      el[k] = v;
    } else {
      el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  return el;
}

export function append(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false || c === true) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
  return el;
}

/** Replaces all children of el. */
export function mount(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

// ── icons ────────────────────────────────────────────────────────────────

const ICONS = {
  overview: ['M3 10.5 12 3l9 7.5', 'M5 9v11h5v-6h4v6h5V9'],
  activity: ['M22 12h-4l-3 9L9 3l-3 9H2'],
  brain: ['M7 4h10a3 3 0 0 1 3 3v10a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3V7a3 3 0 0 1 3-3z', 'M9 9h6v6H9z', 'M9 1v3M15 1v3M9 20v3M15 20v3M1 9h3M1 15h3M20 9h3M20 15h3'],
  chats: ['M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z'],
  settings: ['M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6'],
  backup: ['M20 16.6A5 5 0 0 0 18 7h-1.3A8 8 0 1 0 4 15.3', 'M12 12v9', 'M8 16l4-4 4 4'],
  logs: ['M4 17l6-6-6-6', 'M12 19h8'],
  dev: ['M9 3h6', 'M10 3v6.5L4.4 18.6A1.6 1.6 0 0 0 5.8 21h12.4a1.6 1.6 0 0 0 1.4-2.4L14 9.5V3', 'M7 15h10'],
  in: ['M17 7 7 17', 'M17 17H7V7'],
  out: ['M7 17 17 7', 'M7 7h10v10'],
  check: ['M20 6 9 17l-5-5'],
  x: ['M18 6 6 18M6 6l12 12'],
  alert: ['M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z', 'M12 9v4', 'M12 17h.01'],
  info: ['M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20z', 'M12 16v-4', 'M12 8h.01'],
  folder: ['M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z'],
  folderPlus: ['M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z', 'M12 11v6M9 14h6'],
  git: ['M6 3v12', 'M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M18 9a9 9 0 0 1-9 9'],
  up: ['M12 19V5', 'M5 12l7-7 7 7'],
  right: ['M9 18l6-6-6-6'],
  left: ['M15 18l-6-6 6-6'],
  down: ['M6 9l6 6 6-6'],
  plus: ['M12 5v14M5 12h14'],
  refresh: ['M23 4v6h-6', 'M1 20v-6h6', 'M3.5 9a9 9 0 0 1 14.9-3.4L23 10M1 14l4.6 4.4A9 9 0 0 0 20.5 15'],
  stop: ['M6 6h12v12H6z'],
  sun: ['M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10z', 'M12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4'],
  moon: ['M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z'],
  auto: ['M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20z', { d: 'M12 2a10 10 0 0 0 0 20z', fill: true }],
  menu: ['M3 6h18M3 12h18M3 18h18'],
  copy: ['M20 9h-9a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-9a2 2 0 0 0-2-2z', 'M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1'],
  trash: ['M3 6h18', 'M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2', 'M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6'],
  user: ['M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2', 'M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z'],
  users: ['M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2', 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z', 'M23 21v-2a4 4 0 0 0-3-3.9', 'M16 3.1a4 4 0 0 1 0 7.8'],
  external: ['M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6', 'M15 3h6v6', 'M10 14 21 3'],
  send: ['M22 2 11 13', 'M22 2l-7 20-4-9-9-4z'],
  clock: ['M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20z', 'M12 6v6l4 2'],
  shield: ['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z'],
  phone: ['M22 16.9v3a2 2 0 0 1-2.2 2 19.8 19.8 0 0 1-8.6-3.1 19.5 19.5 0 0 1-6-6A19.8 19.8 0 0 1 2.1 4.2 2 2 0 0 1 4.1 2h3a2 2 0 0 1 2 1.7c.1.9.4 1.8.7 2.7a2 2 0 0 1-.5 2.1L8 9.8a16 16 0 0 0 6 6l1.3-1.3a2 2 0 0 1 2.1-.4c.9.3 1.8.6 2.7.7a2 2 0 0 1 1.7 2z'],
  terminal: ['M4 17l6-6-6-6', 'M12 19h8'],
  key: ['M21 2l-2 2', 'M11.4 11.6a5.5 5.5 0 1 1-7.8 7.8 5.5 5.5 0 0 1 7.8-7.8z', 'M11.4 11.6 15.5 7.5', 'M15.5 7.5l3 3L22 7l-3-3'],
  download: ['M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4', 'M7 10l5 5 5-5', 'M12 15V3'],
  edit: ['M12 20h9', 'M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z'],
  lock: ['M5 11h14a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2z', 'M7 11V7a5 5 0 0 1 10 0v4'],
  pause: ['M6 4h4v16H6zM14 4h4v16h-4z'],
  play: ['M5 3l14 9-14 9z'],
  sparkle: ['M12 3l2.1 5.9L20 11l-5.9 2.1L12 19l-2.1-5.9L4 11l5.9-2.1z'],
  inbox: ['M22 12h-6l-2 3h-4l-2-3H2', 'M5.5 5.1 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.9A2 2 0 0 0 16.8 4H7.2a2 2 0 0 0-1.7 1.1z'],
};

const SVGNS = 'http://www.w3.org/2000/svg';

export function icon(name, cls = '') {
  const svg = document.createElementNS(SVGNS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '2');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  svg.setAttribute('class', `icon ${cls}`.trim());
  for (const p of ICONS[name] || ICONS.info) {
    const path = document.createElementNS(SVGNS, 'path');
    if (typeof p === 'string') {
      path.setAttribute('d', p);
    } else {
      path.setAttribute('d', p.d);
      if (p.fill) path.setAttribute('fill', 'currentColor');
    }
    svg.appendChild(path);
  }
  return svg;
}

// ── time and size formatting ─────────────────────────────────────────────

export function parseTs(ts) {
  if (!ts) return null;
  const d = ts instanceof Date ? ts : new Date(ts);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return null;
  return d;
}

export function relTime(ts) {
  const d = parseTs(ts);
  if (!d) return 'never';
  const diff = (Date.now() - d.getTime()) / 1000;
  const abs = Math.abs(diff);
  const future = diff < -5;
  let s;
  if (abs < 45) return future ? 'in a few seconds' : 'just now';
  if (abs < 90) s = '1 min';
  else if (abs < 3600) s = `${Math.round(abs / 60)} min`;
  else if (abs < 5400) s = '1 hour';
  else if (abs < 86400) s = `${Math.round(abs / 3600)} hours`;
  else if (abs < 172800) return future ? 'tomorrow' : 'yesterday';
  else if (abs < 30 * 86400) s = `${Math.round(abs / 86400)} days`;
  else return d.toLocaleDateString(undefined, { dateStyle: 'medium' });
  return future ? `in ${s}` : `${s} ago`;
}

export function absTime(ts) {
  const d = parseTs(ts);
  if (!d) return '';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' });
}

export function clockTime(ts) {
  const d = parseTs(ts);
  if (!d) return '';
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

/** <time> showing "3 min ago" with the full date in a tooltip; kept fresh by a ticker. */
export function timeAgo(ts, prefix = '') {
  const d = parseTs(ts);
  if (!d) return h('span.muted', 'never');
  return h('time.rel', { datetime: d.toISOString(), title: absTime(d), dataset: { ts: d.toISOString(), prefix } }, prefix + relTime(d));
}

setInterval(() => {
  document.querySelectorAll('time.rel[data-ts]').forEach((el) => {
    el.textContent = (el.dataset.prefix || '') + relTime(el.dataset.ts);
  });
}, 15000);

export function fmtDuration(seconds) {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) return '';
  const s = Math.round(seconds);
  if (s < 1) return `${Math.round(seconds * 1000)} ms`;
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60 ? `${s % 60} s` : ''}`.trim();
  if (s < 86400) return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
  return `${Math.floor(s / 86400)} d ${Math.floor((s % 86400) / 3600)} h`;
}

export function fmtBytes(n) {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function plural(n, one, many) {
  return `${n} ${n === 1 ? one : (many || `${one}s`)}`;
}

// ── WhatsApp ids ─────────────────────────────────────────────────────────

export function isGroupJid(jid) { return typeof jid === 'string' && jid.endsWith('@g.us'); }

/** Short, readable form of a chat JID. */
export function shortJid(jid) {
  if (!jid) return '';
  const [user, server] = String(jid).split('@');
  if (server === 's.whatsapp.net') return `+${user}`;
  if (server === 'g.us') return user.length > 10 ? `group …${user.slice(-6)}` : `group ${user}`;
  if (server === 'lid') return `id …${user.slice(-6)}`;
  if (!server && /^\d+$/.test(user)) return `+${user}`;
  return jid.length > 28 ? `${jid.slice(0, 12)}…${jid.slice(-10)}` : jid;
}

export function chatName(jid) {
  return store.groupNames.get(jid) || '';
}

/** A chat reference: group name when known, the JID in monospace otherwise. */
export function chatEl(jid, { full = false } = {}) {
  if (!jid) return h('span.muted', '—');
  const name = chatName(jid);
  const icn = icon(isGroupJid(jid) ? 'users' : 'user', 'chat-icon');
  if (name) {
    return h('span.chat', { title: jid }, icn, h('span.chat-name', name));
  }
  return h('span.chat', { title: jid }, icn, h('span.mono', full ? jid : shortJid(jid)));
}

export function phone(num) {
  return num ? `+${String(num).replace(/^\+/, '')}` : '';
}

export function waKind(mode) {
  return { connected: 'ok', connecting: 'warn', pairing: 'warn', 'qr-expired': 'warn', disconnected: 'warn', 'logged-out': 'bad', replaced: 'bad', disabled: 'neutral' }[mode] || 'neutral';
}

export function waLabel(mode) {
  return {
    connected: 'WhatsApp connected',
    connecting: 'Connecting to WhatsApp…',
    pairing: 'Waiting for pairing',
    'qr-expired': 'Pairing code expired',
    disconnected: 'WhatsApp disconnected',
    'logged-out': 'Logged out of WhatsApp',
    replaced: 'Session used elsewhere',
    disabled: 'WhatsApp off',
  }[mode] || (mode ? `WhatsApp: ${mode}` : 'WhatsApp');
}

// ── status pills and small pieces ────────────────────────────────────────

export function pill(text, kind = 'neutral', { title, pulse } = {}) {
  return h(`span.pill.${kind}`, { title }, h(`span.dot${pulse ? '.pulse' : ''}`), text);
}

export function badge(text, kind = 'neutral', title) {
  return h(`span.badge.${kind}`, { title }, text);
}

export function mono(text, { copy = false, title } = {}) {
  const el = h('code.mono', { title: title || text }, text);
  if (!copy) return el;
  return h('span.copyable', el, copyButton(text));
}

export function copyButton(text, label = 'Copy') {
  const b = h('button.icon-btn.copy', { type: 'button', title: label, 'aria-label': label }, icon('copy'));
  b.addEventListener('click', async (e) => {
    e.preventDefault();
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(text);
      toast('Copied to the clipboard', 'ok', { timeout: 1800 });
    } catch {
      toast('Could not copy — select the text and copy it yourself', 'warn');
    }
  });
  return b;
}

/** A spinner with a short text, on one line. */
export function loading(...text) {
  return h('div.loading-row', spinner(), h('span', text));
}

export function spinner(cls = '') {
  return h(`span.spinner${cls ? `.${cls}` : ''}`, { 'aria-hidden': 'true' });
}

export function empty(title, text, ...actions) {
  return h('div.empty', h('div.empty-title', title), text && h('p', text), actions.length ? h('div.actions', actions) : null);
}

export function callout(kind, title, ...body) {
  const icn = { warn: 'alert', bad: 'alert', ok: 'check', info: 'info' }[kind] || 'info';
  return h(`div.callout.${kind}`, { role: kind === 'bad' ? 'alert' : null }, icon(icn, 'callout-icon'), h('div.callout-body', title && h('strong', title), body.length ? h('div.callout-text', body) : null));
}

export function kv(rows) {
  const dl = h('dl.kv');
  for (const [k, v] of rows) {
    if (v === null || v === undefined || v === false) continue;
    dl.append(h('dt', k), h('dd', v));
  }
  return dl;
}

export function section(title, ...children) {
  return h('section.section', title ? h('h2.section-title', title) : null, children);
}

export function pageHeader(title, subtitle, ...actions) {
  return h('header.page-header',
    h('div.page-heading', h('h1', title), subtitle ? h('p.page-sub', subtitle) : null),
    actions.length ? h('div.page-actions', actions) : null);
}

// ── buttons ──────────────────────────────────────────────────────────────

/** btn('Save', {kind: 'primary', icon: 'check', onClick}) */
export function btn(label, { kind = '', icon: icn, onClick, small, title, type = 'button', disabled, href, cls = '' } = {}) {
  const classes = ['btn', kind, small ? 'small' : '', cls].filter(Boolean).join('.');
  const after = icn === 'right';
  const children = [icn && !after ? icon(icn) : null, label ? h('span.btn-label', label) : null, icn && after ? icon(icn) : null];
  if (href) return h(`a.${classes}`, { href, title }, children);
  const b = h(`button.${classes}`, { type, title, disabled, 'aria-label': !label ? title : null }, children);
  if (onClick) b.addEventListener('click', (e) => { e.preventDefault(); runBusy(b, () => onClick(e, b)); });
  return b;
}

/** Disables a button and shows a spinner while fn runs; errors become toasts. */
export async function runBusy(button, fn) {
  if (button && (button.disabled || button.classList.contains('busy'))) return undefined;
  if (button) { button.disabled = true; button.classList.add('busy'); }
  try {
    return await fn();
  } catch (e) {
    if (e && e.name !== 'AbortError') toastError(e);
    return undefined;
  } finally {
    if (button) { button.disabled = false; button.classList.remove('busy'); }
  }
}

// ── toasts ───────────────────────────────────────────────────────────────

let toastBox = null;
const recentToasts = new Map();

export function toast(message, kind = 'error', { timeout } = {}) {
  if (!toastBox) {
    toastBox = h('div.toasts', { 'aria-live': 'polite', role: 'status' });
    document.body.appendChild(toastBox);
  }
  const key = `${kind}:${message}`;
  const prev = recentToasts.get(key);
  if (prev && prev.isConnected) return prev;
  const icn = { ok: 'check', warn: 'alert', error: 'alert', info: 'info' }[kind] || 'info';
  const close = h('button.icon-btn', { type: 'button', 'aria-label': 'Dismiss' }, icon('x'));
  const el = h(`div.toast.${kind}`, icon(icn, 'toast-icon'), h('div.toast-msg', message), close);
  const remove = () => { el.classList.add('leaving'); setTimeout(() => el.remove(), 200); recentToasts.delete(key); };
  close.addEventListener('click', remove);
  toastBox.appendChild(el);
  recentToasts.set(key, el);
  setTimeout(remove, timeout || (kind === 'error' ? 9000 : 5000));
  return el;
}

export function toastError(e) {
  const msg = (e && e.message) ? e.message : String(e);
  if (e && e.status === 401) return null; // the connect screen takes over
  return toast(msg, 'error');
}

// ── dialogs ──────────────────────────────────────────────────────────────

/**
 * openDialog({title, body, actions: [{label, kind, value, onClick}]})
 * resolves with the clicked action's value (or its onClick result), or null
 * when dismissed. onClick may return false to keep the dialog open.
 */
const openDialogs = new Set();

/** Closes every open dialog (used when the page changes). */
export function closeAllDialogs() {
  [...openDialogs].forEach((fn) => fn(null));
}

export function openDialog({ title, body, actions = [{ label: 'Close', value: null }], wide = false, dismissible = true }) {
  let finishFn = null;
  const promise = new Promise((resolve) => {
    const dlg = h(`dialog.dialog${wide ? '.wide' : ''}`, { 'aria-label': title });
    let done = false;
    const finish = (v) => {
      if (done) return;
      done = true;
      openDialogs.delete(finish);
      dlg.close();
      dlg.remove();
      resolve(v);
    };
    finishFn = finish;
    openDialogs.add(finish);
    const footer = h('div.dialog-actions');
    for (const a of actions) {
      const b = btn(a.label, { kind: a.kind || '', icon: a.icon });
      b.addEventListener('click', async (e) => {
        e.preventDefault();
        if (!a.onClick) { finish(a.value === undefined ? a.label : a.value); return; }
        const r = await runBusy(b, () => a.onClick(dlg));
        if (r !== false && r !== undefined) finish(r);
      });
      footer.appendChild(b);
    }
    const closeBtn = dismissible ? h('button.icon-btn.dialog-close', { type: 'button', 'aria-label': 'Close' }, icon('x')) : null;
    if (closeBtn) closeBtn.addEventListener('click', () => finish(null));
    append(dlg, [
      h('div.dialog-head', h('h2', title), closeBtn),
      h('div.dialog-body', body),
      footer,
    ]);
    dlg.addEventListener('cancel', (e) => { e.preventDefault(); if (dismissible) finish(null); });
    dlg.addEventListener('click', (e) => { if (e.target === dlg && dismissible) finish(null); });
    document.body.appendChild(dlg);
    dlg.showModal();
  });
  /** Closes the dialog from outside; the promise resolves with v. */
  promise.close = (v = null) => { if (finishFn) finishFn(v); };
  return promise;
}

export async function confirmDialog({ title, message, confirm = 'Confirm', danger = false, cancel = 'Cancel' }) {
  const v = await openDialog({
    title,
    body: typeof message === 'string' ? h('p', message) : message,
    actions: [
      { label: cancel, value: false },
      { label: confirm, kind: danger ? 'danger' : 'primary', value: true },
    ],
  });
  return v === true;
}

// ── form controls ────────────────────────────────────────────────────────

let uid = 0;
export function nextId(prefix = 'f') { uid += 1; return `${prefix}-${uid}`; }

/** A labelled form row. */
export function field({ label, help, control, id, locked, tag, wide, warn }) {
  const ctrl = control;
  const target = id || (ctrl && ctrl.id) || (ctrl && ctrl.querySelector && ctrl.querySelector('input,select,textarea')?.id);
  return h(`div.field${wide ? '.wide' : ''}`,
    label ? h('label.field-label', { for: target || null }, label, tag ? h('span.field-tag', tag) : null) : null,
    ctrl,
    help ? h('div.help', help) : null,
    warn ? h('div.help.warn-text', warn) : null,
    locked ? h('div.help.locked', icon('lock'), `Set by the environment variable ${locked} — change it there.`) : null);
}

/** A switch (checkbox with role=switch). Returns the wrapper; .input is the checkbox. */
export function toggle({ checked = false, label, onChange, disabled, id }) {
  const input = h('input', { type: 'checkbox', role: 'switch', checked: !!checked, disabled, id: id || nextId('sw') });
  if (onChange) input.addEventListener('change', () => onChange(input.checked, input));
  const el = h('label.switch', { for: input.id }, input, h('span.track', h('span.thumb')), label ? h('span.switch-label', label) : null);
  el.input = input;
  return el;
}

export function select(options, value, attrs = {}) {
  const s = h('select', { id: attrs.id || nextId('sel'), ...attrs });
  for (const o of options) {
    const [v, label] = Array.isArray(o) ? o : [o, o];
    s.appendChild(h('option', { value: v, selected: v === value }, label));
  }
  if (value !== undefined) s.value = value;
  return s;
}

export function textInput(attrs = {}) {
  return h('input', { type: 'text', id: attrs.id || nextId('in'), autocomplete: 'off', spellcheck: 'false', ...attrs });
}

/** Editable list of strings, one row per item. el.getValues() returns the trimmed list. */
export function listEditor({ values = [], placeholder = '', mono = true, disabled = false, addLabel = 'Add', onChange, validate, empty: emptyText }) {
  const rows = h('div.list-rows');
  const el = h('div.list-editor', rows);
  const changed = () => { if (onChange) onChange(); };
  const addRow = (v = '', focus = false) => {
    const input = textInput({ value: v, placeholder, disabled, class: mono ? 'mono' : '' });
    input.addEventListener('input', () => { check(input); changed(); });
    const del = h('button.icon-btn', { type: 'button', title: 'Remove', 'aria-label': 'Remove', disabled }, icon('x'));
    const row = h('div.list-row', input, del);
    del.addEventListener('click', () => { row.remove(); renderEmpty(); changed(); });
    rows.appendChild(row);
    check(input);
    renderEmpty();
    if (focus) input.focus();
  };
  const check = (input) => {
    const msg = validate && input.value.trim() ? validate(input.value.trim()) : '';
    input.classList.toggle('invalid', !!msg);
    input.title = msg || '';
  };
  const emptyEl = h('div.list-empty.muted', emptyText || 'None');
  const renderEmpty = () => { emptyEl.hidden = rows.children.length > 0; };
  el.prepend(emptyEl);
  (values || []).forEach((v) => addRow(v));
  renderEmpty();
  if (!disabled) {
    el.appendChild(h('div.list-add', btn(addLabel, { icon: 'plus', small: true, kind: 'ghost', onClick: () => { addRow('', true); changed(); } })));
  }
  el.getValues = () => [...rows.querySelectorAll('input')].map((i) => i.value.trim()).filter(Boolean);
  el.isValid = () => ![...rows.querySelectorAll('input')].some((i) => i.classList.contains('invalid'));
  return el;
}

/** Textarea holding one item per line. */
export function linesArea(values, attrs = {}) {
  const ta = h('textarea.mono', { id: attrs.id || nextId('ta'), rows: attrs.rows || 4, spellcheck: 'false', ...attrs });
  ta.value = (values || []).join('\n');
  return ta;
}

export function linesOf(textarea) {
  return textarea.value.split('\n').map((s) => s.trim()).filter(Boolean);
}

// ── misc ─────────────────────────────────────────────────────────────────

export function slugify(s) {
  let out = '';
  let dash = false;
  for (const ch of String(s).toLowerCase()) {
    if (/[a-z0-9]/.test(ch)) { out += ch; dash = false; } else if (/[-_ .]/.test(ch)) {
      if (!dash && out) { out += '-'; dash = true; }
    }
  }
  out = out.replace(/^-+|-+$/g, '');
  if (out.length > 40) out = out.slice(0, 40).replace(/-+$/, '');
  return out || 'brain';
}

export const BRAIN_ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/;
export const EFFORTS = [['', 'Inherit'], ['low', 'Low'], ['medium', 'Medium'], ['high', 'High'], ['xhigh', 'Extra high'], ['max', 'Max']];
export const DEFAULT_BACKUP_EXCLUDES = ['node_modules/', '*.log', '.DS_Store', '.venv/', '__pycache__/'];

export function routingSummary(routing) {
  if (!routing) return 'No chats';
  const parts = [];
  if (routing.handles_dms) parts.push('Direct messages');
  if (routing.default) parts.push('Default for other chats');
  const n = (routing.groups || []).length;
  if (n) parts.push(plural(n, 'group'));
  return parts.length ? parts.join(' · ') : 'No chats routed yet';
}

export function outcomePill(last) {
  if (!last) return pill('No runs yet', 'neutral');
  const map = { ok: ['Replied', 'ok'], error: ['Failed', 'bad'], interrupted: ['Interrupted', 'warn'], question: ['Asked a question', 'info'] };
  const [label, kind] = map[last.outcome] || [last.outcome || 'Unknown', 'neutral'];
  return pill(label, kind, { title: last.error || '' });
}

export function brainStatusPill(status) {
  const map = {
    ok: ['Active', 'ok'],
    drift: ['Changed on disk', 'warn'],
    missing: ['Folder missing', 'bad'],
    invalid: ['Invalid', 'bad'],
    unapproved: ['Not approved', 'bad'],
  };
  const [label, kind] = map[status] || [status, 'neutral'];
  return pill(label, kind);
}

/** Collapses long text behind a "more" toggle; keeps newlines. */
export function longText(text, { limit = 320, lines = 6, expanded, onToggle } = {}) {
  const t = String(text || '');
  const lineCount = t.split('\n').length;
  const long = t.length > limit || lineCount > lines;
  const body = h('div.msg-text', t);
  if (!long) return body;
  const wrap = h('div.msg-wrap', body);
  const more = h('button.link-btn', { type: 'button' });
  const set = (open) => {
    wrap.classList.toggle('collapsed', !open);
    more.textContent = open ? 'less' : 'more';
    more.setAttribute('aria-expanded', String(open));
  };
  more.addEventListener('click', () => {
    const open = wrap.classList.contains('collapsed');
    set(open);
    if (onToggle) onToggle(open);
  });
  set(!!expanded);
  wrap.appendChild(more);
  return wrap;
}

/** Renders only when the data actually changed (cheap guard for 3 s polling). */
export function changed(holder, key, data) {
  const s = JSON.stringify(data);
  if (holder[key] === s) return false;
  holder[key] = s;
  return true;
}

export function prettyJSON(v) {
  return JSON.stringify(v, null, 2);
}
