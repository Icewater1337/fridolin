// One file of a linked folder (#/file/<root>/<path>, docs/FILES.md):
// Markdown rendered, text as text, images, audio and video in the page,
// a PDF in a tab of its own, anything else to download. Each opening is
// written to the audit log.
//
// What a brain wrote never runs here: Markdown comes as a tree of a few
// element names (components/markdown.js), text is a text node, and the
// bridge serves a file itself (/files/raw) under a policy that runs
// nothing and only from the bridge's own address.

import { get, qs } from '../app/api.js';
import { h, mount, paint } from '../lib/dom.js';
import { fmtBytes } from '../lib/format.js';
import { filesHref, rawHref, wikiHref } from '../lib/files.js';
import { btn, copyButton } from '../components/buttons.js';
import { icon } from '../components/icons.js';
import { callout, empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { markdownDoc, markdownToc } from '../components/markdown.js';

function crumbs(rootId, label, path) {
  const out = [h('a', { href: '#/files' }, 'Files'), ' / ', h('a', { href: filesHref(rootId, '', true) }, label)];
  const parts = path.split('/');
  let acc = '';
  parts.forEach((seg, i) => {
    acc = acc ? `${acc}/${seg}` : seg;
    out.push(' / ', i === parts.length - 1 ? h('span', seg) : h('a', { href: filesHref(rootId, acc, true) }, seg));
  });
  return h('nav.files-crumbs', { 'aria-label': 'Folder' }, out);
}

/** Where a rendered Markdown file's links go, from root. */
function links(root, ticket) {
  return {
    page: (p) => (root.kind === 'brain' && /^(wiki|memories)\//.test(p) ? wikiHref(root.brain, p) : filesHref(root.id, p, false)),
    file: (p) => filesHref(root.id, p, false),
    image: (p) => rawHref(root.id, p, ticket),
  };
}

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

export default {
  title: 'File',
  render(root, ctx) {
    const rootId = ctx.params.root;
    const path = ctx.params.path || '';
    const header = h('div', pageHeader(path.split('/').pop() || 'File'));
    const box = h('div', spinner());
    mount(root, header, box);
    Promise.all([
      get('/api/files/roots', { signal: ctx.signal }),
      get(`/api/files/view${qs({ root: rootId, path })}`, { signal: ctx.signal }),
      get(`/api/files/ticket${qs({ root: rootId })}`, { signal: ctx.signal }),
    ]).then(([{ roots }, v, { ticket }]) => {
      const r = roots.find((x) => x.id === rootId) || { id: rootId, label: rootId, kind: '' };
      const label = r.kind === 'media' ? `${r.label} · media` : r.label;
      ctx.setTitle(`${v.name} · ${label}`);
      const sub = [fmtBytes(v.size), v.mod_time && !v.mod_time.startsWith('0001') ? timeAgo(v.mod_time, 'changed ') : null];
      mount(header,
        pageHeader(v.name, h('span', sub.filter(Boolean).map((x, i) => (i ? [' · ', x] : x))),
          copyButton(path, 'Copy path'),
          btn('Download', { icon: 'download', small: true, href: rawHref(rootId, path, ticket, true) })),
        crumbs(rootId, label, path));
      paint(box, 'file', () => h('div',
        v.truncated ? callout('info', 'Only the start of this file', 'Download it for all of it.') : null,
        preview(v, r, path, ticket)));
    }, (err) => {
      if (!ctx.dead) paint(box, 'error', () => h('div.card', empty(err.status === 404 ? 'No such file' : 'Could not open this file', err.status === 404 ? 'It may have moved, or it is one Fridolin never shows (secrets, its own state, or what the folder’s backup leaves out).' : err.message)));
    });
  },
};
