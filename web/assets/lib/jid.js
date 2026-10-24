// WhatsApp ids and phone numbers, shortened for people to read.

/** @param {unknown} jid */
export function isGroupJid(jid) { return typeof jid === 'string' && jid.endsWith('@g.us'); }

/**
 * A group JID as typed in to be routed or allowed: digits (with an
 * old-style group's -suffix) and @g.us. The bridge checks the allow lists
 * the same way (allowListGroup in dashboard_api.go).
 */
export const GROUP_JID_RE = /^[0-9]+(-[0-9]+)?@g\.us$/;

/**
 * Short, readable form of a chat JID.
 * @param {string | null | undefined} jid
 * @returns {string}
 */
export function shortJid(jid) {
  if (!jid) return '';
  const [user, server] = String(jid).split('@');
  if (server === 's.whatsapp.net') return `+${user}`;
  if (server === 'g.us') return user.length > 10 ? `group …${user.slice(-6)}` : `group ${user}`;
  if (server === 'lid') return `id …${user.slice(-6)}`;
  if (!server && /^\d+$/.test(user)) return `+${user}`;
  return jid.length > 28 ? `${jid.slice(0, 12)}…${jid.slice(-10)}` : jid;
}

/**
 * A phone number as the bridge keeps it, digits with the country code, or
 * null for anything the bridge refuses as one (phoneNumber in
 * identity.go): a +, spaces, dashes, dots, brackets and @s.whatsapp.net
 * go, and 6 to 15 digits must be left. A @lid JID holds WhatsApp's
 * internal id for a sender, no number.
 * @param {unknown} s
 * @returns {string | null}
 */
export function phoneDigits(s) {
  const t = String(s ?? '').trim();
  const at = t.indexOf('@');
  if (at >= 0 && t.slice(at + 1) !== 's.whatsapp.net') return null;
  const n = (at >= 0 ? t.slice(0, at) : t).replace(/^[+\s]+/, '').trim().replace(/[ \u00a0\-.()]/g, '');
  return /^[0-9]{6,15}$/.test(n) ? n : null;
}

/**
 * A number with one leading +, '' for none.
 * @param {string | number | null | undefined} num
 */
export function phone(num) {
  return num ? `+${String(num).replace(/^\+/, '')}` : '';
}

/**
 * A sender as the bridge reports it: a bare number gets a +, anything else stays.
 * @param {string} s
 */
export function phoneish(s) {
  return /^\d{6,}$/.test(s) ? phone(s) : s;
}

/**
 * Who wrote a message, given its sender and chat.
 * @param {string | null | undefined} from
 * @param {string | null | undefined} chat
 * @returns {string}
 */
export function senderLabel(from, chat) {
  if (!from) return 'Someone';
  if (/^\d{6,}$/.test(from)) return `+${from}`;
  if (from.includes('@')) return shortJid(from);
  if (!isGroupJid(chat) && from === chat) return shortJid(from);
  return from;
}
