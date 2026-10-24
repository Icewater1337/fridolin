// The pieces the policy's pages share: badges for a risk level, a run's
// outcome and an approval's status, and the chat a line is about.

import { h } from '../lib/dom.js';
import { badge } from './common.js';
import { chatEl } from './chat.js';

/** @type {Record<string, string>} */
const RISK = { read: 'neutral', write: 'info', irreversible: 'warn' };
/** @type {Record<string, string>} */
const STATUS = { pending: 'info', approved: 'ok', answered: 'ok', denied: 'bad', expired: 'neutral' };

/**
 * The badge of a risk level; nothing for none.
 * @param {string | null | undefined} risk
 */
export function riskBadge(risk) {
  return risk ? badge(risk, RISK[risk] || 'neutral', 'Risk level') : null;
}

/**
 * The badge of how a run or a tool call ended.
 * @param {string | null | undefined} outcome
 */
export function outcomeBadge(outcome) {
  const o = String(outcome || '');
  if (!o) return null;
  if (o === 'ok' || o === 'approved' || o === 'answered') return badge(o, 'ok');
  if (o === 'running' || o === 'pending') return badge(o, 'info');
  if (o === 'stopped' || o === 'expired' || o.startsWith('ignored') || o === 'draft shown') return badge(o, 'neutral');
  if (o.startsWith('refused') || o.startsWith('lockdown')) return badge(o, 'warn');
  return badge(o, 'bad');
}

/**
 * The badge of an approval's status.
 * @param {string} status
 */
export function statusBadge(status) {
  return badge(status, STATUS[status] || 'neutral');
}

/**
 * A thread or chat: a WhatsApp chat by name, a delegated task's by id.
 * @param {string | null | undefined} jid
 */
export function threadEl(jid) {
  if (jid && jid.startsWith('delegate:')) return h('code.mono', jid);
  return chatEl(jid);
}
