// One message in an activity feed (Activity, Overview and brain pages).

import { h } from '../lib/dom.js';
import { isGroupJid, shortJid, senderLabel } from '../lib/jid.js';
import { icon } from './icons.js';
import { chatEl } from './chat.js';
import { timeAgo, longText } from './common.js';

// Which long messages are unfolded, so a repaint keeps them open.
const expandedKeys = new Set();

/** One message row. */
export function activityItem(e, { compact = false, showBrain = true } = {}) {
  const key = `${e.ts}|${e.chat}|${e.type}|${(e.text || '').length}`;
  const out = e.type === 'out';
  const who = out
    ? h('span.act-who', 'Reply')
    : h('span.act-who', { title: e.from || '' }, senderLabel(e.from, e.chat));
  return h(`article.act-item.${out ? 'out' : 'in'}${compact ? '.compact' : ''}`,
    h('div.act-icon', { title: out ? 'Sent' : 'Received' }, icon(out ? 'out' : 'in')),
    h('div.act-body',
      h('div.act-meta',
        who,
        isGroupJid(e.chat) || out || senderLabel(e.from, e.chat) !== shortJid(e.chat) ? [h('span.act-sep', out ? 'to' : 'in'), chatEl(e.chat)] : h('span.act-sep', 'direct message'),
        showBrain && e.brain ? h('a.badge.neutral.act-brain', { href: `#/brains/${encodeURIComponent(e.brain)}`, title: 'Brain' }, e.brain) : null,
        h('span.act-time', timeAgo(e.ts))),
      longText(e.text, {
        limit: compact ? 200 : 360,
        lines: compact ? 3 : 6,
        expanded: expandedKeys.has(key),
        onToggle: (open) => { if (open) expandedKeys.add(key); else expandedKeys.delete(key); },
      })));
}
