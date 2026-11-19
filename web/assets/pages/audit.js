// Audit: the bridge's audit log, newest first, filtered by kind, brain,
// thread or text; #/audit/<run> shows one run's lines, the files it
// changed in its brain's folder and a button that puts them back.

import { errorStatus, get, post, qs } from '../app/api.js';
import { store } from '../app/store.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile, syncOptions } from '../lib/dom.js';
import { brainLabel } from '../lib/brain.js';
import { fmtDuration } from '../lib/format.js';
import { select, textInput } from '../components/fields.js';
import { btn } from '../components/buttons.js';
import { badge, callout, empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { confirmDialog } from '../components/dialog.js';
import { toast } from '../components/toast.js';
import { outcomeBadge, riskBadge, threadEl } from '../components/policy.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { AuditEntry, RunFilesView, UndoResult, UndoStep } from '../../types/api.js' */

/** @type {[string, string][]} */
const KINDS = [
  ['', 'Every kind'],
  ['run_start', 'Run started'], ['run_end', 'Run ended'], ['run_refused', 'Run refused'],
  ['run_files', 'Files a run changed'],
  ['tool_call', 'Tool call'], ['delegation', 'Delegation'],
  ['approval_requested', 'Approval asked'], ['approval_vote', 'Vote'], ['approval_decided', 'Approval decided'],
  ['question_asked', 'Question asked'], ['question_answered', 'Question answered'],
  ['lockdown', 'Lockdown'], ['unlock', 'Unlock'], ['skill_approved', 'Skill approved'],
  ['node_join_code', 'Join code (!join)'], ['dashboard_link', 'Sign-in link (!dashboard)'],
  ['node_approved', 'Node approved (!approve)'], ['node_revoked', 'Node revoked (!revoke)'],
  ['core_move_requested', 'Core move asked (!movecore)'], ['core_move_approved', 'Core move voted'],
  ['core_move_ended', 'Core move ended'], ['core_secrets', 'Host secrets kept or forgotten'],
  ['scheduled_command', 'Scheduled program on a node'],
  ['fride_instance', 'FrIDE instance started or stopped'], ['node_exec', 'Command on a node (node_exec)'],
  ['errand', 'Errand (writing to someone)'], ['voice_call', 'Call to Fridolin'],
  ['calendar_write', 'Calendar event created or deleted'],
];
const KIND_LABEL = Object.fromEntries(KINDS);

/** @type {Record<string, [string, string]>} */
const ACTION = {
  restore: ['write back', 'info'], trash: ['to the trash', 'warn'],
  none: ['leave', 'neutral'], conflict: ['leave', 'bad'],
};

/** @param {UndoStep} s */
function stepRow(s) {
  const [label, kind] = ACTION[s.action] || [s.action, 'neutral'];
  let note = s.why || null;
  if (s.error) note = `failed: ${s.error}`;
  else if (s.done && s.to) note = `done: ${s.to}`;
  else if (s.done) note = 'done';
  return h('tr',
    h('td', h('code.mono', s.path)),
    h('td', s.op),
    h('td', badge(label, kind), note ? h('div.muted.small', note) : null));
}

/**
 * The files run changed and what undoing it would do, or did (result).
 * @param {RunFilesView | null} view null when the run has no record
 * @param {() => Promise<void>} undo
 * @param {UndoResult | null} [result]
 */
function filesCard(view, undo, result) {
  const head = h('div.card-head', h('div.card-title', h('h2', 'Files')));
  if (!view) {
    return h('section.card', head, h('p.muted', 'This run changed no files in its brain\'s folder, or its record is gone: records are kept for run_files.keep_days.'));
  }
  if (view.running || !view.record) return h('section.card', head, h('p.muted', 'The run has not ended yet; its files are recorded once it has.'));
  const rec = view.record;
  if (rec.skipped) return h('section.card', head, callout('info', 'Its files were not recorded', rec.skipped));
  const steps = (result || view.plan)?.steps || [];
  const todo = steps.filter((s) => s.action === 'restore' || s.action === 'trash').length;
  return h('section.card.flush', head,
    rec.undone ? h('p.muted.small', 'Undone ', timeAgo(rec.undone.at), ` by ${rec.undone.by}.`) : null,
    rec.alongside?.length ? h('p.muted.small', `Ran alongside ${rec.alongside.join(', ')}: what they changed in that time is listed too.`) : null,
    h('div.table-wrap', h('table.table',
      h('thead', h('tr', h('th', 'File'), h('th', 'The run'), h('th', result ? 'The undo' : 'Undoing it would'))),
      h('tbody', steps.map(stepRow)))),
    todo && !result ? h('div.toolbar', btn('Undo this run…', { kind: 'danger', small: true, onClick: undo })) : null);
}

/** @param {AuditEntry} e */
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

export default /** @satisfies {PageModule} */ ({
  title: 'Audit',
  render(root, ctx) {
    const filter = { kind: '', brain: '', q: '', run: ctx.params.run || '' };
    let unfollow = () => {};
    const kindSel = select(KINDS, '', { 'aria-label': 'Filter by kind' });
    const brainSel = select([['', 'All brains']], '', { 'aria-label': 'Filter by brain' });
    const searchIn = textInput({ type: 'search', placeholder: 'Search details', 'aria-label': 'Search details' });
    const runNote = h('div');
    const filesBox = h('div');
    const box = h('div', spinner());
    const body = h('tbody');
    const table = h('div.card.flush.table-wrap', h('table.table',
      h('thead', h('tr', h('th', 'When'), h('th', 'What'), h('th', 'Brain and run'), h('th', 'Thread'), h('th', 'Detail'), h('th', 'Risk'), h('th', 'Outcome'))),
      body));

    ctx.watch(store, (s) => s.status, (s) => {
      if (!s) return;
      syncOptions(brainSel, [['', 'All brains'], ...(s.brains || []).map((b) => /** @type {[string, string]} */ ([b.id, brainLabel(b)]))], filter.brain);
    });

    const showFiles = async () => {
      const run = filter.run;
      if (!run) { paint(filesBox, 'none', () => null); return; }
      const path = `/api/runs/${encodeURIComponent(run)}/files`;
      /** @type {RunFilesView | null} */
      let view = null;
      try {
        view = await get(path);
      } catch (e) {
        if (errorStatus(e) !== 404) throw e;
      }
      const undo = async () => {
        const n = (view?.plan?.steps || []).filter((s) => s.action === 'restore' || s.action === 'trash').length;
        const ok = await confirmDialog({
          title: 'Undo this run?',
          message: `${n === 1 ? '1 file is' : `${n} files are`} written back as they were before the run, or moved to the folder's trash if the run created them. A file that changed since the run is left as it is.`,
          confirm: 'Undo', danger: true,
        });
        if (!ok) return;
        /** @type {UndoResult} */
        const res = await post(`/api/runs/${encodeURIComponent(run)}/undo`, {});
        const failed = res.steps.filter((s) => s.error).length;
        toast(failed ? `The run is partly undone: ${failed} could not be put back.` : 'The run is undone.', failed ? 'warn' : 'ok');
        view = await get(path);
        paint(filesBox, `files:${run}:done`, () => filesCard(view, undo, res));
      };
      paint(filesBox, `files:${run}:${JSON.stringify(view)}`, () => filesCard(view, undo));
    };
    const follow = () => {
      unfollow();
      showFiles().catch(() => paint(filesBox, 'none', () => null));
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
      filesBox,
      box);
    follow();
  },
});
