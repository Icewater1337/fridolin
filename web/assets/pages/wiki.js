// Wiki: each brain's wiki/ folder and memory notes as pages to read, with
// links between them, backlinks, a table of contents and search across a
// brain's pages or all of them (docs/FILES.md). #/wiki lists the brains;
// #/wiki/<brain> opens its front page; #/wiki/<brain>/<path> a page.
// You edit a page in place, its Markdown, and make new ones; each change
// is audited and committed where the brain's folder is a git repository,
// and listed under the page's History. Each page opened is written to the
// audit log.

import { get, qs } from '../app/api.js';
import { h, mount, paint } from '../lib/dom.js';
import { filesHref, rawHref, wikiHref } from '../lib/files.js';
import { btn } from '../components/buttons.js';
import { editOnOpen, historyBox, newFile, takeEditOnOpen, textEditor } from '../components/file-actions.js';
import { empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { textInput } from '../components/fields.js';
import { icon } from '../components/icons.js';
import { markdownDoc, markdownToc } from '../components/markdown.js';

/** @import { Page, PageModule } from '../app/page.js' */
/** @import { WikiHit, WikiPageInfo } from '../../types/api.js' */

/**
 * @param {WikiHit[]} list
 * @param {boolean} oneBrain
 */
function hits(list, oneBrain) {
  if (!list.length) return h('div.card', empty('Nothing found', 'No page has all of these words.'));
  return h('ul.wiki-hits', list.map((x) => h('li',
    h('a', { href: wikiHref(x.brain, x.path) }, x.title),
    oneBrain ? null : h('span.muted.small', ` · ${x.brain}`),
    h('p.muted.small', x.snippet))));
}

/**
 * A search box; results go into out.
 * @param {Page} ctx
 * @param {HTMLElement} out
 * @param {string} brain
 */
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

/**
 * @param {HTMLElement} root
 * @param {Page} ctx
 */
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

/**
 * The pages, by folder: the wiki's own, then the memory notes.
 * @param {string} brain
 * @param {WikiPageInfo[]} pages
 * @param {string} current
 */
function pageTree(brain, pages, current) {
  /** @type {Map<string, WikiPageInfo[]>} */
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

/**
 * @param {HTMLElement} root
 * @param {Page} ctx
 * @param {string} brain
 * @param {string} page
 */
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
      const rootId = `brain:${brain}`;
      const edit = async () => {
        const v = await get(`/api/files/view${qs({ root: rootId, path, source: '1' })}`, { signal: ctx.signal });
        if (!v.editable) throw new Error('This page cannot be edited here.');
        const editor = textEditor({
          rootId, path, text: v.source, sha: v.sha,
          onSaved: () => { ctx.dirty = null; brainView(root, ctx, brain, page); },
          onCancel: () => { ctx.dirty = null; brainView(root, ctx, brain, page); },
        });
        ctx.dirty = () => editor.dirty();
        paint(main, `edit:${path}`, () => h('div', pageHeader(`Editing ${title}`, path), editor));
        editor.focus();
      };
      const tools = pg.editable ? h('div.toolbar.wiki-tools', h('div.toolbar-group',
        btn('Edit', { icon: 'edit', small: true, onClick: edit }),
        btn('New page', {
          icon: 'plus', small: true,
          onClick: async () => {
            const folder = path.startsWith('memories/') ? 'memories' : 'wiki';
            const p = await newFile(rootId, folder, { placeholder: 'people/alex.md' });
            if (p) { editOnOpen(rootId, p); ctx.navigate(wikiHref(brain, p)); }
          },
        }))) : null;
      if (pg.editable && takeEditOnOpen(rootId, path)) { edit(); return; }
      paint(main, `page:${path}:${pg.page.mod_time}`, () => h('div.md-layout',
        h('div',
          tools,
          h('p.muted.small.wiki-meta', pg.page.section === 'memories' ? 'Memory note' : 'Wiki', ' · ', timeAgo(pg.page.mod_time, 'changed '), ' · ',
            h('a', { href: filesHref(`brain:${brain}`, path, false) }, path)),
          hasH1 ? null : h('h1.wiki-title', title),
          pg.doc.tags && pg.doc.tags.length ? h('div.wiki-tags', pg.doc.tags.map((t) => h('span.badge.neutral', t))) : null,
          h('div.card', doc),
          pg.backlinks.length ? h('section.wiki-backlinks', h('h3', 'Linked from'),
            h('ul', pg.backlinks.map((b) => h('li', h('a', { href: wikiHref(brain, b.path) }, b.title))))) : null,
          pg.editable ? historyBox(ctx, rootId, path) : null),
        markdownToc(pg.doc, doc)));
    }, (err) => {
      if (!ctx.dead) paint(main, 'error', () => h('div.card', empty(err.status === 404 ? 'No such page' : 'Could not open this page', err.status === 404 ? 'It may have been renamed or removed.' : err.message, btn('Front page', { href: wikiHref(brain, '') }))));
    });
  }, (err) => {
    if (!ctx.dead) mount(root, pageHeader('Wiki'), h('div.card', empty(err.status === 404 ? 'No such brain' : 'Could not read this wiki', err.message)));
  });
}

export default /** @satisfies {PageModule} */ ({
  title: 'Wiki',
  render(root, ctx) {
    if (ctx.params.brain) brainView(root, ctx, ctx.params.brain, ctx.params.page || '');
    else brainsView(root, ctx);
  },
});
