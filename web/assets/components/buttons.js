// Buttons: btn() with an optional icon and a busy state while its handler
// runs, and the copy-to-clipboard button.

import { h } from '../lib/dom.js';
import { icon } from './icons.js';
import { toast, toastError } from './toast.js';
import { errorName } from '../lib/errors.js';

/** @import { Child } from '../lib/dom.js' */

/**
 * btn()'s options. kind is a class such as primary, ghost or danger; icon
 * an icon's name ('right' goes after the label); onClick gets the click
 * and the button, which is busy (disabled, with a spinner) until what
 * onClick returns has settled.
 * @typedef {object} BtnOptions
 * @property {string} [kind]
 * @property {string} [icon]
 * @property {(e: MouseEvent, b: HTMLButtonElement) => unknown} [onClick]
 * @property {boolean} [small]
 * @property {string} [title]
 * @property {'button' | 'submit' | 'reset'} [type]
 * @property {boolean} [disabled]
 * @property {string} [href] makes it a link, which takes no onClick
 * @property {string} [cls] more classes, dot-separated
 */

/**
 * btn('Save', {kind: 'primary', icon: 'check', onClick})
 * @overload
 * @param {Child} label
 * @param {BtnOptions & {href: string}} options
 * @returns {HTMLAnchorElement}
 */
/**
 * @overload
 * @param {Child} label
 * @param {BtnOptions & {href?: undefined}} [options]
 * @returns {HTMLButtonElement}
 */
/**
 * @param {Child} label
 * @param {BtnOptions} [options]
 * @returns {HTMLAnchorElement | HTMLButtonElement}
 */
export function btn(label, { kind = '', icon: icn, onClick, small, title, type = 'button', disabled, href, cls = '' } = {}) {
  const classes = ['btn', kind, small ? 'small' : '', cls].filter(Boolean).join('.');
  const after = icn === 'right';
  const children = [icn && !after ? icon(icn) : null, label ? h('span.btn-label', label) : null, icn && after ? icon(icn) : null];
  if (href) return h(`a.${classes}`, { href, title }, children);
  const b = h(`button.${classes}`, { type, title, disabled, 'aria-label': !label ? title : null }, children);
  if (onClick) b.addEventListener('click', (e) => { e.preventDefault(); runBusy(b, () => onClick(e, b)); });
  return b;
}

/**
 * Disables a button and shows a spinner while fn runs; errors become toasts.
 * Resolves with what fn resolved with, or undefined when the button was
 * busy already or fn failed.
 * @template T
 * @param {HTMLButtonElement | null | undefined} button
 * @param {() => T | Promise<T>} fn
 * @returns {Promise<T | undefined>}
 */
export async function runBusy(button, fn) {
  if (button && (button.disabled || button.classList.contains('busy'))) return undefined;
  if (button) { button.disabled = true; button.classList.add('busy'); }
  try {
    return await fn();
  } catch (e) {
    if (errorName(e) !== 'AbortError') toastError(e);
    return undefined;
  } finally {
    if (button) { button.disabled = false; button.classList.remove('busy'); }
  }
}

/**
 * An icon button that copies text to the clipboard.
 * @param {string} text
 * @param {string} [label]
 */
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
