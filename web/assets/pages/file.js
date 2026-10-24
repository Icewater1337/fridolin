// One file of a linked folder (#/file/<root>/<path>, docs/FILES.md):
// Markdown rendered, text as text, images, audio and video in the page,
// a PDF in a tab of its own, anything else to download. Each opening is
// written to the audit log. Where the folder is writable, a text or
// Markdown file is edited in place, and any file renamed, moved or
// deleted (components/file-actions.js); its changes are listed under
// History. A brain's file can be asked about on WhatsApp, and its link
// copied: its address on your site where the site has a domain.
//
// What a brain wrote never runs here: Markdown comes as a tree of a few
// element names (components/markdown.js), text is a text node, and the
// bridge serves a file itself (/files/raw) under a policy that runs
// nothing and only from the bridge's own address.

import { get, qs } from '../app/api.js';
import { h, mount, paint } from '../lib/dom.js';
import { fmtBytes } from '../lib/format.js';
import { filesHref, parentOf, rawHref, siteFileHref, whatsappHref, wikiHref } from '../lib/files.js';
import { btn, copyButton } from '../components/buttons.js';
import { deleteFile, historyBox, moveFile, takeEditOnOpen, textEditor } from '../components/file-actions.js';
import { icon } from '../components/icons.js';
import { callout, empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { markdownDoc, markdownToc } from '../components/markdown.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Child } from '../lib/dom.js' */
/** @import { FileRoot, FileRootKind, FileView, SiteSettings } from '../../types/api.js' */

/**
 * The root a file is in; kind '' when the bridge no longer lists it.
 * @typedef {Pick<FileRoot, 'id' | 'label' | 'brain'> & {kind: FileRootKind | '', writable?: boolean}} Root
 */

/**
 * @param {string} rootId
 * @param {string} label
 * @param {string} path
 */
function crumbs(rootId, label, path) {
  /** @type {Child[]} */
  const out = [h('a', { href: '#/files' }, 'Files'), ' / ', h('a', { href: filesHref(rootId, '', true) }, label)];
  const parts = path.split('/');
  let acc = '';
  parts.forEach((seg, i) => {
    acc = acc ? `${acc}/${seg}` : seg;
    out.push(' / ', i === parts.length - 1 ? h('span', seg) : h('a', { href: filesHref(rootId, acc, true) }, seg));
  });
  return h('nav.files-crumbs', { 'aria-label': 'Folder' }, out);
}

/**
 * Where a rendered Markdown file's links go, from root.
 * @param {Root} root
 * @param {string} ticket
 */
function links(root, ticket) {
  return {
    page: (p) => (root.kind === 'brain' && /^(wiki|memories)\//.test(p) ? wikiHref(root.brain, p) : filesHref(root.id, p, false)),
    file: (p) => filesHref(root.id, p, false),
    image: (p) => rawHref(root.id, p, ticket),
  };
}

/**
 * @param {FileView} v
 * @param {Root} root
 * @param {string} path
 * @param {string} ticket
 */
function preview(v, root, path, ticket) {
  const src = rawHref(root.id, path, ticket);
  switch (v.kind) {
    case 'markdown': {
      const doc = markdownDoc(v.doc, links(root, ticket));
      return h('div.md-layout', h('div.card', doc), markdownToc(v.doc, doc));
    }
    case 'text': return h('div.card.flush', h('pre.file-text', v.text));
    case 'image': return h('div.card.file-media', h('img', { src, alt: v.name }));
    case 'audio': return h('div.card', h('audio', { controls: true, preload: 'metadata', src }));
    case 'video': return h('div.card.file-media', h('video', { controls: true, preload: 'metadata', src }));
    case 'pdf': return h('div.card', empty('A PDF', 'It opens in a tab of its own, in your browser’s viewer.',
      h('a.btn.primary', { href: src, target: '_blank', rel: 'noopener' }, icon('external'), h('span.btn-label', 'Open the PDF'))));
    default: return h('div.card', empty('No preview', 'Fridolin shows Markdown, text, images, PDFs, audio and video. Download this file to open it.'));
  }
}

/**
 * "Ask the brain" on WhatsApp, its message about the file typed out.
 * @param {Root} root
 * @param {string} path
 * @param {string | null | undefined} number the bridge's own WhatsApp number
 */
function askButton(root, path, number) {
  if (root.kind !== 'brain' || !number) return null;
  const text = `About the file ${path} in the ${root.label} brain's folder: `;
  return btn('Ask the brain', { icon: 'send', small: true, href: whatsappHref(number, text), title: 'Opens your chat with Fridolin on WhatsApp, the question begun' });
}

/**
 * The link to copy: the file on your site, for a brain's file where the site has a domain; else this page.
 * @param {Root} root
 * @param {string} path
 * @param {SiteSettings | null | undefined} site
 */
function linkToCopy(root, path, site) {
  if (root.kind === 'brain' && site && site.domain) return siteFileHref(site.domain, root.brain, path);
  const here = globalThis.location;
  return `${here ? here.origin + here.pathname : ''}${filesHref(root.id, path, false)}`;
}

export default /** @satisfies {PageModule} */ ({
  title: 'File',
  render(root, ctx) {
    const rootId = ctx.params.root;
    const path = ctx.params.path || '';
    const header = h('div', pageHeader(path.split('/').pop() || 'File'));
    const box = h('div', spinner());
    mount(root, header, box);
    const quiet = (p) => p.catch(() => null);
    Promise.all([
      get('/api/files/roots', { signal: ctx.signal }),
      get(`/api/files/view${qs({ root: rootId, path, source: '1' })}`, { signal: ctx.signal }),
      get(`/api/files/ticket${qs({ root: rootId })}`, { signal: ctx.signal }),
      quiet(get('/api/status', { signal: ctx.signal })),
      quiet(get('/api/site', { signal: ctx.signal })),
    ]).then(([{ roots }, first, { ticket }, status, site]) => {
      let v = first;
      /** @type {Root} */
      const r = roots.find((x) => x.id === rootId) || { id: rootId, label: rootId, kind: '' };
      const label = r.kind === 'media' ? `${r.label} · media` : r.label;
      ctx.setTitle(`${v.name} · ${label}`);
      const sub = [fmtBytes(v.size), v.mod_time && !v.mod_time.startsWith('0001') ? timeAgo(v.mod_time, 'changed ') : null];
      const number = status && status.whatsapp && status.whatsapp.own_number;
      /** @type {ReturnType<typeof textEditor> | null} */
      let editor = null;
      const show = () => paint(box, 'file', () => h('div',
        v.truncated ? callout('info', 'Only the start of this file', 'Download it for all of it.') : null,
        preview(v, r, path, ticket),
        r.writable ? historyBox(ctx, rootId, path) : null));
      const edit = () => {
        editor = textEditor({
          rootId, path, text: v.kind === 'markdown' ? v.source : v.text, sha: v.sha,
          onSaved: async () => {
            editor = null;
            ctx.dirty = null;
            v = await get(`/api/files/view${qs({ root: rootId, path, source: '1' })}`, { signal: ctx.signal });
            show();
          },
          onCancel: () => { editor = null; ctx.dirty = null; show(); },
        });
        ctx.dirty = () => Boolean(editor && editor.dirty());
        paint(box, 'edit', () => editor);
        editor.focus();
      };
      mount(header,
        pageHeader(v.name, h('span', sub.filter(Boolean).map((x, i) => (i ? [' · ', x] : x))),
          copyButton(path, 'Copy path'),
          copyButton(linkToCopy(r, path, site && site.settings), r.kind === 'brain' && site && site.settings && site.settings.domain ? 'Copy the link on your site' : 'Copy link'),
          askButton(r, path, number),
          v.editable ? btn('Edit', { icon: 'edit', small: true, onClick: edit }) : null,
          r.writable ? btn('Rename', {
            icon: 'right', small: true,
            onClick: async () => { const to = await moveFile(rootId, path); if (to) ctx.navigate(filesHref(rootId, to, false)); },
          }) : null,
          r.writable ? btn('Delete', {
            icon: 'trash', small: true, kind: 'danger',
            onClick: async () => { if (await deleteFile(rootId, path, false)) ctx.navigate(filesHref(rootId, parentOf(path), true)); },
          }) : null,
          btn('Download', { icon: 'download', small: true, href: rawHref(rootId, path, ticket, true) })),
        crumbs(rootId, label, path));
      if (v.editable && takeEditOnOpen(rootId, path)) edit();
      else show();
    }, (err) => {
      if (!ctx.dead) paint(box, 'error', () => h('div.card', empty(err.status === 404 ? 'No such file' : 'Could not open this file', err.status === 404 ? 'It may have moved, or it is one Fridolin never shows (secrets, its own state, or what the folder’s backup leaves out).' : err.message)));
    });
  },
});
