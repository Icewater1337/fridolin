// Picks a folder on a node for a backup target: the node's drives and
// volumes (fs.roots), then folder names below one (fs.browse). The node
// lists names of folders only, never a file or its content.

import { post } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { icon } from './icons.js';
import { btn } from './buttons.js';
import { textInput } from './fields.js';
import { badge, spinner } from './common.js';
import { fmtBytes } from '../lib/format.js';

/** One call on the node through the dashboard's generic call route. */
async function nodeCall(node, method, params) {
  const r = await post(`/api/nodes/${encodeURIComponent(node)}/call`, { method, params: params || {} });
  return r.result || {};
}

/** joinPath joins a folder name to a path in the node's own spelling. */
export function joinPath(dir, name, sep) {
  const s = sep || (dir.includes('\\') ? '\\' : '/');
  return dir.endsWith(s) ? dir + name : dir + s + name;
}

/**
 * nodeFolderPicker({node, onPick(path)}) returns an element that browses
 * node's drives and folders; onPick gets the folder chosen, which may be a
 * new folder below the one shown (the core makes it when the target is
 * added).
 */
export function nodeFolderPicker({ node, onPick }) {
  let sep = '/';
  let current = null; // the folder shown, null at the list of drives
  const listEl = h('div.fb-list');
  const err = h('p.form-error', { hidden: true });
  const cur = h('code.mono.fb-current');
  const upBtn = h('button.icon-btn', { type: 'button', title: 'Up one folder', 'aria-label': 'Up one folder' }, icon('up'));
  const useBtn = btn('Use this folder', { kind: 'primary', icon: 'check', small: true, disabled: true });
  const nameIn = textInput({ placeholder: 'New folder name, such as fridolin-backup', 'aria-label': 'New folder name' });
  const newBtn = btn('Use new folder', { icon: 'folderPlus', small: true, disabled: true });

  const fail = (e) => { err.hidden = false; err.textContent = e.message || String(e); };
  upBtn.addEventListener('click', () => {
    if (!current) return;
    if (current.parent) browse(current.parent); else roots();
  });
  useBtn.addEventListener('click', () => { if (current) onPick(current.path); });
  newBtn.addEventListener('click', () => {
    const name = nameIn.value.trim();
    if (!current || !name) { nameIn.focus(); return; }
    if (/[\\/:*?"<>|]/.test(name) || name === '.' || name === '..') {
      fail(new Error('Use a plain folder name, without slashes or : * ? " < > |.'));
      return;
    }
    onPick(joinPath(current.path, name, sep));
  });

  function busy() { mount(listEl, h('div.fb-loading.muted', spinner(), h('span', 'Asking the node…'))); err.hidden = true; }

  async function roots() {
    busy();
    current = null;
    try {
      const r = await nodeCall(node, 'fs.roots');
      sep = r.sep || '/';
      cur.textContent = 'Drives and volumes';
      upBtn.disabled = true;
      useBtn.disabled = true;
      newBtn.disabled = true;
      const list = r.roots || [];
      if (!list.length) { mount(listEl, h('div.fb-empty.muted', 'The node lists no drive it lends for backups.')); return; }
      mount(listEl, h('ul.fb-entries', { 'aria-label': 'Drives' }, list.map((v) => h('li', h('button.fb-item', { type: 'button', title: v.path, onclick: () => browse(v.path) },
        icon('folder'), h('span.fb-name', v.label ? `${v.label} — ${v.path}` : v.path),
        v.removable ? badge('removable', 'neutral', 'An external or network disk that may be away') : null,
        v.free ? h('span.muted.small', ` ${fmtBytes(v.free)} free`) : null,
        icon('right', 'fb-chevron'))))));
    } catch (e) { fail(e); mount(listEl); }
  }

  async function browse(path) {
    busy();
    try {
      const r = await nodeCall(node, 'fs.browse', { path });
      current = r;
      cur.textContent = r.path;
      upBtn.disabled = false;
      useBtn.disabled = false;
      newBtn.disabled = false;
      const folders = r.folders || [];
      mount(listEl,
        r.repo ? h('p.small', badge('restic', 'info', 'This folder holds a restic repository'), ' This folder holds a restic repository already: a target here opens it with the password the core keeps for it.') : null,
        folders.length
          ? h('ul.fb-entries', { 'aria-label': 'Folders' }, folders.map((name) => h('li', h('button.fb-item', { type: 'button', onclick: () => browse(joinPath(r.path, name, sep)) },
            icon('folder'), h('span.fb-name', name), icon('right', 'fb-chevron')))))
          : h('div.fb-empty.muted', 'No folders here. Use this one, or name a new one below.'),
        r.truncated ? h('p.muted.small', 'More folders than shown.') : null);
    } catch (e) { fail(e); }
  }

  const el = h('div.folder-browser',
    h('div.fb-toolbar', upBtn, cur),
    err,
    listEl,
    h('div.fb-footer',
      h('div.fb-use', useBtn),
      h('div.fb-new', nameIn, newBtn)));
  roots();
  return el;
}
