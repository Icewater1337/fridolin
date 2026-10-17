// WhatsApp ids and phone numbers, shortened for people to read.

export function isGroupJid(jid) { return typeof jid === 'string' && jid.endsWith('@g.us'); }

/**
 * A group JID as typed in to be routed or allowed: digits (with an
 * old-style group's -suffix) and @g.us. The bridge checks the allow lists
 * the same way (allowListGroup in dashboard_api.go).
 */
export const GROUP_JID_RE = /^[0-9]+(-[0-9]+)?@g\.us$/;

/** Short, readable form of a chat JID. */
export function shortJid(jid) {
  if (!jid) return '';
  const [user, server] = String(jid).split('@');
  if (server === 's.whatsapp.net') return `+${user}`;
  if (server === 'g.us') return user.length > 10 ? `group …${user.slice(-6)}` : `group ${user}`;
  if (server === 'lid') return `id …${user.slice(-6)}`;
  if (!server && /^\d+$/.test(user)) return `+${user}`;
  return jid.length > 28 ? `${jid.slice(0, 12)}…${jid.slice(-10)}` : jid;
}

export function phone(num) {
  return num ? `+${String(num).replace(/^\+/, '')}` : '';
}

/** A sender as the bridge reports it: a bare number gets a +, anything else stays. */
export function phoneish(s) {
  return /^\d{6,}$/.test(s) ? phone(s) : s;
}

/** Who wrote a message, given its sender and chat. */
export function senderLabel(from, chat) {
  if (!from) return 'Someone';
  if (/^\d{6,}$/.test(from)) return `+${from}`;
  if (from.includes('@')) return shortJid(from);
  if (!isGroupJid(chat) && from === chat) return shortJid(from);
  return from;
}
