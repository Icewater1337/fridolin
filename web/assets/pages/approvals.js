// Approvals: what runs asked the owner (request_approval, ask_user), the
// ones still waiting with Approve and Deny, which count as the owner's
// vote, the ones decided, and the lockdown with its switch.

import { post } from '../app/api.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { btn } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { toast } from '../components/toast.js';
import { callout, empty, longText, pageHeader, spinner, timeAgo } from '../components/common.js';
import { riskBadge, statusBadge, threadEl } from '../components/policy.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Approval } from '../../types/api.js' */

const approvalsResource = resource('/api/approvals');
const policyResource = resource('/api/policy');

/**
 * @param {Approval} a
 * @param {boolean} approve
 */
function decide(a, approve) {
  return async () => {
    await post(`/api/approvals/${encodeURIComponent(a.id)}`, { decision: approve ? 'approve' : 'deny' });
    toast(approve ? 'Approved' : 'Denied', 'ok', { timeout: 2000 });
    await Promise.all([approvalsResource.refresh(), policyResource.refresh()]);
  };
}

/** @param {Approval} a */
function pendingCard(a) {
  const question = a.kind === 'question';
  return h('article.card.approval-card',
    h('div.act-meta',
      h('strong', question ? 'Question' : 'Approval'),
      h('span.act-sep', 'from'), h('code.mono', a.brain),
      h('span.act-sep', 'in'), threadEl(a.chat),
      riskBadge(a.risk),
      a.skill ? h('span.muted.small', `skill ${a.skill}${a.step ? `, step ${a.step}` : ''}`) : null,
      h('span.act-time', timeAgo(a.created_at))),
    h('p.approval-action', a.action),
    a.details ? longText(a.details, { limit: 400, lines: 8 }) : null,
    question
      ? h('p.muted.small', a.options && a.options.length ? `Options: ${a.options.join(' · ')}. Answer in the chat.` : 'Answer in the chat: the next message from you there is the answer.')
      : h('div.actions',
        btn('Approve', { kind: 'primary', icon: 'check', onClick: decide(a, true) }),
        btn('Deny', { kind: 'danger', icon: 'x', onClick: decide(a, false) }),
        h('span.muted.small', h('span', 'No answer by '), timeAgo(a.expires_at), h('span', ' counts as no.'))));
}

/** @param {Approval} a */
function historyRow(a) {
  return h('tr',
    h('td', timeAgo(a.decided_at || a.created_at)),
    h('td', h('code.mono', a.brain)),
    h('td', a.action, a.answer && a.kind === 'question' ? h('div.muted.small', `→ ${a.answer}`) : null),
    h('td', riskBadge(a.risk)),
    h('td', statusBadge(a.status)),
    h('td', a.decided_by ? h('span.mono.small', a.decided_by) : null));
}

export default /** @satisfies {PageModule} */ ({
  title: 'Approvals',
  render(root, ctx) {
    const lock = h('div');
    const pendingBox = h('div', spinner());
    const pendingList = h('div.stack');
    const historyBox = h('div');

    mount(root,
      pageHeader('Approvals', 'Before an irreversible step a run asks you with a poll in WhatsApp; only votes from your own numbers count. You can answer here too.'),
      lock,
      h('section.section', h('h2.section-title', 'Waiting for you'), pendingBox),
      h('section.section', h('h2.section-title', 'Decided'), historyBox));

    ctx.follow(policyResource, (p) => {
      if (!p) return;
      paint(lock, { on: p.lockdown, by: p.lockdown_by, since: p.lockdown_since }, ({ on, by, since }) => {
        if (on) {
          return callout('warn', 'Lockdown', h('p', 'Only read-only work runs: nothing writes, sends or submits, and no approval is asked. ',
            `Since ${since ? new Date(since).toLocaleString() : '—'}, by ${by || '—'}.`),
          h('div.actions', btn('Unlock', { kind: 'primary', icon: 'play', onClick: async () => {
            await post('/api/policy/lockdown', { on: false });
            toast('Unlocked', 'ok');
            await policyResource.refresh();
          } })));
        }
        return h('div.toolbar',
          h('span.muted.small', `At most ${p.max_runs_per_hour} runs an hour per chat, ${p.max_pending_irreversible} irreversible actions waiting at once; no answer within ${p.approval_timeout_hours} h counts as no.`),
          btn('Lock down…', { kind: 'danger', small: true, icon: 'lock', onClick: async () => {
            const ok = await confirmDialog({
              title: 'Lock the bridge down?',
              message: h('p', 'Only read-only work runs until you unlock it, here or with !unlock from your number. Runs of skills that write or submit are refused.'),
              confirm: 'Lock down', danger: true,
            });
            if (!ok) return;
            await post('/api/policy/lockdown', { on: true });
            await policyResource.refresh();
          } }));
      });
    }, 5000);

    ctx.follow(approvalsResource, (data) => {
      if (!data) return;
      const pending = data.pending || [];
      if (!pending.length) {
        paint(pendingBox, 'none', () => h('div.card', empty('Nothing waiting', 'When a run asks for approval or asks you a question, it shows here until you answer.')));
      } else {
        paint(pendingBox, 'list', () => pendingList);
        reconcile(pendingList, pending, (a) => a.id, pendingCard);
      }
      const history = data.history || [];
      paint(historyBox, history.length ? history.map((a) => `${a.id}:${a.status}`).join(',') : 'none', () => (history.length
        ? h('div.card.flush.table-wrap', h('table.table',
          h('thead', h('tr', h('th', 'When'), h('th', 'Brain'), h('th', 'Asked'), h('th', 'Risk'), h('th', 'Answer'), h('th', 'By'))),
          h('tbody', history.map(historyRow))))
        : h('div.card', empty('Nothing decided yet', 'Answered, denied and expired requests are kept here.'))));
    }, 3000);
  },
});
