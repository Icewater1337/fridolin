// In-dashboard folder browser (folders inside the home folder only), used
// when adding a brain and when choosing where to restore a backup.

import { get, qs } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { icon } from './icons.js';
import { btn } from './buttons.js';
import { toggle, textInput } from './fields.js';
import { badge, spinner } from './common.js';

/**
 * folderBrowser({start, onPick(path, {create}), pickLabel, newLabel, signal})
 * returns an element. onPick receives an absolute path; create is true when
 * the folder does not exist yet (typed into "New folder"). signal (the page's)
 * stops loading when it aborts. el.reload() loads the folder shown again.
 * @param {object} options
 * @param {string} [options.start]
 * @param {(path: string, how: {create: boolean}) => void} options.onPick
 * @param {string} [options.pickLabel]
 * @param {string} [options.newLabel]
 * @param {string} [options.newHint]
 * @param {AbortSignal} [options.signal]
 */
export function folderBrowser({ start = '', onPick, pickLabel = 'Use this folder', newLabel = 'Use new folder', newHint = '', signal }) {
  let path = start;
  let hidden = false;
  let data = null;
  let loading = null; // AbortController of the folder being loaded
  const crumbs = h('nav.fb-crumbs', { 'aria-label': 'Folder path' });
  const listEl = h('div.fb-list');
  const err = h('p.form-error', { hidden: true });
  const upBtn = h('button.icon-btn', { type: 'button', title: 'Up one folder', 'aria-label': 'Up one folder' }, icon('up'));
  const hiddenSw = toggle({ label: 'Show hidden', onChange: (v) => { hidden = v; load(path); } });
  const useBtn = btn(pickLabel, { kind: 'primary', icon: 'check', small: true, disabled: true });
  const cur = h('code.mono.fb-current');
  const nameIn = textInput({ placeholder: 'New folder name', 'aria-label': 'New folder name' });
  const newBtn = btn(newLabel, { icon: 'folderPlus', small: true, disabled: true });
  const newErr = h('span.form-error.small', { hidden: true });

  upBtn.addEventListener('click', () => { if (data && data.parent) load(data.parent); });
  useBtn.addEventListener('click', () => { if (data) onPick(data.path, { create: false }); });
  const submitNew = () => {
    const name = nameIn.value.trim();
    newErr.hidden = true;
    if (!name) { nameIn.focus(); return; }
    if (name.includes('/') || name === '.' || name === '..') {
      newErr.hidden = false;
      newErr.textContent = 'Use a plain folder name without slashes.';
      return;
    }
    if (data) onPick(`${data.path.replace(/\/+$/, '')}/${name}`, { create: true });
  };
  newBtn.addEventListener('click', submitNew);
  nameIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); submitNew(); } });

  const el = h('div.folder-browser',
    h('div.fb-toolbar', upBtn, crumbs, hiddenSw),
    err,
    listEl,
    h('div.fb-footer',
      h('div.fb-use', h('span.muted.small', 'Current folder'), cur, useBtn),
      h('div.fb-new', nameIn, newBtn, newErr),
      newHint ? h('p.muted.small', newHint) : null));

  // Only the folder clicked last is shown: opening another one stops the
  // load before it, so a slow answer cannot replace a newer folder.
  // Leaving the page stops the one in progress.
  if (signal) signal.addEventListener('abort', () => { if (loading) loading.abort(); }, { once: true });
  async function load(p) {
    if (loading) loading.abort();
    if (signal && signal.aborted) return;
    const mine = new AbortController();
    loading = mine;
    mount(listEl, h('div.fb-loading.muted', spinner(), h('span', 'Loading…')));
    err.hidden = true;
    try {
      const next = await get(`/api/fs/list${qs({ path: p, hidden: hidden ? 1 : '' })}`, { signal: mine.signal });
      if (loading !== mine) return;
      data = next;
      path = data.path;
      paint();
    } catch (e) {
      if (loading !== mine || mine.signal.aborted) return;
      err.hidden = false;
      err.textContent = e.message;
      if (!data) mount(listEl);
      else paint();
    }
  }

  function paint() {
    const home = data.home.replace(/\/+$/, '');
    const rel = data.path === home ? '' : data.path.slice(home.length + 1);
    const segs = rel ? rel.split('/') : [];
    /** @type {HTMLElement[]} */
    const items = [h('button.crumb', { type: 'button', onclick: () => load(home) }, icon('overview'), 'Home')];
    segs.forEach((s, i) => {
      const target = `${home}/${segs.slice(0, i + 1).join('/')}`;
      items.push(h('span.crumb-sep', '/'));
      items.push(i === segs.length - 1 ? h('span.crumb.current', s) : h('button.crumb', { type: 'button', onclick: () => load(target) }, s));
    });
    mount(crumbs, items);
    upBtn.disabled = !data.parent;
    newBtn.disabled = false;
    cur.textContent = rel ? `~/${rel}` : '~';
    cur.title = data.path;
    const atHome = data.path === home;
    useBtn.disabled = atHome;
    useBtn.title = atHome ? 'Pick or create a folder inside your home folder' : data.path;
    if (!data.entries.length) {
      mount(listEl, h('div.fb-empty.muted', 'No folders here. Create a new one below.'));
      return;
    }
    // A list of buttons: a role on the button itself would hide from
    // screen readers that it is one.
    mount(listEl, h('ul.fb-entries', { 'aria-label': 'Folders' }, data.entries.map((e) => h('li', h('button.fb-item', { type: 'button', title: e.path, onclick: () => load(e.path) },
      icon('folder'),
      h('span.fb-name', e.name),
      e.is_brain ? badge('brain.json', 'info', 'This folder has a brain.json') : null,
      e.is_git ? badge('git', 'neutral', 'Git repository') : null,
      icon('right', 'fb-chevron'))))));
  }

  load(path);
  return Object.assign(el, { reload: () => load(path) });
}
