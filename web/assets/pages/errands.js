// Errands: what Fridolin writes to people for you, for the core brain
// (docs/BRAINS.md, "Errands"). Every errand, those that have not ended
// first, with Stop for one that has not; #/errands/<id> shows one with its
// conversation; and the people Fridolin stopped listening to, with
// Unblock. What the people wrote is theirs: it is shown as text only,
// never read as markup.

import { post } from '../app/api.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { btn } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { toast } from '../components/toast.js';
import { icon } from '../components/icons.js';
import { badge, empty, kv, longText, mono, spinner, timeAgo } from '../components/common.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { BlockedContact, ErrandDetail, ErrandLine, ErrandView } from '../../types/api.js' */

const errandsResource = resource('/api/errands');

// An errand's state, as its badge says it.
/** @type {Record<string, [string, string]>} */
const STATES = {
  asking: ['Waiting for you', 'warn'],
  open: ['Open', 'info'],
  done: ['Done', 'ok'],
  aborted: ['Aborted', 'bad'],
  declined: ['Declined', 'neutral'],
  expired: ['Expired', 'neutral'],
  stopped: ['Stopped', 'neutral'],
};

/** @param {string} state */
function stateBadge(state) {
  const [text, kind] = STATES[state] || [state, 'neutral'];
  return badge(text, kind);
}

// Who wrote a line of the conversation, and its class: the bridge's own
// names only, whatever a line holds.
/** @type {Record<string, string>} */
const FROM = { you: 'you', them: 'them', owner: 'owner' };

/**
 * Stop, after a confirmation: the errand ends without a word to them.
 * @param {ErrandView} e
 * @param {() => Promise<unknown>} after
 */
function stopButton(e, after) {
  return btn('Stop', {
    kind: 'danger', small: true, icon: 'stop',
    onClick: async () => {
      const ok = await confirmDialog({
        title: 'Stop this errand?',
        message: h('p', 'The errand with ', h('strong', e.name), ' ends now, without a word to them. What they write afterwards is passed on to you, not answered.'),
        confirm: 'Stop', danger: true,
      });
      if (!ok) return;
      await post(`/api/errands/${encodeURIComponent(e.id)}/stop`);
      toast('Errand stopped', 'ok', { timeout: 2000 });
      await after();
    },
  });
}

/**
 * Unblock, after a confirmation: as !unblock in your own chat.
 * @param {BlockedContact} c
 */
function unblockButton(c) {
  return btn('Unblock', {
    small: true, icon: 'check',
    onClick: async () => {
      const ok = await confirmDialog({
        title: 'Unblock this person?',
        message: h('div',
          h('p', 'Fridolin reads what ', h('strong', c.name || 'they'), ' (', mono(`+${c.number}`), ') write again, and passes it on to you. Writing to them in an errand asks you first again.'),
          h('p.muted.small', 'As !unblock in your own chat.')),
        confirm: 'Unblock',
      });
      if (!ok) return;
      await post(`/api/errand-contacts/${encodeURIComponent(c.number)}/unblock`);
      toast('Unblocked', 'ok', { timeout: 2000 });
      await errandsResource.refresh();
    },
  });
}

/** @param {ErrandView} e */
function errandRow(e) {
  return h('tr',
    h('td', timeAgo(e.created)),
    h('td', h('a', { href: `#/errands/${encodeURIComponent(e.id)}` }, e.name), h('div.muted.small.mono', e.masked)),
    h('td', h('code.mono', e.brain)),
    h('td', stateBadge(e.state)),
    h('td', String(e.sent)),
    h('td', e.live ? h('span', 'ends ', timeAgo(e.deadline)) : timeAgo(e.closed || e.deadline)),
    h('td', e.outcome ? longText(e.outcome, { limit: 160, lines: 3 }) : null),
    h('td', e.live ? stopButton(e, () => errandsResource.refresh()) : null));
}

/** @param {BlockedContact} c */
function blockedRow(c) {
  return h('tr',
    h('td', c.name || h('span.muted', 'no name'), h('div.muted.small.mono', c.masked)),
    h('td', c.reason ? longText(c.reason, { limit: 160, lines: 3 }) : null),
    h('td', timeAgo(c.since)),
    h('td', unblockButton(c)));
}

/**
 * The conversation: Fridolin's lines on one side, theirs on the other,
 * yours (passed on through errand_answer) beside Fridolin's.
 * @param {ErrandLine[]} lines
 * @param {string} name
 */
function conversation(lines, name) {
  if (!lines.length) return h('div.card', empty('Nothing written yet', 'The first message goes out once you approve it.'));
  return h('div.card', h('div.errand-lines', lines.map((l) => {
    const from = FROM[l.from] || 'them';
    const who = from === 'you' ? 'Fridolin' : from === 'owner' ? 'You, passed on' : name;
    return h(`div.errand-line.${from}`,
      h('div.errand-line-text', l.text),
      h('div.errand-line-meta', who, ' · ', timeAgo(l.at)));
  })));
}

export default /** @satisfies {PageModule} */ ({
  title: (params) => (params.id ? 'Errand' : 'Errands'),
  render(root, ctx) {
    const id = ctx.params.id || '';
    const heading = h('h1', id ? 'Errand' : 'Errands');
    const detailBox = h('div', id ? spinner() : null);
    const listBox = h('div', spinner());
    const listBody = h('tbody');
    const table = h('div.card.flush.table-wrap', h('table.table',
      h('thead', h('tr', h('th', 'Opened'), h('th', 'With'), h('th', 'Brain'), h('th', 'State'), h('th', 'Messages'), h('th', 'Ends'), h('th', 'How it ended'), h('th', ''))),
      listBody));
    const blockedBox = h('div');
    const blockedBody = h('tbody');
    const blockedTable = h('div.card.flush.table-wrap', h('table.table',
      h('thead', h('tr', h('th', 'Who'), h('th', 'Why'), h('th', 'Since'), h('th', ''))),
      blockedBody));

    mount(root,
      h('header.page-header', h('div.page-heading',
        id ? h('a.back-link', { href: '#/errands' }, icon('left'), 'Errands') : null,
        heading,
        id ? null : h('p.page-sub', 'Fridolin writes to people who are not in allowed_numbers for you, for the core brain, and carries the conversation until it ends. The first message to someone waits for your approval; what the brief does not cover comes to you.'))),
      id ? h('section.section', detailBox) : null,
      h('section.section', h('h2.section-title', id ? 'All errands' : 'Errands'), listBox),
      h('section.section', h('h2.section-title', 'Blocked'), blockedBox));

    ctx.follow(errandsResource, (data) => {
      if (!data) return;
      const errands = data.errands || [];
      if (!errands.length) {
        paint(listBox, 'none', () => h('div.card', empty('No errands yet', 'When the core brain has Fridolin write to someone for you (contact_person), the errand shows here with its conversation. Errands are off until the core brain’s brain.json turns them on.')));
      } else {
        paint(listBox, 'list', () => table);
        reconcile(listBody, errands, (e) => e.id, errandRow);
      }
      const blocked = data.blocked || [];
      if (!blocked.length) {
        paint(blockedBox, 'none', () => h('p.muted.small', 'Nobody. When someone tries to make Fridolin do what it must not, it ends the errand and stops listening to them until you unblock them, here or with !unblock in your own chat.'));
      } else {
        paint(blockedBox, 'list', () => blockedTable);
        reconcile(blockedBody, blocked, (c) => c.number, blockedRow);
      }
    }, 5000);

    if (!id) return;
    const one = resource(`/api/errands/${encodeURIComponent(id)}`, { missing: null });
    const refresh = () => Promise.all([one.refresh(), errandsResource.refresh()]);
    ctx.follow(one, (d) => {
      if (d === undefined) return;
      if (!d) {
        paint(detailBox, 'missing', () => h('div.card', empty('No such errand', `There is no errand “${id}”.`, btn('All errands', { href: '#/errands', kind: 'primary' }))));
        return;
      }
      heading.textContent = `Errand with ${d.name}`;
      ctx.setTitle(`Errand with ${d.name}`);
      paint(detailBox, d, renderDetail);
    }, 3000);

    /** @param {ErrandDetail} d */
    function renderDetail(d) {
      return h('div.stack',
        h('div.card',
          h('div.card-head', h('div.card-title', h('h2', d.name)), stateBadge(d.state)),
          kv([
            ['Number', mono(`+${d.number}`, { copy: true })],
            ['Brain', h('code.mono', d.brain)],
            ['Goal', d.goal],
            ['Brief', longText(d.brief, { limit: 320, lines: 6 })],
            ['Asked', d.by_owner ? 'by you, in your own chat' : 'by a run, not your own message: it waited for your approval'],
            ['Opened', timeAgo(d.created)],
            [d.live ? 'Ends' : 'Ended', timeAgo(d.live ? d.deadline : (d.closed || d.deadline))],
            ['Messages', `${d.sent} from Fridolin, at most ${d.max_messages}`],
            ['How it ended', d.outcome ? longText(d.outcome, { limit: 320, lines: 6 }) : null],
          ]),
          d.live ? h('div.actions', stopButton(d, refresh)) : null),
        h('h2.section-title', 'Conversation'),
        h('p.muted.small', 'What they wrote is theirs, shown as they wrote it.'),
        conversation(d.lines || [], d.name));
    }
  },
});
