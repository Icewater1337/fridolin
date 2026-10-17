// One message in an activity feed (Activity, Overview and brain pages),
// and the feed itself.

import { h, reconcile } from '../lib/dom.js';
import { isGroupJid, shortJid, senderLabel } from '../lib/jid.js';
import { icon } from './icons.js';
import { chatEl, chatName } from './chat.js';
import { timeAgo, longText } from './common.js';

/** One message row. */
export function activityItem(e, { compact = false, showBrain = true } = {}) {
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
      longText(e.text, { limit: compact ? 200 : 360, lines: compact ? 3 : 6 })));
}

const itemKey = (e) => `${e.ts}|${e.chat}|${e.type}|${(e.text || '').length}`;

/**
 * Shows the messages in container, newest first as the bridge sends them,
 * with activityItem's options. A message already shown keeps its row, so
 * one that was unfolded stays unfolded; it is drawn again once its chat's
 * name is learnt.
 */
export function showActivity(container, items, opts = {}) {
  const rows = items.map((e) => ({ ...e, chat_name: chatName(e.chat), opts }));
  reconcile(container, rows, itemKey, (e) => activityItem(e, e.opts));
}
