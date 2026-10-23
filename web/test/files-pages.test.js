// The Files, File and Wiki pages, and the Markdown they show, on a stubbed
// bridge: what they list and link, and that nothing a brain wrote becomes
// markup, a script or a request elsewhere.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { matchRoute } from '../assets/app/router.js';
import { ROUTES } from '../assets/app/routes.js';
import { filesHref, rawHref, wikiHref } from '../assets/lib/files.js';
import { markdownNodes } from '../assets/components/markdown.js';
import filesPage from '../assets/pages/files.js';
import filePage from '../assets/pages/file.js';
import wikiPage from '../assets/pages/wiki.js';

const ROOTS = {
  roots: [
    { id: 'brain:main', kind: 'brain', brain: 'main', label: 'Main', path: '/Users/you/main', location: 'core', writable: false, backup: 'on', excluded: ['/work/'], online: true },
    { id: 'media:main', kind: 'media', brain: 'main', label: 'Main', path: '/data/media/main', location: 'core', writable: false, backup: 'on', online: true },
    { id: 'node:n1:0', kind: 'node', node: 'n1', label: '/srv/share', path: '/srv/share', location: 'box', writable: true, online: false },
  ],
};
const TICKET = { root: 'brain:main', ticket: '123.sig', expires: '2026-10-23T12:00:00Z' };
const DOC = {
  title: 'Alex', toc: [{ level: 1, text: 'Alex', id: 'alex' }, { level: 2, text: 'Role', id: 'role' }],
  body: [
    { t: 'h1', id: 'alex', c: [{ x: 'Alex' }] },
    { t: 'p', c: [{ x: '<script>alert(1)</script> ' }, { t: 'a', page: 'wiki/acme.md', c: [{ x: 'Acme' }] }, { x: ' ' }, { t: 'a', file: 'docs/plan.pdf', c: [{ x: 'plan' }] }] },
    { t: 'h2', id: 'role', c: [{ x: 'Role' }] },
    { t: 'p', c: [{ t: 'img', file: 'wiki/logo.png', alt: 'logo' }, { t: 'a', href: 'https://example.com/', c: [{ x: 'web' }] }, { t: 'a', page: 'nowhere', missing: true, c: [{ x: 'nowhere' }] }] },
  ],
};

async function open(t, page, params, route) {
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, route);
  const root = h('div');
  document.body.append(root);
  const ctx = createPage({ params });
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle(20);
  return { root, calls };
}

const hrefs = (el) => el.querySelectorAll('a').map((a) => a.getAttribute('href'));

test('the routes take a root and a path of any depth', () => {
  const m = matchRoute(ROUTES, '#/files/brain%3Amain/docs/2026/plan.pdf');
  assert.equal(m.route.id, 'files');
  assert.deepEqual(m.params, { root: 'brain:main', path: 'docs/2026/plan.pdf' });
  assert.deepEqual(matchRoute(ROUTES, '#/files').params, { path: '' });
  assert.equal(matchRoute(ROUTES, '#/file/brain%3Amain/a%20b.md').params.path, 'a b.md');
  assert.deepEqual(matchRoute(ROUTES, '#/wiki/main/wiki/people/alex.md').params, { brain: 'main', page: 'wiki/people/alex.md' });
  assert.equal(filesHref('brain:main', 'a b/c.md', false), '#/file/brain%3Amain/a%20b/c.md');
  assert.equal(wikiHref('main', 'wiki/x.md'), '#/wiki/main/wiki/x.md');
  assert.equal(rawHref('brain:main', 'a&b.png', 't.s', true), '/files/raw?root=brain%3Amain&path=a%26b.png&ticket=t.s&download=1');
});

test('Markdown becomes elements of the list and text, never markup', () => {
  installDom();
  const links = { page: (p) => `#page:${p}`, file: (p) => `#file:${p}`, image: (p) => `/img/${p}` };
  const nodes = markdownNodes([
    { t: 'script', c: [{ x: 'kept as text' }] },
    { t: 'p', c: [{ x: '<b>not bold</b>' }] },
    { t: 'a', href: 'javascript:alert(1)', c: [{ x: 'js' }] },
    { t: 'img', href: 'https://evil.example.com/x.png', alt: 'remote' },
    { t: 'li', checked: true, c: [{ x: 'done' }] },
  ], links);
  const box = h('div', nodes);
  assert.equal(box.querySelector('script'), null);
  assert.equal(box.querySelector('b'), null);
  assert.equal(box.textContent.includes('<b>not bold</b>'), true);
  assert.deepEqual(hrefs(box), [], 'a javascript: link stays text');
  assert.equal(box.querySelector('img'), null, 'an image from elsewhere is not loaded');
  assert.equal(box.querySelector('li input[type=checkbox]').checked, true);
});

test('Files lists the folders by kind, with what they are', async (t) => {
  const { root } = await open(t, filesPage, {}, (path) => (path === '/api/files/roots' ? ROOTS : { status: 404, body: { error: 'no' } }));
  const groups = root.querySelectorAll('.files-group h2').map((e) => e.textContent);
  assert.deepEqual(groups, ['Brain folders', 'Media inboxes', 'Node folders']);
  assert.deepEqual(hrefs(root).filter((x) => x.startsWith('#/files/')), ['#/files/brain%3Amain', '#/files/media%3Amain', '#/files/node%3An1%3A0']);
  assert.match(root.textContent, /Hidden, as the backup leaves them out: \/work\//);
  assert.match(root.textContent, /Offline/);
  assert.match(root.textContent, /On box/);
});

test('a folder lists its entries, folders and files each to their page', async (t) => {
  const { root, calls } = await open(t, filesPage, { root: 'brain:main', path: 'docs' }, (path) => {
    if (path === '/api/files/roots') return ROOTS;
    if (path.startsWith('/api/files/list')) {
      return { root: 'brain:main', path: 'docs', truncated: false, entries: [{ name: '2026', dir: true, size: 0, mod_time: '2026-10-01T00:00:00Z' }, { name: 'plan.pdf', size: 2048, kind: 'pdf', mod_time: '2026-10-02T00:00:00Z' }] };
    }
    return { status: 404, body: { error: 'no' } };
  });
  assert.ok(calls.some((c) => c.path === '/api/files/list?root=brain%3Amain&path=docs'));
  const links = hrefs(root);
  assert.ok(links.includes('#/files/brain%3Amain/docs/2026'));
  assert.ok(links.includes('#/file/brain%3Amain/docs/plan.pdf'));
  assert.ok(links.includes('#/files/brain%3Amain'), 'the breadcrumbs lead back');
  assert.match(root.textContent, /2\.0 KB|2 KB|2\.0 KiB/);
});

test('a Markdown file is rendered, its links and images through the bridge', async (t) => {
  const { root } = await open(t, filePage, { root: 'brain:main', path: 'wiki/people/alex.md' }, (path) => {
    if (path === '/api/files/roots') return ROOTS;
    if (path.startsWith('/api/files/view')) return { root: 'brain:main', path: 'wiki/people/alex.md', name: 'alex.md', size: 120, mod_time: '2026-10-02T00:00:00Z', kind: 'markdown', doc: DOC };
    if (path.startsWith('/api/files/ticket')) return TICKET;
    return { status: 404, body: { error: 'no' } };
  });
  assert.equal(root.querySelector('script'), null);
  assert.match(root.textContent, /<script>alert\(1\)<\/script>/);
  const links = hrefs(root);
  assert.ok(links.includes('#/wiki/main/wiki/acme.md'), 'a page of the wiki opens in the wiki');
  assert.ok(links.includes('#/file/brain%3Amain/docs/plan.pdf'));
  assert.equal(root.querySelectorAll('a[target=_blank]').length, 1, 'the one link to the web');
  assert.ok(links.includes('/files/raw?root=brain%3Amain&path=wiki%2Fpeople%2Falex.md&ticket=123.sig&download=1'));
  assert.equal(root.querySelector('img').getAttribute('src'), '/files/raw?root=brain%3Amain&path=wiki%2Flogo.png&ticket=123.sig');
  assert.equal(root.querySelector('a[target=_blank]').getAttribute('rel'), 'noopener noreferrer nofollow');
  assert.ok(root.querySelector('.md-missing'));
  assert.ok(root.querySelector('.md-toc'), 'the headings below the title make a table of contents');
});

test('a text file is text; a file Fridolin hides is not found', async (t) => {
  const { root } = await open(t, filePage, { root: 'brain:main', path: 'page.html' }, (path) => {
    if (path === '/api/files/roots') return ROOTS;
    if (path.startsWith('/api/files/view')) return { root: 'brain:main', path: 'page.html', name: 'page.html', size: 20, mod_time: '2026-10-02T00:00:00Z', kind: 'text', text: '<img src=x onerror=alert(1)>' };
    if (path.startsWith('/api/files/ticket')) return TICKET;
    return { status: 404, body: { error: 'no' } };
  });
  assert.equal(root.querySelector('img'), null);
  assert.equal(root.querySelector('pre.file-text').textContent, '<img src=x onerror=alert(1)>');

  const hidden = await open(t, filePage, { root: 'brain:main', path: '.env' }, (path) => (path === '/api/files/roots' ? ROOTS : { status: 404, body: { error: 'no such file or folder' } }));
  assert.match(hidden.root.textContent, /No such file/);
});

test('the wiki shows a brain’s pages, the page, its backlinks and search', async (t) => {
  const pages = {
    brain: 'main', label: 'Main', home: 'wiki/index.md', truncated: false,
    pages: [
      { path: 'wiki/index.md', name: 'index', title: 'Home', section: 'wiki' },
      { path: 'wiki/people/alex.md', name: 'people/alex', title: 'Alex', section: 'wiki' },
      { path: 'memories/README.md', name: 'README', title: 'Memory', section: 'memories' },
    ],
  };
  const { root, calls } = await open(t, wikiPage, { brain: 'main', page: 'wiki/people/alex.md' }, (path) => {
    if (path === '/api/wiki/main/pages') return pages;
    if (path.startsWith('/api/files/ticket')) return TICKET;
    if (path.startsWith('/api/wiki/main/page')) return { brain: 'main', page: pages.pages[1], doc: DOC, backlinks: [pages.pages[0]] };
    if (path.startsWith('/api/wiki/search')) return { hits: [{ brain: 'main', path: 'memories/README.md', title: 'Memory', section: 'memories', snippet: '…about Alex…' }] };
    return { status: 404, body: { error: 'no' } };
  });
  assert.ok(calls.some((c) => c.path === '/api/wiki/main/page?path=wiki%2Fpeople%2Falex.md'));
  assert.deepEqual(root.querySelectorAll('.wiki-tree h3').map((e) => e.textContent), ['people', 'Memories']);
  assert.equal(root.querySelector('.wiki-tree a[aria-current=page]').textContent, 'Alex');
  assert.deepEqual(root.querySelectorAll('.wiki-backlinks a').map((a) => a.getAttribute('href')), ['#/wiki/main/wiki/index.md']);

  const box = root.querySelector('.wiki-side input');
  box.value = 'alex';
  box.dispatchEvent(Object.assign(new Event('keydown'), { key: 'Enter' }));
  await settle(10);
  assert.ok(calls.some((c) => c.path === '/api/wiki/search?q=alex&brain=main'));
  assert.deepEqual(root.querySelectorAll('.wiki-hits a').map((a) => a.getAttribute('href')), ['#/wiki/main/memories/README.md']);
});

test('the wiki’s front page lists the brains that have one', async (t) => {
  const { root } = await open(t, wikiPage, {}, (path) => (path === '/api/wiki' ? { brains: [{ brain: 'main', label: 'Main', pages: 3, home: 'wiki/index.md' }] } : { status: 404, body: {} }));
  assert.ok(hrefs(root).includes('#/wiki/main'));
  assert.match(root.textContent, /3 pages/);
});
