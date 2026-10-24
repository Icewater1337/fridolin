// A chat reference: the group's name when it is known, the JID otherwise.

import { h } from '../lib/dom.js';
import { isGroupJid, shortJid } from '../lib/jid.js';
import { icon } from './icons.js';

// The app registers where names come from (the WhatsApp groups it loaded),
// so this component does not depend on app state.
/** @type {(jid: string) => string} */
let nameOf = () => '';

/**
 * Sets the function chat labels use to look up a JID's name ('' when unknown).
 * @param {(jid: string) => string} fn
 */
export function setChatNames(fn) { nameOf = fn; }

/**
 * The name of a chat, '' when unknown.
 * @param {string} jid
 */
export function chatName(jid) {
  return nameOf(jid) || '';
}

/**
 * A chat reference: group name when known, the JID in monospace otherwise.
 * @param {string | null | undefined} jid
 * @param {{full?: boolean}} [options] full shows the whole JID
 */
export function chatEl(jid, { full = false } = {}) {
  if (!jid) return h('span.muted', '—');
  const name = chatName(jid);
  const icn = icon(isGroupJid(jid) ? 'users' : 'user', 'chat-icon');
  if (name) {
    return h('span.chat', { title: jid }, icn, h('span.chat-name', name));
  }
  return h('span.chat', { title: jid }, icn, h('span.mono', full ? jid : shortJid(jid)));
}
