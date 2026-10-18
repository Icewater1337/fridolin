// Toasts: short messages in the corner. The same message is shown once at a
// time, so a failing 3 s poll does not stack up copies.

import { h } from '../lib/dom.js';
import { icon } from './icons.js';

// The corner and its two live regions: errors go to an alert, which a
// screen reader reads out at once, the rest to a polite status. So the
// errors stack above the other toasts on purpose, not in the order they
// came: an error is the one to see first. Made by the first toast, and
// again once it is no longer in the document (each page test installs a
// fresh one).
let toastBox = null;
let regions = null;
const recentToasts = new Map();

function regionFor(kind) {
  if (!toastBox || !toastBox.isConnected) {
    regions = {
      alert: h('div.toast-region', { role: 'alert' }),
      status: h('div.toast-region', { role: 'status', 'aria-live': 'polite' }),
    };
    toastBox = h('div.toasts', regions.alert, regions.status);
    document.body.appendChild(toastBox);
  }
  return kind === 'error' ? regions.alert : regions.status;
}

export function toast(message, kind = 'error', { timeout } = {}) {
  const region = regionFor(kind);
  const key = `${kind}:${message}`;
  const prev = recentToasts.get(key);
  if (prev && prev.isConnected) return prev;
  const icn = { ok: 'check', warn: 'alert', error: 'alert', info: 'info' }[kind] || 'info';
  const close = h('button.icon-btn', { type: 'button', 'aria-label': 'Dismiss' }, icon('x'));
  const el = h(`div.toast.${kind}`, icon(icn, 'toast-icon'), h('div.toast-msg', message), close);
  let left = timeout || (kind === 'error' ? 9000 : 5000);
  let since = 0;
  let timer = null;
  let gone = false;
  // Runs from the close button and from the timer, whichever comes first.
  // A newer toast may own the key by then; only this toast's entry goes.
  const remove = () => {
    if (gone) return;
    gone = true;
    clearTimeout(timer);
    timer = null;
    el.classList.add('leaving');
    setTimeout(() => el.remove(), 200);
    if (recentToasts.get(key) === el) recentToasts.delete(key);
  };
  const run = () => { since = Date.now(); timer = setTimeout(remove, left); };
  // The pointer or the focus on a toast holds it, so it does not go while
  // it is being read or before its button is reached. It then gets what
  // was left of its time, and at least a moment to move away.
  const holds = new Set();
  const hold = (why) => {
    holds.add(why);
    if (!timer) return;
    clearTimeout(timer);
    timer = null;
    left = Math.max(left - (Date.now() - since), 1500);
  };
  const release = (why) => {
    holds.delete(why);
    if (!holds.size && !gone && !timer) run();
  };
  el.addEventListener('mouseenter', () => hold('pointer'));
  el.addEventListener('mouseleave', () => release('pointer'));
  el.addEventListener('focusin', () => hold('focus'));
  el.addEventListener('focusout', (e) => { if (!el.contains(e.relatedTarget)) release('focus'); });
  close.addEventListener('click', remove);
  region.appendChild(el);
  recentToasts.set(key, el);
  run();
  return el;
}

export function toastError(e) {
  const msg = (e && e.message) ? e.message : String(e);
  if (e && e.status === 401) return null; // the connect screen takes over
  if (e && e.name === 'AbortError') return null; // the page that asked is gone
  return toast(msg, 'error');
}
