// Threads: the recent runs, grouped by the chat they served. A task the
// core brain delegated is in the thread of the run that delegated it.

import { qs } from '../app/api.js';
import { store } from '../app/store.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { fmtDuration, plural } from '../lib/format.js';
import { empty, pageHeader, spinner, timeAgo } from '../components/common.js';
import { outcomeBadge, riskBadge, threadEl } from '../components/policy.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Thread, ThreadRun } from '../../types/api.js' */

/** @param {ThreadRun} r */
function runRow(r) {
  return h('tr',
    h('td', timeAgo(r.started)),
    h('td', h('code.mono', r.brain)),
    h('td', r.skill ? h('span', r.skill, ' ') : h('span.muted', 'no skill '), riskBadge(r.risk)),
    h('td', outcomeBadge(r.outcome), r.detail ? h('div.muted.small', r.detail) : null),
    h('td.right', r.duration_ms ? fmtDuration(r.duration_ms / 1000) : ''),
    h('td.right', h('a.link-btn', { href: `#/audit/${encodeURIComponent(r.run)}` }, 'Audit')));
}

/** @param {Thread} t */
function threadCard(t) {
  return h('article.card.flush',
    h('div.act-meta.thread-head',
      threadEl(t.thread),
      h('span.act-sep', '·'),
      h('span', plural(t.count, 'run')),
      h('span.act-sep', '·'),
      h('span.muted', (t.brains || []).join(', ')),
      h('span.act-time', timeAgo(t.last))),
    h('div.table-wrap', h('table.table',
      h('thead', h('tr', h('th', 'Started'), h('th', 'Brain'), h('th', 'Skill and risk'), h('th', 'Outcome'), h('th.right', 'Took'), h('th.right', ''))),
      h('tbody', (t.runs || []).map(runRow)))));
}

export default /** @satisfies {PageModule} */ ({
  title: 'Threads',
  render(root, ctx) {
    const box = h('div', spinner());
    const list = h('div.stack');
    /** @type {Thread[] | null} */
    let threads = null;
    const show = () => {
      if (!threads) return;
      if (!threads.length) {
        paint(box, 'none', () => h('div.card', empty('No runs yet', 'Each run a brain makes is listed here, under the chat it answered.')));
        return;
      }
      paint(box, 'list', () => list);
      reconcile(list, threads, (t) => `${t.thread}|${t.last}|${t.count}`, threadCard);
    };
    mount(root,
      pageHeader('Threads', 'Recent runs, by the chat they served: which brain ran, the skill the bridge took it for with its risk budget, and how it ended.'),
      box);
    ctx.follow(resource(`/api/threads${qs({ limit: 100 })}`), (data) => { threads = data || []; show(); }, 5000);
    // Chat labels use group names once the groups have loaded.
    ctx.watch(store, (s) => s.names, () => { list.replaceChildren(); show(); });
  },
});
