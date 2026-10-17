// WhatsApp ids and phone numbers, shortened for people to read.

export function isGroupJid(jid) { return typeof jid === 'string' && jid.endsWith('@g.us'); }

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
