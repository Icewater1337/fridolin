// Buttons: btn() with an optional icon and a busy state while its handler
// runs, and the copy-to-clipboard button.

import { h } from '../lib/dom.js';
import { icon } from './icons.js';
import { toast, toastError } from './toast.js';

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
