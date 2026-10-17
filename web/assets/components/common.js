// Small building blocks every page uses: status pills and badges, callouts,
// empty states, spinners, page headers, monospace values, folded long text
// and self-updating relative times.

import { h } from '../lib/dom.js';
import { parseTs, relTime, absTime } from '../lib/format.js';
import { icon } from './icons.js';
import { copyButton } from './buttons.js';

export function pill(text, kind = 'neutral', { title, pulse } = {}) {
  return h(`span.pill.${kind}`, { title }, h(`span.dot${pulse ? '.pulse' : ''}`), text);
}

export function badge(text, kind = 'neutral', title) {
  return h(`span.badge.${kind}`, { title }, text);
}

export function mono(text, { copy = false, title } = {}) {
  const el = h('code.mono', { title: title || text }, text);
  if (!copy) return el;
  return h('span.copyable', el, copyButton(text));
}

/** A spinner with a short text, on one line. */
export function loading(...text) {
  return h('div.loading-row', spinner(), h('span', text));
}

export function spinner(cls = '') {
  return h(`span.spinner${cls ? `.${cls}` : ''}`, { 'aria-hidden': 'true' });
}

export function empty(title, text, ...actions) {
  return h('div.empty', h('div.empty-title', title), text && h('p', text), actions.length ? h('div.actions', actions) : null);
}

export function callout(kind, title, ...body) {
  const icn = { warn: 'alert', bad: 'alert', ok: 'check', info: 'info' }[kind] || 'info';
  return h(`div.callout.${kind}`, { role: kind === 'bad' ? 'alert' : null }, icon(icn, 'callout-icon'), h('div.callout-body', title && h('strong', title), body.length ? h('div.callout-text', body) : null));
}

export function kv(rows) {
  const dl = h('dl.kv');
  for (const [k, v] of rows) {
    if (v === null || v === undefined || v === false) continue;
    dl.append(h('dt', k), h('dd', v));
  }
  return dl;
}

export function section(title, ...children) {
  return h('section.section', title ? h('h2.section-title', title) : null, children);
}

/** The page's h1, a subtitle (text or nodes) and buttons on the right. */
export function pageHeader(title, subtitle, ...actions) {
  return h('header.page-header',
    h('div.page-heading', h('h1', title), subtitle ? h('p.page-sub', subtitle) : null),
    actions.length ? h('div.page-actions', actions) : null);
}

/** Collapses long text behind a "more" toggle; keeps newlines. */
export function longText(text, { limit = 320, lines = 6, expanded, onToggle } = {}) {
  const t = String(text || '');
  const lineCount = t.split('\n').length;
  const long = t.length > limit || lineCount > lines;
  const body = h('div.msg-text', t);
  if (!long) return body;
  const wrap = h('div.msg-wrap', body);
  const more = h('button.link-btn', { type: 'button' });
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

/** <time> showing "3 min ago" with the full date in a tooltip; kept fresh by a ticker. */
export function timeAgo(ts, prefix = '') {
  const d = parseTs(ts);
  if (!d) return h('span.muted', 'never');
  return h('time.rel', { datetime: d.toISOString(), title: absTime(d), dataset: { ts: d.toISOString(), prefix } }, prefix + relTime(d));
}

/** Keeps every timeAgo() on the page current; the app starts it once. */
export function keepTimesFresh() {
  setInterval(() => {
    if (document.hidden) return;
    document.querySelectorAll('time.rel[data-ts]').forEach((el) => {
      el.textContent = (el.dataset.prefix || '') + relTime(el.dataset.ts);
    });
  }, 15000);
}
