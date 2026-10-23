// The site's built-in Files and Wiki (docs/FILES.md): the brain folders
// and wikis the signed-in account's role may see. Everything shown comes
// from /_fridolin/api/, which the site passes to Fridolin. A page's
// Markdown arrives as a tree of a few element names and texts, built here
// with DOM calls only: nothing a brain wrote can become markup or script.

const app = document.getElementById('app');

// h('p', {class: 'x'}, 'text', child) — the attributes may be left out.
function h(tag, attrs, ...children) {
  if (attrs === null || attrs === undefined || typeof attrs !== 'object' || attrs instanceof Node || Array.isArray(attrs)) {
    if (attrs !== null && attrs !== undefined) children.unshift(attrs);
    attrs = {};
  }
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'value') el.value = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    // Strings always become text nodes. The typeof test is what CodeQL
    // takes as proof that no string reaches appendChild (as in the
    // dashboard's lib/dom.js).
    if (typeof c === 'object' && c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
  return el;
}

async function api(path, params) {
  const qs = params ? `?${new URLSearchParams(params)}` : '';
  const r = await fetch(`/_fridolin/api/${path}${qs}`, { credentials: 'same-origin', cache: 'no-store' });
  let body = null;
  try { body = await r.json(); } catch { /* empty */ }
  if (r.status === 401) { location.assign(`/?next=${encodeURIComponent(location.pathname + location.search)}`); throw new Error('Sign in first'); }
  if (!r.ok) throw new Error((body && body.error) || `Error ${r.status}`);
  return body;
}

const rawURL = (root, path, download) =>
  `/_fridolin/api/files/raw?${new URLSearchParams({ root, path, ...(download ? { download: '1' } : {}) })}`;

const enc = (p) => p.split('/').map(encodeURIComponent).join('/');
const dec = (p) => p.split('/').map((s) => { try { return decodeURIComponent(s); } catch { return s; } }).join('/');

function fmtBytes(n) {
  if (n < 1024) return `${n} B`;
  const u = ['KB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return `${n.toFixed(n < 10 ? 1 : 0)} ${u[i]}`;
}
const fmtDate = (s) => new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });

// ── navigation ────────────────────────────────────────────────────────────

function go(href) {
  history.pushState(null, '', href);
  render();
}
// A link inside the app: plain clicks stay on the page.
function link(href, attrs, ...children) {
  return h('a', {
    href,
    ...attrs,
    onclick: (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      go(href);
    },
  }, ...children);
}
window.addEventListener('popstate', render);

let site = { title: 'Site' };
let me = null;

function frame(section, ...body) {
  const nav = h('nav', { class: 'bnav' },
    link('/', {}, 'Home'),
    link('/files', { 'aria-current': section === 'files' ? 'page' : null }, 'Files'),
    link('/wiki', { 'aria-current': section === 'wiki' ? 'page' : null }, 'Wiki'));
  app.className = '';
  app.replaceChildren(
    h('header', { class: 'topbar' }, h('h1', {}, link('/', { class: 'home-link' }, site.title)), nav,
      h('div', { class: 'who' }, h('span', me ? (me.name || me.email) : ''))),
    h('div', { class: 'content wide' }, ...body));
}

function failure(err) {
  return h('div', { class: 'empty', role: 'alert' }, err.message || String(err));
}

// ── Markdown tree ─────────────────────────────────────────────────────────

const TAGS = new Set(['p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li', 'blockquote', 'pre', 'code', 'em', 'strong', 'del', 'a', 'img', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'hr', 'br']);

// renderDoc builds nodes; ctx says where a page or file link goes and
// where an image loads from.
function renderDoc(nodes, ctx) {
  const out = [];
  for (const n of nodes || []) {
    if (!n.t) { out.push(document.createTextNode(n.x || '')); continue; }
    if (!TAGS.has(n.t)) { out.push(...renderDoc(n.c, ctx)); continue; }
    if (n.t === 'img') {
      out.push(n.file ? h('img', { src: ctx.image(n.file), alt: n.alt || '', title: n.title || null, loading: 'lazy' }) : document.createTextNode(n.alt || ''));
      continue;
    }
    if (n.t === 'a') {
      const kids = renderDoc(n.c, ctx);
      if (n.href) out.push(h('a', { href: n.href, target: '_blank', rel: 'noopener noreferrer nofollow', title: n.title || null }, kids));
      else if (n.missing) out.push(h('span', { class: 'missing', title: `No page “${n.page}” yet` }, kids));
      else if (n.page) out.push(link(ctx.page(n.page, n.anchor), { title: n.title || null }, kids));
      else if (n.file) out.push(link(ctx.file(n.file), { title: n.title || null }, kids));
      else if (n.anchor) out.push(h('a', { href: `#${encodeURIComponent(n.anchor)}` }, kids));
      else out.push(...kids);
      continue;
    }
    const el = h(n.t, { id: n.id || null, start: n.start || null, 'data-lang': n.lang || null, class: n.align ? `align-${n.align}` : null });
    if (n.checked !== undefined) {
      el.classList.add('task');
      el.append(h('input', { type: 'checkbox', disabled: true, checked: n.checked || null, 'aria-label': n.checked ? 'Done' : 'Not done' }), ' ');
    }
    el.append(...renderDoc(n.c, ctx));
    out.push(el);
  }
  return out;
}

// ── Files ─────────────────────────────────────────────────────────────────

// /files/<brain>/<path>: a trailing slash is a folder.
function filesHref(brain, path, dir) {
  const p = path ? `${enc(path)}${dir ? '/' : ''}` : '';
  return `/files/${encodeURIComponent(brain)}/${p}`;
}

async function filesPage(rest) {
  if (!rest) {
    const { roots } = await api('files/roots');
    if (roots.length === 1) { history.replaceState(null, '', filesHref(roots[0].brain, '', true)); return render(); }
    frame('files', h('h2', {}, 'Files'),
      roots.length ? h('div', { class: 'tiles' }, roots.map((r) => link(filesHref(r.brain, '', true), { class: 'tile' },
        h('span', { class: 'tile-icon', 'aria-hidden': 'true' }, '📁'), h('div', {}, h('h3', {}, r.label), h('p', {}, r.brain)))))
        : h('div', { class: 'empty' }, 'No folders are shared with you.'));
    return;
  }
  const slash = rest.indexOf('/');
  const brain = dec(slash < 0 ? rest : rest.slice(0, slash));
  const tail = slash < 0 ? '' : rest.slice(slash + 1);
  const isDir = tail === '' || tail.endsWith('/');
  const path = dec(tail.replace(/\/$/, ''));
  const root = `brain:${brain}`;
  const crumbs = h('nav', { class: 'crumbs', 'aria-label': 'Folder' }, link('/files', {}, 'Files'), ' / ', link(filesHref(brain, '', true), {}, brain));
  let acc = '';
  const parts = path ? path.split('/') : [];
  parts.forEach((seg, i) => {
    acc = acc ? `${acc}/${seg}` : seg;
    crumbs.append(' / ', i === parts.length - 1 ? h('span', {}, seg) : link(filesHref(brain, acc, true), {}, seg));
  });
  if (isDir) return folderView(brain, root, path, crumbs);
  return fileView(brain, root, path, crumbs);
}

async function folderView(brain, root, path, crumbs) {
  const params = new URLSearchParams(location.search);
  const q = params.get('q') || '';
  const sort = params.get('sort') || 'name';
  const list = q ? await api('files/search', { root, path, q }) : await api('files/list', { root, path });
  let rows = q ? list.hits.map((x) => ({ ...x, at: x.path })) : list.entries.map((e) => ({ ...e, at: path ? `${path}/${e.name}` : e.name }));
  if (sort === 'date') rows = rows.sort((a, b) => (b.dir - a.dir) || b.mod_time.localeCompare(a.mod_time));
  if (sort === 'size') rows = rows.sort((a, b) => (b.dir - a.dir) || (b.size - a.size));
  const search = h('input', { type: 'search', name: 'q', value: q, placeholder: 'Find by name below this folder', 'aria-label': 'Find by name' });
  const sortSel = h('select', { name: 'sort', 'aria-label': 'Sort', onchange: () => form.requestSubmit() },
    ['name', 'date', 'size'].map((s) => h('option', { value: s, selected: s === sort || null }, `Sort by ${s}`)));
  const form = h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      const next = new URLSearchParams();
      if (search.value.trim()) next.set('q', search.value.trim());
      if (sortSel.value !== 'name') next.set('sort', sortSel.value);
      go(`${location.pathname}${next.size ? `?${next}` : ''}`);
    },
  }, search, sortSel, h('button', { type: 'submit' }, 'Find'));
  const table = rows.length ? h('table', { class: 'files' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', { class: 'num' }, 'Size'), h('th', {}, 'Modified'))),
    h('tbody', {}, rows.map((e) => h('tr', {},
      h('td', {}, link(filesHref(brain, e.at, e.dir), {}, e.dir ? '📁 ' : '📄 ', q ? e.at : e.name)),
      h('td', { class: 'num' }, e.dir ? '' : fmtBytes(e.size)),
      h('td', {}, fmtDate(e.mod_time))))))
    : h('div', { class: 'empty' }, q ? 'Nothing found.' : 'This folder is empty.');
  frame('files', crumbs, form, table, (list.truncated ? h('p', { class: 'muted' }, 'Only the first entries are shown.') : null));
}

async function fileView(brain, root, path, crumbs) {
  const v = await api('files/view', { root, path });
  const ctx = {
    page: (p) => filesHref(brain, p, false),
    file: (p) => filesHref(brain, p, false),
    image: (p) => rawURL(root, p),
  };
  let body;
  switch (v.kind) {
    case 'markdown': body = h('article', { class: 'doc' }, renderDoc(v.doc.body, ctx)); break;
    case 'text': body = h('pre', { class: 'text' }, v.text); break;
    case 'image': body = h('div', { class: 'preview-media' }, h('img', { src: rawURL(root, path), alt: v.name })); break;
    case 'audio': body = h('audio', { controls: true, preload: 'metadata', src: rawURL(root, path) }); break;
    case 'video': body = h('video', { controls: true, preload: 'metadata', src: rawURL(root, path), class: 'preview-media' }); break;
    case 'pdf': body = h('p', {}, h('a', { class: 'button primary', href: rawURL(root, path), target: '_blank', rel: 'noopener' }, 'Open the PDF')); break;
    default: body = h('div', { class: 'empty' }, 'No preview for this kind of file. Download it to open it.');
  }
  const copy = h('button', {
    type: 'button',
    class: 'small',
    onclick: async () => {
      try { await navigator.clipboard.writeText(location.href); copy.textContent = 'Copied'; } catch { copy.textContent = 'Copy failed'; }
    },
  }, 'Copy link');
  frame('files', crumbs,
    h('div', { class: 'filehead' },
      h('div', {}, h('h2', {}, v.name), h('p', { class: 'muted' }, `${fmtBytes(v.size)}${v.mod_time && !v.mod_time.startsWith('0001') ? ` · ${fmtDate(v.mod_time)}` : ''}`)),
      h('div', { class: 'actions' }, copy, h('a', { class: 'button small', href: rawURL(root, path, true) }, 'Download'))),
    v.truncated ? h('p', { class: 'muted' }, 'Only the start of this file is shown; download it for all of it.') : null,
    body);
}

// ── Wiki ──────────────────────────────────────────────────────────────────

// A page's address: wiki/a/b.md is /wiki/<brain>/a/b, memories/x.md is
// /wiki/<brain>/_memories/x.
function pageHref(brain, path, anchor) {
  let name;
  if (path.startsWith('wiki/')) name = path.slice(5);
  else if (path.startsWith('memories/')) name = `_memories/${path.slice(9)}`;
  else return filesHref(brain, path, false);
  name = name.replace(/\.(md|markdown)$/i, '');
  return `/wiki/${encodeURIComponent(brain)}/${enc(name)}${anchor ? `#${encodeURIComponent(anchor)}` : ''}`;
}
function pathOf(name) {
  if (name.startsWith('_memories/')) return `memories/${name.slice(10)}.md`;
  return `wiki/${name}.md`;
}

function searchBox(brain, q) {
  const input = h('input', { type: 'search', value: q || '', placeholder: brain ? 'Search this wiki' : 'Search every wiki', 'aria-label': 'Search' });
  return h('form', {
    class: 'toolbar',
    onsubmit: (e) => {
      e.preventDefault();
      const v = input.value.trim();
      go(`${brain ? `/wiki/${encodeURIComponent(brain)}` : '/wiki'}${v ? `?q=${encodeURIComponent(v)}` : ''}`);
    },
  }, input, h('button', { type: 'submit' }, 'Search'));
}

async function searchResults(brain, q) {
  const { hits } = await api('wiki/search', brain ? { q, brain } : { q });
  return hits.length ? h('ul', { class: 'hits' }, hits.map((x) => h('li', {},
    link(pageHref(x.brain, x.path), {}, x.title), brain ? null : h('span', { class: 'muted' }, ` · ${x.brain}`),
    h('p', { class: 'muted' }, x.snippet))))
    : h('div', { class: 'empty' }, 'No page has all of these words.');
}

function pageTree(brain, pages, current) {
  const groups = new Map();
  for (const p of pages) {
    const folder = p.section === 'memories' ? 'Memories' : (p.name.includes('/') ? p.name.slice(0, p.name.lastIndexOf('/')) : '');
    if (!groups.has(folder)) groups.set(folder, []);
    groups.get(folder).push(p);
  }
  return h('nav', { class: 'tree', 'aria-label': 'Pages' }, [...groups].map(([folder, list]) => h('div', {},
    folder ? h('h4', {}, folder) : null,
    h('ul', {}, list.map((p) => h('li', {}, link(pageHref(brain, p.path), { 'aria-current': p.path === current ? 'page' : null }, p.title)))))));
}

async function wikiPage(rest) {
  const q = new URLSearchParams(location.search).get('q') || '';
  if (!rest) {
    const { brains } = await api('wiki');
    if (brains.length === 1 && !q) { history.replaceState(null, '', `/wiki/${encodeURIComponent(brains[0].brain)}`); return render(); }
    frame('wiki', h('h2', {}, 'Wiki'), searchBox('', q),
      q ? await searchResults('', q)
        : brains.length ? h('div', { class: 'tiles' }, brains.map((b) => link(`/wiki/${encodeURIComponent(b.brain)}`, { class: 'tile' },
          h('span', { class: 'tile-icon', 'aria-hidden': 'true' }, '📚'), h('div', {}, h('h3', {}, b.label), h('p', {}, `${b.pages} pages`)))))
          : h('div', { class: 'empty' }, 'No wiki is shared with you.'));
    return;
  }
  const slash = rest.indexOf('/');
  const brain = dec(slash < 0 ? rest : rest.slice(0, slash));
  const name = slash < 0 ? '' : dec(rest.slice(slash + 1));
  const idx = await api(`wiki/${encodeURIComponent(brain)}/pages`);
  const side = h('aside', { class: 'wiki-side' }, searchBox(brain, q), pageTree(brain, idx.pages, name ? pathOf(name) : idx.home));
  if (q) { frame('wiki', h('div', { class: 'wiki' }, side, h('div', { class: 'wiki-main' }, h('h2', {}, `Search: ${q}`), await searchResults(brain, q)))); return; }
  const path = name ? pathOf(name) : idx.home;
  if (!path) {
    frame('wiki', h('div', { class: 'wiki' }, side, h('div', { class: 'wiki-main' }, h('h2', {}, idx.label),
      h('p', { class: 'muted' }, `${idx.pages.length} pages. Pick one on the left.`))));
    return;
  }
  const pg = await api(`wiki/${encodeURIComponent(brain)}/page`, { path });
  const root = `brain:${brain}`;
  const ctx = {
    page: (p, a) => pageHref(brain, p, a),
    file: (p) => filesHref(brain, p, false),
    image: (p) => rawURL(root, p),
  };
  const toc = pg.doc.toc.filter((t) => t.level > 1);
  document.title = `${pg.doc.title || pg.page.title} · ${site.title}`;
  frame('wiki', h('div', { class: 'wiki' }, side,
    h('div', { class: 'wiki-main' },
      h('p', { class: 'muted small' }, `${idx.label} · ${pg.page.section === 'memories' ? 'Memory note' : 'Wiki'} · edited ${fmtDate(pg.page.mod_time)}`),
      pg.doc.title && !pg.doc.body.some((n) => n.t === 'h1') ? h('h1', {}, pg.doc.title) : null,
      pg.doc.tags && pg.doc.tags.length ? h('p', { class: 'tags' }, pg.doc.tags.map((t) => h('span', { class: 'tag' }, t))) : null,
      h('article', { class: 'doc' }, renderDoc(pg.doc.body, ctx)),
      pg.backlinks.length ? h('section', { class: 'backlinks' }, h('h3', {}, 'Linked from'),
        h('ul', {}, pg.backlinks.map((b) => h('li', {}, link(pageHref(brain, b.path), {}, b.title))))) : null),
    toc.length ? h('nav', { class: 'toc', 'aria-label': 'On this page' }, h('h4', {}, 'On this page'),
      h('ul', {}, toc.map((t) => h('li', { class: `l${t.level}` }, h('a', { href: `#${encodeURIComponent(t.id)}` }, t.text))))) : null));
  if (location.hash) document.getElementById(decodeURIComponent(location.hash.slice(1)))?.scrollIntoView();
}

// ── start ─────────────────────────────────────────────────────────────────

async function render() {
  const [, section, ...rest] = location.pathname.split('/');
  const tail = rest.join('/');
  document.title = `${section === 'files' ? 'Files' : 'Wiki'} · ${site.title}`;
  try {
    if (section === 'files') await filesPage(tail);
    else await wikiPage(tail);
  } catch (err) {
    frame(section, failure(err));
  }
  if (!location.hash) window.scrollTo(0, 0);
}

(async () => {
  try {
    const s = await fetch('/_fridolin/site.json', { cache: 'no-store' });
    if (s.ok) site = await s.json();
    const m = await fetch('/auth/me', { cache: 'no-store' });
    if (!m.ok) { location.assign(`/?next=${encodeURIComponent(location.pathname + location.search)}`); return; }
    me = await m.json();
  } catch { /* shown below */ }
  render();
})();
