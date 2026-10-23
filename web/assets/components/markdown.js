// A brain's Markdown, as the bridge renders it: a tree of a few element
// names and texts (internal/wiki), built here with h(). Nothing in it can
// become markup or script: texts are text nodes, element names come from
// a fixed list, a link goes to the web only by an http, https or mailto
// address the bridge checked, and an image only loads from the brain's own
// folder through the bridge.

import { h } from '../lib/dom.js';

const TAGS = new Set(['p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li', 'blockquote', 'pre', 'code', 'em', 'strong', 'del', 'a', 'img', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'hr', 'br']);
const WEB = /^(https?:|mailto:)/i;

/**
 * markdownNodes(nodes, links) builds the tree. links says where things go:
 * page(path, anchor) and file(path) return a dashboard address (#/…),
 * image(path) the address an image loads from.
 */
export function markdownNodes(nodes, links) {
  const out = [];
  for (const n of nodes || []) {
    if (!n.t) { out.push(n.x || ''); continue; }
    if (!TAGS.has(n.t)) { out.push(...markdownNodes(n.c, links)); continue; }
    if (n.t === 'img') {
      out.push(n.file ? h('img', { src: links.image(n.file), alt: n.alt || '', title: n.title || null, loading: 'lazy' }) : (n.alt || ''));
      continue;
    }
    if (n.t === 'a') {
      const kids = markdownNodes(n.c, links);
      if (n.href && WEB.test(n.href)) out.push(h('a', { href: n.href, target: '_blank', rel: 'noopener noreferrer nofollow', title: n.title || null }, kids));
      else if (n.missing) out.push(h('span.md-missing', { title: `No page “${n.page}” yet` }, kids));
      else if (n.page) out.push(h('a', { href: links.page(n.page, n.anchor), title: n.title || null }, kids));
      else if (n.file) out.push(h('a', { href: links.file(n.file), title: n.title || null }, kids));
      else if (n.anchor) out.push(h('a', { href: '#', onclick: (e) => { e.preventDefault(); scrollToAnchor(e.currentTarget, n.anchor); } }, kids));
      else out.push(...kids);
      continue;
    }
    const el = h(n.t, {
      dataset: n.id ? { anchor: n.id } : null,
      start: n.start || null,
      class: [n.align ? `md-${n.align}` : '', n.checked !== undefined ? 'md-task' : ''].filter(Boolean).join(' ') || null,
    });
    if (n.checked !== undefined) el.append(h('input', { type: 'checkbox', disabled: true, checked: Boolean(n.checked), 'aria-label': n.checked ? 'Done' : 'Not done' }), ' ');
    el.append(...markdownNodes(n.c, links).map((c) => (typeof c === 'string' ? document.createTextNode(c) : c)));
    out.push(el);
  }
  return out;
}

/** Scrolls to the heading anchor names in the document from's in. */
function scrollToAnchor(from, anchor) {
  let top = from;
  while (top.parentNode && !top.classList?.contains('md-doc')) top = top.parentNode;
  const target = top.querySelector && top.querySelector(`[data-anchor="${String(anchor).replace(/"/g, '')}"]`);
  if (target) target.scrollIntoView({ block: 'start' });
}

/** The article for a rendered page. */
export function markdownDoc(doc, links) {
  return h('article.md-doc', markdownNodes(doc.body, links));
}

/** The table of contents of a rendered page (its headings below the title), or null. */
export function markdownToc(doc, docEl) {
  const items = (doc.toc || []).filter((t) => t.level > 1);
  if (!items.length) return null;
  return h('nav.md-toc', { 'aria-label': 'On this page' }, h('h3', 'On this page'),
    h('ul', items.map((t) => h(`li.md-l${Math.min(t.level, 4)}`, h('a', {
      href: '#',
      onclick: (e) => { e.preventDefault(); scrollToAnchor(docEl, t.id); },
    }, t.text)))));
}
