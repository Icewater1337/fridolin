// Status pills for brains: whether a brain runs, and how its last run ended.

import { pill } from './common.js';

export function outcomePill(last) {
  if (!last) return pill('No runs yet', 'neutral');
  const map = { ok: ['Replied', 'ok'], error: ['Failed', 'bad'], interrupted: ['Interrupted', 'warn'], question: ['Asked a question', 'info'] };
  const [label, kind] = map[last.outcome] || [last.outcome || 'Unknown', 'neutral'];
  return pill(label, kind, { title: last.error || '' });
}

export function brainStatusPill(status) {
  const map = {
    ok: ['Active', 'ok'],
    drift: ['Changed on disk', 'warn'],
    missing: ['Folder missing', 'bad'],
    invalid: ['Invalid', 'bad'],
    unapproved: ['Not approved', 'bad'],
  };
  const [label, kind] = map[status] || [status, 'neutral'];
  return pill(label, kind);
}
