// Toasts: short messages in the corner. The same message is shown once at a
// time, so a failing 3 s poll does not stack up copies.

import { h } from '../lib/dom.js';
import { icon } from './icons.js';

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
