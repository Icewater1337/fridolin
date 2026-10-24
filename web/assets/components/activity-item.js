// One message in an activity feed (Activity, Overview and brain pages),
// and the feed itself.

import { h, reconcile } from '../lib/dom.js';
import { isGroupJid, shortJid, senderLabel } from '../lib/jid.js';
import { icon } from './icons.js';
import { chatEl, chatName } from './chat.js';
import { timeAgo, longText } from './common.js';

/** @import { ActivityEntry } from '../../types/api.js' */

/**
 * How an activity row is shown: compact in short feeds, showBrain with a
 * link to its brain.
 * @typedef {{compact?: boolean, showBrain?: boolean}} ActivityOptions
 */

/**
 * One message row, or one event row (see eventItem).
 * @param {ActivityEntry} e
 * @param {ActivityOptions} [options]
 */
export function activityItem(e, { compact = false, showBrain = true } = {}) {
  if (e.type === 'event') return eventItem(e, { compact, showBrain });
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

/**
 * Something that happened to a chat's messages rather than a message: a
 * vote in a poll, an edit or a deletion of a message a run had read. Its
 * text says what the person did ("voted …", "deleted a message they had
 * sent"), so it reads after their name, muted, beside an icon of its own:
 * shown as a received message, it would look as if they had typed it.
 * @param {ActivityEntry} e
 * @param {ActivityOptions} options
 */
function eventItem(e, { compact, showBrain }) {
  return h(`article.act-item.event${compact ? '.compact' : ''}`,
    h('div.act-icon', { title: 'Event' }, icon('event')),
    h('div.act-body',
      h('div.act-meta',
        isGroupJid(e.chat) || senderLabel(e.from, e.chat) !== shortJid(e.chat) ? [h('span.act-sep', 'in'), chatEl(e.chat)] : h('span.act-sep', 'direct message'),
        showBrain && e.brain ? h('a.badge.neutral.act-brain', { href: `#/brains/${encodeURIComponent(e.brain)}`, title: 'Brain' }, e.brain) : null,
        h('span.act-time', timeAgo(e.ts))),
      h('div.msg-text.act-event',
        h('span.act-who', { title: e.from || '' }, senderLabel(e.from, e.chat)), ' ', e.text || '')));
}

const itemKey = (/** @type {ActivityEntry} */ e) => `${e.ts}|${e.chat}|${e.type}|${(e.text || '').length}`;

/**
 * Shows the messages in container, newest first as the bridge sends them,
 * with activityItem's options. A message already shown keeps its row, so
 * one that was unfolded stays unfolded; it is drawn again once its chat's
 * name is learnt.
 * @param {Element} container
 * @param {ActivityEntry[]} items
 * @param {ActivityOptions} [opts]
 */
export function showActivity(container, items, opts = {}) {
  const rows = items.map((e) => ({ ...e, chat_name: chatName(e.chat), opts }));
  reconcile(container, rows, itemKey, (e) => activityItem(e, e.opts));
}
