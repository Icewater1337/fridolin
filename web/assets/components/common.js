// Small building blocks every page uses: status pills and badges, callouts,
// empty states, spinners, page headers, monospace values, folded long text
// and self-updating relative times.

import { h } from '../lib/dom.js';
import { parseTs, relTime, absTime } from '../lib/format.js';
import { icon } from './icons.js';
import { copyButton } from './buttons.js';

/** @import { Attrs, Child } from '../lib/dom.js' */
/** @import { Timestamp } from '../lib/format.js' */

/**
 * A status pill: a dot and text, of kind ok, warn, bad, info or neutral.
 * @param {Child} text
 * @param {string} [kind]
 * @param {{title?: string, pulse?: boolean}} [options]
 */
export function pill(text, kind = 'neutral', { title, pulse } = {}) {
  return h(`span.pill.${kind}`, { title }, h(`span.dot${pulse ? '.pulse' : ''}`), text);
}

/**
 * A small badge of kind ok, warn, bad, info or neutral.
 * @param {Child} text
 * @param {string} [kind]
 * @param {string} [title]
 */
export function badge(text, kind = 'neutral', title) {
  return h(`span.badge.${kind}`, { title }, text);
}

/**
 * text in monospace, with a copy button when copy is set.
 * @param {string} text
 * @param {{copy?: boolean, title?: string}} [options]
 */
export function mono(text, { copy = false, title } = {}) {
  const el = h('code.mono', { title: title || text }, text);
  if (!copy) return el;
  return h('span.copyable', el, copyButton(text));
}

/**
 * A spinner with a short text, on one line.
 * @param {...Child} text
 */
export function loading(...text) {
  return h('div.loading-row', spinner(), h('span', text));
}

/** @param {string} [cls] more classes, dot-separated */
export function spinner(cls = '') {
  return h(`span.spinner${cls ? `.${cls}` : ''}`, { 'aria-hidden': 'true' });
}

/**
 * An empty state: a title, a line of text and buttons.
 * @param {Child} title
 * @param {Child} [text]
 * @param {...Child} actions
 */
export function empty(title, text, ...actions) {
  return h('div.empty', h('div.empty-title', title), text && h('p', text), actions.length ? h('div.actions', actions) : null);
}

/**
 * callout(kind, title, ...body): a note of kind info, ok, warn or bad, with
 * a title in bold (none when null) above the body. When the first item of
 * body is a plain object, not a node, an array or null, it holds the
 * callout's attributes as h() takes them, e.g. {class, hidden}.
 * @param {'info' | 'ok' | 'warn' | 'bad'} kind
 * @param {Child} title
 * @param {...(Child | Attrs)} body
 */
export function callout(kind, title, ...body) {
  const first = body[0];
  const attrs = first && typeof first === 'object' && !(first instanceof Node) && !Array.isArray(first) ? /** @type {Attrs} */ (body.shift()) : {};
  const text = /** @type {Child[]} */ (body);
  const icn = { warn: 'alert', bad: 'alert', ok: 'check', info: 'info' }[kind] || 'info';
  return h(`div.callout.${kind}`, { role: kind === 'bad' ? 'alert' : null, ...attrs }, icon(icn, 'callout-icon'), h('div.callout-body', title && h('strong', title), text.length ? h('div.callout-text', text) : null));
}

/**
 * A list of [term, value] pairs; a pair whose value is null, undefined or
 * false is left out.
 * @param {readonly (readonly [Child, Child])[]} rows
 */
export function kv(rows) {
  const dl = h('dl.kv');
  for (const [k, v] of rows) {
    if (v === null || v === undefined || v === false) continue;
    dl.append(h('dt', k), h('dd', v));
  }
  return dl;
}

/**
 * The page's h1, a subtitle (text or nodes) and buttons on the right.
 * @param {Child} title
 * @param {Child} [subtitle]
 * @param {...Child} actions
 */
export function pageHeader(title, subtitle, ...actions) {
  return h('header.page-header',
    h('div.page-heading', h('h1', title), subtitle ? h('p.page-sub', subtitle) : null),
    actions.length ? h('div.page-actions', actions) : null);
}

/**
 * Collapses long text behind a "more" toggle; keeps newlines.
 * @param {unknown} text
 * @param {{limit?: number, lines?: number, expanded?: boolean, onToggle?: (open: boolean) => void}} [options]
 */
export function longText(text, { limit = 320, lines = 6, expanded, onToggle } = {}) {
  const t = String(text || '');
  const lineCount = t.split('\n').length;
  const long = t.length > limit || lineCount > lines;
  const body = h('div.msg-text', t);
  if (!long) return body;
  const wrap = h('div.msg-wrap', body);
  const more = h('button.link-btn', { type: 'button' });
  /** @param {boolean} open */
  const set = (open) => {
    wrap.classList.toggle('collapsed', !open);
    more.textContent = open ? 'less' : 'more';
    more.setAttribute('aria-expanded', String(open));
  };
  more.addEventListener('click', () => {
    const open = wrap.classList.contains('collapsed');
    set(open);
    if (onToggle) onToggle(open);
  });
  set(!!expanded);
  wrap.appendChild(more);
  return wrap;
}

/**
 * <time> showing "3 min ago" with the full date in a tooltip; kept fresh by a ticker.
 * @param {Timestamp} ts
 * @param {string} [prefix]
 */
export function timeAgo(ts, prefix = '') {
  const d = parseTs(ts);
  if (!d) return h('span.muted', 'never');
  return h('time.rel', { datetime: d.toISOString(), title: absTime(d), dataset: { ts: d.toISOString(), prefix } }, prefix + relTime(d));
}

/** Keeps every timeAgo() on the page current; the app starts it once. */
export function keepTimesFresh() {
  setInterval(() => {
    if (document.hidden) return;
    document.querySelectorAll('time.rel[data-ts]').forEach((node) => {
      const el = /** @type {HTMLElement} */ (node);
      el.textContent = (el.dataset.prefix || '') + relTime(el.dataset.ts);
    });
  }, 15000);
}
