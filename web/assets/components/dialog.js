// Modal dialogs on <dialog>: openDialog for custom content, confirmDialog
// for yes/no questions.

import { h, append } from '../lib/dom.js';
import { icon } from './icons.js';
import { btn, runBusy } from './buttons.js';

/** @import { Child } from '../lib/dom.js' */

/**
 * A dialog's button. Clicking it closes the dialog with value (label when
 * value is undefined); one with onClick runs that instead, as openDialog
 * says.
 * @typedef {object} DialogAction
 * @property {string} label
 * @property {string} [kind]
 * @property {string} [icon]
 * @property {unknown} [value]
 * @property {(dialog: HTMLDialogElement) => unknown} [onClick]
 */

/**
 * What openDialog returns: a promise of what the dialog closed with, which
 * close(v) closes it with from outside and setActions(list) gives new buttons.
 * @typedef {Promise<unknown> & {close: (v?: unknown) => void, setActions: (list: DialogAction[]) => void}} DialogHandle
 */

/** @type {Set<(v: unknown) => void>} */
const openDialogs = new Set();

/** Closes every open dialog of the page (used when the page changes). */
export function closeAllDialogs() {
  [...openDialogs].forEach((fn) => fn(null));
}

/**
 * openDialog({title, body, actions: [{label, kind, value, onClick}]}) resolves
 * with the clicked action's value (its label if it has none), or null when
 * dismissed. An action with onClick(dialog) shows a spinner while it runs
 * and closes the dialog with what it resolves to; when that is false or
 * undefined, or it throws (shown as a toast), the dialog stays open.
 * dismissible false gives it no close button and keeps Escape and a click
 * beside it from closing it. A dialog that outlivesPage is the app's, not
 * the page's: a page change leaves it open.
 * @param {object} options
 * @param {string} options.title
 * @param {Child} [options.body]
 * @param {DialogAction[]} [options.actions]
 * @param {boolean} [options.wide]
 * @param {boolean} [options.dismissible]
 * @param {boolean} [options.outlivesPage]
 * @returns {DialogHandle}
 */
export function openDialog({ title, body, actions = [{ label: 'Close', value: null }], wide = false, dismissible = true, outlivesPage = false }) {
  /** @type {((v: unknown) => void) | null} */
  let finishFn = null;
  /** @type {((list: DialogAction[]) => void) | null} */
  let setActionsFn = null;
  /** @type {Promise<unknown>} */
  const promise = new Promise((resolve) => {
    const dlg = h(`dialog.dialog${wide ? '.wide' : ''}`, { 'aria-label': title });
    let done = false;
    /** @param {unknown} v */
    const finish = (v) => {
      if (done) return;
      done = true;
      openDialogs.delete(finish);
      dlg.close();
      dlg.remove();
      resolve(v);
    };
    finishFn = finish;
    if (!outlivesPage) openDialogs.add(finish);
    const footer = h('div.dialog-actions');
    /** @param {DialogAction} a */
    const button = (a) => {
      const b = btn(a.label, { kind: a.kind || '', icon: a.icon });
      b.addEventListener('click', async (e) => {
        e.preventDefault();
        if (!a.onClick) { finish(a.value === undefined ? a.label : a.value); return; }
        const r = await runBusy(b, () => a.onClick(dlg));
        if (r !== false && r !== undefined) finish(r);
      });
      return b;
    };
    setActionsFn = (list) => footer.replaceChildren(...list.map(button));
    setActionsFn(actions);
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
  return Object.assign(promise, {
    /**
     * Closes the dialog from outside; the promise resolves with v.
     * @param {unknown} [v]
     */
    close: (v = null) => { if (finishFn) finishFn(v); },
    /**
     * Replaces the dialog's buttons with these actions, e.g. once what it waited for is over.
     * @param {DialogAction[]} list
     */
    setActions: (list) => { if (setActionsFn) setActionsFn(list); },
  });
}

/**
 * Asks a yes/no question; resolves with true for confirm, false for cancel
 * or a dismissed dialog.
 * @param {{title: string, message: Child, confirm?: string, danger?: boolean, cancel?: string}} options
 * @returns {Promise<boolean>}
 */
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
