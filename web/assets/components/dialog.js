// Modal dialogs on <dialog>: openDialog for custom content, confirmDialog
// for yes/no questions.

import { h, append } from '../lib/dom.js';
import { icon } from './icons.js';
import { btn, runBusy } from './buttons.js';

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
 */
export function openDialog({ title, body, actions = [{ label: 'Close', value: null }], wide = false, dismissible = true, outlivesPage = false }) {
  let finishFn = null;
  let setActionsFn = null;
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
    if (!outlivesPage) openDialogs.add(finish);
    const footer = h('div.dialog-actions');
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
  /** Closes the dialog from outside; the promise resolves with v. */
  promise.close = (v = null) => { if (finishFn) finishFn(v); };
  /** Replaces the dialog's buttons with these actions, e.g. once what it waited for is over. */
  promise.setActions = (list) => { if (setActionsFn) setActionsFn(list); };
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
