// What GET /api/connections says about usage, in words: the month, what a
// caller used, what it cost against its limit, and who the caller is.
// Pure functions, so they are unit-tested with node --test
// (web/test/connections.test.js).

import { brainLabel } from './brain.js';
import { fmtDuration, fmtUSD } from './format.js';

/** @import { ConnectionUsage } from '../../types/api.js' */

const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

/**
 * "October 2026" for "2026-10"; "this month" for no month, and the text
 * as it is for anything else.
 * @param {string | null | undefined} month
 */
export function monthName(month) {
  const m = /^(\d{4})-(\d{2})$/.exec(month || '');
  const name = m && MONTHS[Number(m[2]) - 1];
  return m && name ? `${name} ${m[1]}` : month || 'this month';
}

const count = (/** @type {number} */ n) => Math.round(n).toLocaleString('en-US');

/**
 * What a caller used, as the providers bill it: "5 min 41 s of audio ·
 * 1,200 tokens in, 800 out · 3,400 characters"; '' for nothing.
 * @param {Pick<ConnectionUsage, 'audio_seconds' | 'input_tokens' | 'output_tokens' | 'characters'>} u
 */
export function usedText(u) {
  const parts = [];
  if (u.audio_seconds > 0) parts.push(`${fmtDuration(u.audio_seconds)} of audio`);
  if (u.input_tokens > 0 || u.output_tokens > 0) parts.push(`${count(u.input_tokens || 0)} tokens in, ${count(u.output_tokens || 0)} out`);
  if (u.characters > 0) parts.push(`${count(u.characters)} ${Math.round(u.characters) === 1 ? 'character' : 'characters'}`);
  return parts.join(' · ');
}

/**
 * "$0.05 of $20", or "$0.05" for a caller with no limit (0).
 * @param {number} usd
 * @param {number} limit
 */
export function spendText(usd, limit) {
  return limit > 0 ? `${fmtUSD(usd)} of ${fmtUSD(limit)}` : fmtUSD(usd);
}

/**
 * How close a caller is to its monthly limit: 'over' once the estimate
 * reached it (the bridge refuses its calls then), 'near' from 80 %, else
 * ''; '' too for a caller with no limit.
 * @param {number} usd
 * @param {number} limit
 * @returns {'over' | 'near' | ''}
 */
export function budgetState(usd, limit) {
  if (!(limit > 0)) return '';
  if (usd >= limit) return 'over';
  return usd >= 0.8 * limit ? 'near' : '';
}

/**
 * Whom a usage line is for.
 * @typedef {{kind: 'module' | 'brain' | '', id: string, name: string}} Caller
 */

/**
 * The caller of a usage line, "module:<id>" or "brain:<id>", with a
 * brain's name from brains where it is one of them.
 * @param {string} caller
 * @param {readonly {id: string, name?: string}[]} [brains]
 * @returns {Caller}
 */
export function callerOf(caller, brains = []) {
  const at = caller.indexOf(':');
  const kind = caller.slice(0, at);
  const id = caller.slice(at + 1);
  if (kind === 'module') return { kind, id, name: id };
  if (kind === 'brain') {
    const b = brains.find((x) => x.id === id);
    return { kind, id, name: b ? brainLabel(b) : id };
  }
  return { kind: '', id: caller, name: caller };
}
