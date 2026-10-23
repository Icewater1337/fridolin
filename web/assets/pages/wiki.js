// Wiki: each brain's wiki/ folder and memory notes as pages to read, with
// links between them, backlinks, a table of contents and search across a
// brain's pages or all of them (docs/FILES.md). #/wiki lists the brains;
// #/wiki/<brain> opens its front page; #/wiki/<brain>/<path> a page.
// Read-only; each page opened is written to the audit log.

import { get, qs } from '../app/api.js';
import { h, mount, paint } from '../lib/dom.js';
import { filesHref, rawHref, wikiHref } from '../lib/files.js';
import { btn } from '../components/buttons.js';
import { empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { textInput } from '../components/fields.js';
import { icon } from '../components/icons.js';
import { markdownDoc, markdownToc } from '../components/markdown.js';

function hits(list, oneBrain) {
  if (!list.length) return h('div.card', empty('Nothing found', 'No page has all of these words.'));
  return h('ul.wiki-hits', list.map((x) => h('li',
    h('a', { href: wikiHref(x.brain, x.path) }, x.title),
    oneBrain ? null : h('span.muted.small', ` · ${x.brain}`),
    h('p.muted.small', x.snippet))));
}

/** A search box; results go into out. */
function searchBox(ctx, out, brain) {
  const input = textInput({ type: 'search', placeholder: brain ? 'Search this wiki' : 'Search every wiki', 'aria-label': 'Search the wiki' });
  input.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    const q = input.value.trim();
    if (!q) { paint(out, 'none', () => null); return; }
    paint(out, 'loading', () => spinner());
    get(`/api/wiki/search${qs({ q, brain })}`, { signal: ctx.signal }).then(
      (r) => paint(out, `q:${q}`, () => h('section.wiki-results', h('h2', `Search: ${q}`), hits(r.hits, Boolean(brain)))),
      (err) => { if (!ctx.dead) paint(out, 'error', () => h('div.card', empty('Search failed', err.message))); });
  });
  return input;
}

function brainsView(root, ctx) {
  const results = h('div');
  const box = h('div', spinner());
  mount(root, pageHeader('Wiki', 'Each brain’s wiki/ folder and its memory notes, as pages. Brains write them; here you read and search them.'),
    h('div.toolbar', h('div.toolbar-group', searchBox(ctx, results, ''))), results, box);
  get('/api/wiki', { signal: ctx.signal }).then(({ brains }) => {
    if (!brains.length) {
      paint(box, 'none', () => h('div.card', empty('No wiki yet', 'A brain’s pages are the Markdown files in its wiki/ and memories/ folders. Ask a brain to start its wiki.')));
      return;
    }
    paint(box, 'brains', () => h('div.files-roots', brains.map((b) => h('a.card.files-root', { href: wikiHref(b.brain, '') },
      h('div.files-root-head', icon('book'), h('strong', b.label)),
      h('p.muted.small', `${b.pages} ${b.pages === 1 ? 'page' : 'pages'}`)))));
  }, (err) => { if (!ctx.dead) paint(box, 'error', () => h('div.card', empty('Could not read the wikis', err.message))); });
}

/** The pages, by folder: the wiki's own, then the memory notes. */
function pageTree(brain, pages, current) {
  const groups = new Map();
  for (const p of pages) {
    const folder = p.section === 'memories' ? 'Memories' : (p.name.includes('/') ? p.name.slice(0, p.name.lastIndexOf('/')) : '');
    if (!groups.has(folder)) groups.set(folder, []);
    groups.get(folder).push(p);
  }
  return h('nav.wiki-tree', { 'aria-label': 'Pages' }, [...groups].map(([folder, list]) => h('div',
    folder ? h('h3', folder) : null,
    h('ul', list.map((p) => h('li', h('a', { href: wikiHref(brain, p.path), 'aria-current': p.path === current ? 'page' : null }, p.title)))))));
}

function brainView(root, ctx, brain, page) {
  const results = h('div');
  const main = h('div.wiki-main', spinner());
  const side = h('aside.wiki-side', spinner());
  mount(root, h('div.wiki-layout', side, h('div', results, main)));
  Promise.all([
    get(`/api/wiki/${encodeURIComponent(brain)}/pages`, { signal: ctx.signal }),
    get(`/api/files/ticket${qs({ root: `brain:${brain}` })}`, { signal: ctx.signal }),
  ]).then(([idx, { ticket }]) => {
    const path = page || idx.home;
    mount(side, h('a.muted.small', { href: '#/wiki' }, '← Every wiki'), h('h2', idx.label), searchBox(ctx, results, brain), pageTree(brain, idx.pages, path));
    if (!path) {
      ctx.setTitle(`${idx.label} · Wiki`);
      paint(main, 'list', () => h('div', pageHeader(idx.label, `${idx.pages.length} pages; pick one.`),
        idx.pages.length ? null : h('div.card', empty('No pages', 'This brain’s wiki/ and memories/ folders hold no Markdown files yet.'))));
      return;
    }
    get(`/api/wiki/${encodeURIComponent(brain)}/page${qs({ path })}`, { signal: ctx.signal }).then((pg) => {
      const title = pg.doc.title || pg.page.title;
      ctx.setTitle(`${title} · ${idx.label}`);
      const links = {
        page: (p) => (/^(wiki|memories)\//.test(p) ? wikiHref(brain, p) : filesHref(`brain:${brain}`, p, false)),
        file: (p) => filesHref(`brain:${brain}`, p, false),
        image: (p) => rawHref(`brain:${brain}`, p, ticket),
      };
      const doc = markdownDoc(pg.doc, links);
      const hasH1 = pg.doc.body.some((n) => n.t === 'h1');
      paint(main, `page:${path}:${pg.page.mod_time}`, () => h('div.md-layout',
        h('div',
          h('p.muted.small.wiki-meta', pg.page.section === 'memories' ? 'Memory note' : 'Wiki', ' · ', timeAgo(pg.page.mod_time, 'changed '), ' · ',
            h('a', { href: filesHref(`brain:${brain}`, path, false) }, path)),
          hasH1 ? null : h('h1.wiki-title', title),
          pg.doc.tags && pg.doc.tags.length ? h('div.wiki-tags', pg.doc.tags.map((t) => h('span.badge.neutral', t))) : null,
          h('div.card', doc),
          pg.backlinks.length ? h('section.wiki-backlinks', h('h3', 'Linked from'),
            h('ul', pg.backlinks.map((b) => h('li', h('a', { href: wikiHref(brain, b.path) }, b.title))))) : null),
        markdownToc(pg.doc, doc)));
    }, (err) => {
      if (!ctx.dead) paint(main, 'error', () => h('div.card', empty(err.status === 404 ? 'No such page' : 'Could not open this page', err.status === 404 ? 'It may have been renamed or removed.' : err.message, btn('Front page', { href: wikiHref(brain, '') }))));
    });
  }, (err) => {
    if (!ctx.dead) mount(root, pageHeader('Wiki'), h('div.card', empty(err.status === 404 ? 'No such brain' : 'Could not read this wiki', err.message)));
  });
}

export default {
  title: 'Wiki',
  render(root, ctx) {
    if (ctx.params.brain) brainView(root, ctx, ctx.params.brain, ctx.params.page || '');
    else brainsView(root, ctx);
  },
};
