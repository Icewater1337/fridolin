// Files: every folder the bridge is linked to — each brain's folder and
// media inbox, and the folders nodes lend — and what is in them
// (docs/FILES.md). #/files lists the folders; #/files/<root>/<path> one
// folder, with search by name and sorting; a file opens on #/file/….
// Where a folder is writable you upload into it, make a file, rename,
// move and delete (components/file-actions.js), and choose how it takes
// uploads from your site's roles. What a folder's backup leaves out,
// secrets and Fridolin's own state are never listed.

import { api, get, qs } from '../app/api.js';
import { h, mount, paint } from '../lib/dom.js';
import { fmtBytes } from '../lib/format.js';
import { filesHref } from '../lib/files.js';
import { icon } from '../components/icons.js';
import { btn } from '../components/buttons.js';
import { badge, empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { textInput, select } from '../components/fields.js';
import { deleteFile, editOnOpen, moveFile, newFile, uploadButton } from '../components/file-actions.js';
import { toast } from '../components/toast.js';

/** @import { Page, PageModule } from '../app/page.js' */
/** @import { Child } from '../lib/dom.js' */
/** @import { FileEntry, FileRoot, FileRootKind, FilesList, FilesSearch, FileUpload, UploadRule } from '../../types/api.js' */

/**
 * A row of the folder's table: an entry, at its path in the root.
 * @typedef {FileEntry & {at: string}} Row
 */

/** @type {[FileRootKind, string][]} */
const GROUPS = [['brain', 'Brain folders'], ['media', 'Media inboxes'], ['node', 'Node folders']];

/** @param {FileRoot} r */
function rootCard(r) {
  const badges = [
    badge(r.writable ? 'Writable' : 'Read-only', r.writable ? 'warn' : 'neutral'),
    badge(r.location === 'core' ? 'On the core' : `On ${r.location}`, 'neutral'),
  ];
  if (r.backup === 'on') badges.push(badge('Backed up', 'ok'));
  if (r.backup === 'off') badges.push(badge('Not backed up', 'warn'));
  if (r.kind === 'node' && !r.online) badges.push(badge('Offline', 'bad'));
  const title = r.kind === 'media' ? `${r.label} · media` : r.label;
  return h('a.card.files-root', { href: filesHref(r.id, '', true) },
    h('div.files-root-head', icon(r.kind === 'node' ? 'server' : r.kind === 'media' ? 'inbox' : 'folder'), h('strong', title)),
    h('code.mono.muted.small', r.path),
    h('div.files-badges', badges),
    r.excluded && r.excluded.length ? h('p.muted.small', `Hidden, as the backup leaves them out: ${r.excluded.join(', ')}`) : null);
}

/**
 * @param {HTMLElement} box
 * @param {Page} ctx
 */
function rootsView(box, ctx) {
  get('/api/files/roots', { signal: ctx.signal }).then(({ roots }) => {
    if (!roots.length) {
      paint(box, 'empty', () => h('div.card', empty('No folders yet', 'Add a brain, or approve a node that lends folders, and its files show here.')));
      return;
    }
    paint(box, 'roots', () => h('div', GROUPS.map(([kind, label]) => {
      const list = roots.filter((r) => r.kind === kind);
      return list.length ? h('section.files-group', h('h2', label), h('div.files-roots', list.map(rootCard))) : null;
    })));
  }, (err) => { if (!ctx.dead) paint(box, 'error', () => h('div.card', empty('Could not list the folders', err.message))); });
}

/**
 * @param {string} rootId
 * @param {string} label
 * @param {string} path
 */
function crumbs(rootId, label, path) {
  /** @type {Child[]} */
  const out = [h('a', { href: '#/files' }, 'Files'), ' / ', h('a', { href: filesHref(rootId, '', true) }, label)];
  let acc = '';
  const parts = path ? path.split('/') : [];
  parts.forEach((seg, i) => {
    acc = acc ? `${acc}/${seg}` : seg;
    out.push(' / ', i === parts.length - 1 ? h('span', seg) : h('a', { href: filesHref(rootId, acc, true) }, seg));
  });
  return h('nav.files-crumbs', { 'aria-label': 'Folder' }, out);
}

/** @type {[string, string][]} */
const SORTS = [['name', 'Name'], ['date', 'Newest first'], ['size', 'Largest first']];

/**
 * @param {Row[]} rows
 * @param {string} how
 * @returns {Row[]}
 */
function sorted(rows, how) {
  const dirsFirst = (a, b) => Number(Boolean(b.dir)) - Number(Boolean(a.dir));
  if (how === 'date') return [...rows].sort((a, b) => dirsFirst(a, b) || String(b.mod_time).localeCompare(String(a.mod_time)));
  if (how === 'size') return [...rows].sort((a, b) => dirsFirst(a, b) || (b.size || 0) - (a.size || 0));
  return rows;
}

/** @type {[FileUpload, string][]} */
const RULES = [['', 'Only you upload here'], ['folder', 'Site roles may upload: files land here'], ['brain', 'Site roles may upload: files go to the brain']];

/**
 * How the folder takes uploads from your site's roles (brain.json files.uploads).
 * @param {string} rootId
 * @param {string} path
 * @param {UploadRule | undefined} rule
 * @param {() => void} reload
 */
function ruleControl(rootId, path, rule, reload) {
  const own = rule && rule.folder === path ? rule.mode : '';
  const sel = select(RULES, own, { 'aria-label': 'Uploads from the site' });
  sel.addEventListener('change', async () => {
    try {
      await api('PUT', '/api/files/upload-rule', { root: rootId, path, mode: sel.value });
      toast('Saved', 'ok', { timeout: 1500 });
      reload();
    } catch (e) {
      sel.value = own;
      toast(e.message || String(e));
    }
  });
  const inherited = rule && rule.folder !== path
    ? h('span.muted.small', `Inside ${rule.folder}/, where ${rule.mode === 'brain' ? 'uploads go to the brain' : 'uploads land in the folder'}.`) : null;
  return h('div.files-rule', h('span.small', 'Uploads from the site:'), sel, inherited,
    h('span.muted.small', 'Who may upload is set on the Site page, under Files and Wiki.'));
}

/**
 * @param {HTMLElement} header
 * @param {HTMLElement} box
 * @param {Page} ctx
 * @param {string} rootId
 * @param {string} path
 */
function folderView(header, box, ctx, rootId, path) {
  /** @type {{sort: string, q: string, rows: Row[], truncated: boolean, searching: boolean, writable: boolean}} */
  const state = { sort: 'name', q: '', rows: [], truncated: false, searching: false, writable: false };
  const searchIn = textInput({ type: 'search', placeholder: 'Find by name below this folder', 'aria-label': 'Find by name' });
  const sortSel = select(SORTS, 'name', { 'aria-label': 'Sort' });
  const actions = h('div.toolbar-group');
  const ruleBox = h('div');
  const table = h('div');
  /** @param {Row} e */
  const rowActions = (e) => h('td', h('div.files-actions',
    btn('', {
      icon: 'edit', small: true, title: `Rename or move ${e.name}`,
      onClick: async () => { if (await moveFile(rootId, e.at)) load(''); },
    }),
    btn('', {
      icon: 'trash', small: true, title: `Delete ${e.name}`,
      onClick: async () => { if (await deleteFile(rootId, e.at, e.dir)) load(''); },
    })));
  const show = () => {
    if (!state.rows.length) {
      paint(table, 'empty', () => h('div.card', empty(state.q ? 'Nothing found' : 'This folder is empty', state.q ? `No name below this folder contains “${state.q}”.` : '')));
      return;
    }
    paint(table, `rows:${state.sort}:${state.q}:${state.rows.length}:${state.truncated}:${state.writable}`, () => [h('div.card.flush.table-wrap', h('table.table.files-table',
      h('thead', h('tr', h('th', 'Name'), h('th.num', 'Size'), h('th', 'Modified'), state.writable ? h('th', { 'aria-label': 'Actions' }) : null)),
      h('tbody', sorted(state.rows, state.sort).map((e) => h('tr',
        h('td', h('a.files-name', { href: filesHref(rootId, e.at, e.dir) }, icon(e.dir ? 'folder' : 'file'), state.searching ? e.at : e.name)),
        h('td.num', e.dir ? '' : fmtBytes(e.size)),
        h('td', timeAgo(e.mod_time)),
        state.writable ? rowActions(e) : null))))),
    state.truncated ? h('p.muted.small', 'Only the first entries are shown.') : null]);
  };
  /** @param {string} q */
  const load = (q) => {
    state.q = q;
    paint(table, 'loading', () => spinner());
    /** @type {Promise<FilesSearch | FilesList>} */
    const call = q ? get(`/api/files/search${qs({ root: rootId, path, q })}`, { signal: ctx.signal })
      : get(`/api/files/list${qs({ root: rootId, path })}`, { signal: ctx.signal });
    call.then((r) => {
      if ('entries' in r) {
        state.writable = Boolean(r.writable);
        /** @type {HTMLElement[]} */
        const buttons = [];
        if (r.upload) buttons.push(uploadButton(rootId, path, r.upload, () => load('')));
        if (r.writable) {
          buttons.push(btn('New file', {
            icon: 'plus', small: true,
            onClick: async () => { const p = await newFile(rootId, path); if (p) { editOnOpen(rootId, p); ctx.navigate(filesHref(rootId, p, false)); } },
          }));
        }
        mount(actions, buttons);
        mount(ruleBox, r.writable && rootId.startsWith('brain:') && path ? ruleControl(rootId, path, r.rule, () => load('')) : null,
          r.upload === 'brain' ? h('p.muted.small', 'What you upload here goes to the brain, as a file sent on WhatsApp: it puts it where it belongs and tells you in its chat.') : null);
      }
      state.searching = Boolean(q);
      state.rows = 'hits' in r ? r.hits.map((x) => ({ ...x, at: x.path })) : r.entries.map((e) => ({ ...e, at: path ? `${path}/${e.name}` : e.name }));
      state.truncated = r.truncated;
      show();
    }, (err) => { if (!ctx.dead) paint(table, 'error', () => h('div.card', empty('Could not open this folder', err.message))); });
  };
  searchIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); load(searchIn.value.trim()); } });
  searchIn.addEventListener('input', () => { if (!searchIn.value.trim() && state.q) load(''); });
  sortSel.addEventListener('change', () => { state.sort = sortSel.value; show(); });
  get('/api/files/roots', { signal: ctx.signal }).then(({ roots }) => {
    const r = roots.find((x) => x.id === rootId);
    if (!r) { paint(box, 'missing', () => h('div.card', empty('No such folder', 'It may belong to a brain that was removed, or a node that is no longer approved.'))); return; }
    const label = r.kind === 'media' ? `${r.label} · media` : r.label;
    ctx.setTitle(path ? `${path.split('/').pop()} · ${label}` : label);
    mount(header, pageHeader(path ? path.split('/').pop() : label, r.path), crumbs(rootId, label, path));
    paint(box, 'folder', () => h('div', h('div.toolbar', h('div.toolbar-group', searchIn, sortSel), actions), ruleBox, table));
    load('');
  }, (err) => { if (!ctx.dead) paint(box, 'error', () => h('div.card', empty('Could not list the folders', err.message))); });
}

export default /** @satisfies {PageModule} */ ({
  title: 'Files',
  render(root, ctx) {
    const header = h('div', pageHeader('Files', 'Every folder Fridolin is linked to: your brains’ folders and media, and the folders nodes lend. What a folder’s backup leaves out, secrets and Fridolin’s own state are never shown.'));
    const box = h('div', spinner());
    mount(root, header, box);
    if (ctx.params.root) folderView(header, box, ctx, ctx.params.root, ctx.params.path || '');
    else rootsView(box, ctx);
  },
});
