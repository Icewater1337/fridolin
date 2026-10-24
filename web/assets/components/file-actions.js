// Changing files on the Files, File and Wiki pages (docs/FILES.md,
// "Changing files"): edit a text or Markdown file in place, upload into a
// folder, create a file, rename or move, and delete into the folder's
// trash. The bridge checks every change again, audits it and commits it
// where the folder is a git repository; these only ask and show.

import { api, get, qs, upload } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { btn } from './buttons.js';
import { spinner, timeAgo } from './common.js';
import { confirmDialog, openDialog } from './dialog.js';
import { textInput } from './fields.js';
import { toast } from './toast.js';

const baseName = (p) => p.slice(p.lastIndexOf('/') + 1);

// A file just made opens in its editor: the page that made it says so
// here, the File or Wiki page asks as it opens.
let editNext = '';

/** Opens the file path of root in its editor when its page opens next. */
export function editOnOpen(rootId, path) { editNext = `${rootId}\u0000${path}`; }

/** Whether the file path of root was to open in its editor; asks once. */
export function takeEditOnOpen(rootId, path) {
  const yes = editNext === `${rootId}\u0000${path}`;
  editNext = '';
  return yes;
}

/** Tells what became of a change in a git repository. */
function noteCommit(r) {
  if (r && r.git_error) toast(`Saved, but ${r.git_error}`, 'warn');
  else if (r && r.commit) toast(`Saved and committed (${r.commit})`, 'ok', { timeout: 2500 });
  else toast('Saved', 'ok', { timeout: 1800 });
}

/**
 * Asks for a new path of path in the root and moves it there. Resolves
 * with the new path, or null.
 */
export async function moveFile(rootId, path) {
  const input = textInput({ value: path, 'aria-label': 'New path', spellcheck: 'false', autocomplete: 'off' });
  const r = await openDialog({
    title: `Rename or move ${baseName(path)}`,
    body: h('div', h('p.muted', 'Its path in the folder. Change the name to rename it, the folders before it to move it; the folder must be there.'), input),
    actions: [
      { label: 'Cancel', value: null },
      {
        label: 'Move',
        kind: 'primary',
        onClick: async () => {
          const to = input.value.trim().replace(/^\/+|\/+$/g, '');
          if (!to || to === path) return null;
          return api('POST', '/api/files/move', { root: rootId, path, to });
        },
      },
    ],
  });
  if (!r) return null;
  noteCommit(r);
  return r.path;
}

/** Asks, then deletes path (into the folder's trash). Resolves true once it is gone. */
export async function deleteFile(rootId, path, dir) {
  const ok = await confirmDialog({
    title: `Delete ${baseName(path)}?`,
    message: `${dir ? 'The folder and everything in it go' : 'It goes'} into the folder’s trash, .fridolin-trash, where it stays until you empty that yourself.`,
    confirm: 'Delete',
    danger: true,
  });
  if (!ok) return false;
  noteCommit(await api('POST', '/api/files/delete', { root: rootId, path }));
  return true;
}

/**
 * Asks for the name of a new text or Markdown file in folder and creates
 * it empty, or with text. Resolves with its path, or null.
 */
export async function newFile(rootId, folder, { text = '', placeholder = 'notes.md' } = {}) {
  const input = textInput({ placeholder, 'aria-label': 'Name', spellcheck: 'false', autocomplete: 'off' });
  const r = await openDialog({
    title: 'New file',
    body: h('div', h('p.muted', `A Markdown (.md) or text (.txt) file in ${folder ? `${folder}/` : 'the folder'}; a name with / in it makes the folders too.`), input),
    actions: [
      { label: 'Cancel', value: null },
      {
        label: 'Create',
        kind: 'primary',
        onClick: async () => {
          let name = input.value.trim().replace(/^\/+|\/+$/g, '');
          if (!name) return null;
          if (!/\.[a-z0-9]+$/i.test(name)) name += '.md';
          return api('PUT', '/api/files/text', { root: rootId, path: folder ? `${folder}/${name}` : name, text, sha: '' });
        },
      },
    ],
  });
  if (!r) return null;
  noteCommit(r);
  return r.path;
}

/**
 * An Upload button for the folder path of root: where the files go is the
 * bridge's to say (to: 'folder' or 'brain'). onDone(result) runs after.
 */
export function uploadButton(rootId, path, to, onDone) {
  const input = h('input', { type: 'file', multiple: true, hidden: true, 'aria-label': 'Files to upload' });
  const button = btn(to === 'brain' ? 'Send to the brain' : 'Upload', { icon: 'up', small: true });
  button.addEventListener('click', (e) => { e.preventDefault(); input.click(); });
  input.addEventListener('change', async () => {
    const list = [...(input.files || [])];
    input.value = '';
    if (!list.length) return;
    button.disabled = true;
    button.classList.add('busy');
    try {
      const r = await upload(`/api/files/upload?${new URLSearchParams({ root: rootId, path })}`, list);
      const n = r.files.length;
      if (r.to === 'brain') toast(`${n === 1 ? 'The file went' : `${n} files went`} to the brain, which files ${n === 1 ? 'it' : 'them'}. Its answer comes in its chat.`, 'ok');
      else noteCommit(r);
      if (onDone) onDone(r);
    } catch (e) {
      toast(e.message || String(e));
    } finally {
      button.disabled = false;
      button.classList.remove('busy');
    }
  });
  return h('span.files-upload', button, input);
}

/**
 * An editor of a text file: a textarea holding text, Save and Cancel.
 * Saving sends sha, the version it started from; one changed meanwhile is
 * refused, and the text stays to copy. onSaved(result, text) runs after a
 * save, onCancel() after Cancel. editor.dirty() says whether there is
 * something to save.
 */
export function textEditor({ rootId, path, text, sha, onSaved, onCancel }) {
  let base = text;
  let version = sha;
  const area = h('textarea.file-editor', { 'aria-label': `Text of ${baseName(path)}`, spellcheck: path.endsWith('.md') ? 'true' : 'false', rows: 24 });
  area.value = text;
  const status = h('span.muted.small', { role: 'status' });
  const save = btn('Save', {
    kind: 'primary',
    icon: 'check',
    onClick: async () => {
      try {
        const r = await api('PUT', '/api/files/text', { root: rootId, path, text: area.value, sha: version });
        base = area.value;
        version = r.sha;
        status.textContent = '';
        noteCommit(r);
        if (onSaved) onSaved(r, base);
      } catch (e) {
        status.textContent = e.status === 409 ? 'It changed elsewhere meanwhile: copy your text, open the file again and make your change there.' : (e.message || String(e));
      }
    },
  });
  const cancel = btn('Cancel', {
    onClick: async () => {
      if (area.value !== base && !(await confirmDialog({ title: 'Discard your changes?', message: 'What you typed is not saved.', confirm: 'Discard', danger: true }))) return;
      area.value = base;
      if (onCancel) onCancel();
    },
  });
  const el = h('div.card.file-edit', area, h('div.toolbar', h('div.toolbar-group', save, cancel), status));
  el.dirty = () => area.value !== base;
  el.focus = () => area.focus();
  return el;
}

/** The changes made to the file here, newest first, once asked for. */
export function historyBox(ctx, rootId, path) {
  const list = h('div');
  const box = h('details.card.file-history-box', h('summary', 'History'), list);
  box.addEventListener('toggle', () => {
    if (!box.open || list.dataset.loaded) return;
    list.dataset.loaded = '1';
    mount(list, spinner());
    get(`/api/files/history${qs({ root: rootId, path })}`, { signal: ctx.signal }).then(({ changes }) => {
      mount(list, changes.length ? h('ul.file-history', changes.map((c) => h('li',
        h('strong', c.action), ' ', c.to && c.action === 'move' ? `${c.path} → ${c.to}` : c.path, ' · ', c.by, ' · ', timeAgo(c.at),
        c.commit ? h('span.muted.small', ` · commit ${c.commit}`) : null)))
        : h('p.muted.small', 'No changes made here yet. Changes the brain makes itself are in its folder’s git history, where it has one.'));
    }, (err) => { if (!ctx.dead) mount(list, h('p.muted.small', err.message)); });
  });
  return box;
}

