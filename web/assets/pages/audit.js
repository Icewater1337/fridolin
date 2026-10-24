// Audit: the bridge's audit log, newest first, filtered by kind, brain,
// thread or text; #/audit/<run> shows one run's lines.

import { qs } from '../app/api.js';
import { store } from '../app/store.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile, syncOptions } from '../lib/dom.js';
import { brainLabel } from '../lib/brain.js';
import { fmtDuration } from '../lib/format.js';
import { select, textInput } from '../components/fields.js';
import { btn } from '../components/buttons.js';
import { empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { outcomeBadge, riskBadge, threadEl } from '../components/policy.js';

const KINDS = [
  ['', 'Every kind'],
  ['run_start', 'Run started'], ['run_end', 'Run ended'], ['run_refused', 'Run refused'],
  ['tool_call', 'Tool call'], ['delegation', 'Delegation'],
  ['approval_requested', 'Approval asked'], ['approval_vote', 'Vote'], ['approval_decided', 'Approval decided'],
  ['question_asked', 'Question asked'], ['question_answered', 'Question answered'],
  ['lockdown', 'Lockdown'], ['unlock', 'Unlock'], ['skill_approved', 'Skill approved'],
  ['node_join_code', 'Join code (!join)'], ['dashboard_link', 'Sign-in link (!dashboard)'],
  ['node_approved', 'Node approved (!approve)'], ['node_revoked', 'Node revoked (!revoke)'],
  ['core_move_requested', 'Core move asked (!movecore)'], ['core_move_approved', 'Core move voted'],
  ['core_move_ended', 'Core move ended'], ['core_secrets', 'Host secrets kept or forgotten'],
  ['scheduled_command', 'Scheduled program on a node'],
];
const KIND_LABEL = Object.fromEntries(KINDS);

function lineRow(e) {
  return h('tr',
    h('td', timeAgo(e.ts)),
    h('td', KIND_LABEL[e.kind] || e.kind),
    h('td', e.brain ? h('code.mono', e.brain) : null, e.run ? h('div.muted.small.mono', e.run) : null),
    h('td', e.thread ? threadEl(e.thread) : null),
    h('td', e.detail || null, e.skill ? h('div.muted.small', `skill ${e.skill}`) : null, e.by ? h('div.muted.small', `by ${e.by}`) : null),
    h('td', riskBadge(e.risk)),
    h('td', outcomeBadge(e.outcome), e.duration_ms ? h('div.muted.small', fmtDuration(e.duration_ms / 1000)) : null));
}

export default {
  title: 'Audit',
  render(root, ctx) {
    const filter = { kind: '', brain: '', q: '', run: ctx.params.run || '' };
    let unfollow = () => {};
    const kindSel = select(KINDS, '', { 'aria-label': 'Filter by kind' });
    const brainSel = select([['', 'All brains']], '', { 'aria-label': 'Filter by brain' });
    const searchIn = textInput({ type: 'search', placeholder: 'Search details', 'aria-label': 'Search details' });
    const runNote = h('div');
    const box = h('div', spinner());
    const body = h('tbody');
    const table = h('div.card.flush.table-wrap', h('table.table',
      h('thead', h('tr', h('th', 'When'), h('th', 'What'), h('th', 'Brain and run'), h('th', 'Thread'), h('th', 'Detail'), h('th', 'Risk'), h('th', 'Outcome'))),
      body));

    ctx.watch(store, (s) => s.status, (s) => {
      if (!s) return;
      syncOptions(brainSel, [['', 'All brains'], ...(s.brains || []).map((b) => [b.id, brainLabel(b)])], filter.brain);
    });

    const follow = () => {
      unfollow();
      paint(runNote, filter.run, (run) => (run ? h('div.toolbar', h('span.muted.small', 'The lines of run ', h('code.mono', run)),
        btn('Show every run', { small: true, href: '#/audit' })) : null));
      const feed = resource(`/api/audit${qs({ limit: 500, kind: filter.kind, brain: filter.brain, q: filter.q, run: filter.run })}`);
      unfollow = feed.follow((lines) => {
        if (!lines || !lines.length) {
          paint(box, 'none', () => h('div.card', empty('Nothing logged', 'Runs, tool calls, approvals, votes, delegations and lockdowns are written here as they happen.')));
          return;
        }
        paint(box, 'table', () => table);
        reconcile(body, lines, (e) => `${e.ts}|${e.kind}|${e.run}|${e.id}`, lineRow);
      }, 5000);
    };
    ctx.onCleanup(() => unfollow());
    const refilter = () => { paint(box, 'loading', () => spinner()); follow(); };
    kindSel.addEventListener('change', () => { filter.kind = kindSel.value; refilter(); });
    brainSel.addEventListener('change', () => { filter.brain = brainSel.value; refilter(); });
    searchIn.addEventListener('input', ctx.debounce(() => { filter.q = searchIn.value.trim(); refilter(); }, 300));

    mount(root,
      pageHeader('Audit', 'What brains did and who decided: one line per run, tool call, approval, vote, delegation and lockdown, from the log in the data dir (audit/YYYY-MM.jsonl). Arguments and secrets are never written.'),
      h('div.toolbar', h('div.toolbar-group', kindSel, brainSel, searchIn)),
      runNote,
      box);
    follow();
  },
};
