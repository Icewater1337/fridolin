// Status pills for brains: whether a brain runs, and how its last run
// ended; and the invitation to add the first brain.

import { h } from '../lib/dom.js';
import { issueLabel } from '../lib/brain.js';
import { icon } from './icons.js';
import { btn } from './buttons.js';
import { pill, badge, timeAgo } from './common.js';

/** @import { BrainRuntime, RunInfo } from '../../types/api.js' */

/**
 * How a brain's last run ended, as a pill.
 * @param {RunInfo | null | undefined} last
 */
export function outcomePill(last) {
  if (!last) return pill('No runs yet', 'neutral');
  /** @type {Record<string, [string, string]>} */
  const map = { ok: ['Replied', 'ok'], error: ['Failed', 'bad'], interrupted: ['Interrupted', 'warn'], question: ['Asked a question', 'info'] };
  const [label, kind] = map[last.outcome] || [last.outcome || 'Unknown', 'neutral'];
  return pill(label, kind, { title: last.error || '' });
}

/**
 * A brain's status (ok, or its worst issue's kind) as a pill.
 * @param {string} status
 */
export function brainStatusPill(status) {
  if (status === 'ok') return pill('Active', 'ok');
  const kind = /** @type {Record<string, string>} */ ({ drift: 'warn', missing: 'bad', invalid: 'bad', unapproved: 'bad' })[status] || 'neutral';
  return pill(issueLabel(status, { short: true }), kind);
}

/**
 * What a running brain is doing, from its runtime ({running, queue, last}):
 * working or idle, how many messages wait, and how the last run ended.
 * @param {Pick<BrainRuntime, 'running' | 'queue' | 'last'>} rt
 */
export function runtimeSummary(rt) {
  const queued = (rt.queue || []).length;
  return [
    rt.running ? pill('Working', 'info', { pulse: true, title: rt.running.preview || '' }) : pill('Idle', 'neutral'),
    queued ? badge(`${queued} queued`, 'warn') : null,
    h('span.run-last', outcomePill(rt.last), rt.last ? h('span.muted.small', timeAgo(rt.last.finished)) : null),
  ];
}

/** The card shown while there is no brain at all. */
export function firstBrainHero() {
  return h('div.card.hero',
    h('div.hero-icon', icon('sparkle')),
    h('div.hero-body',
      h('h2', 'Set up your first brain'),
      h('p', 'A brain is a folder on this computer with its own instructions, memory and conversation history. Pick or create a folder, choose which chats it answers, and you are ready to go.'),
      h('div.actions', btn('Add a brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' }))));
}
