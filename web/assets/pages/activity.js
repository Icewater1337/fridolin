// Activity: messages across brains, newest first.

import { get, qs } from '../app/api.js';
import { store, onStatus } from '../app/state.js';
import { h, mount } from '../lib/dom.js';
import { changed } from '../lib/memo.js';
import { select, textInput } from '../components/fields.js';
import { empty, pageHeader, spinner } from '../components/common.js';
import { activityItem } from '../components/activity-item.js';

export default {
  title: 'Activity',
  render(root, ctx) {
    const memo = {};
    let brain = ctx.params.brain || '';
    let search = '';
    let items = null;

    const brainSel = select([['', 'All brains']], brain, { 'aria-label': 'Filter by brain' });
    const searchIn = textInput({ type: 'search', placeholder: 'Search messages', 'aria-label': 'Search messages' });
    const list = h('div.activity-list', spinner());
    const count = h('span.muted.small');

    const fillBrains = (s) => {
      const opts = [['', 'All brains'], ...(s.brains || []).map((b) => [b.id, b.name && b.name !== b.id ? `${b.name} (${b.id})` : b.id])];
      if (brain && !opts.some(([v]) => v === brain)) opts.push([brain, brain]);
      if (!changed(memo, 'opts', opts)) return;
      mount(brainSel, opts.map(([v, l]) => h('option', { value: v, selected: v === brain }, l)));
      brainSel.value = brain;
    };
    ctx.onCleanup(onStatus(fillBrains));

    const paint = () => {
      if (!items) return;
      const q = search.toLowerCase();
      const shown = q ? items.filter((e) => `${e.text} ${e.from} ${e.chat} ${e.brain} ${store.groupNames.get(e.chat) || ''}`.toLowerCase().includes(q)) : items;
      count.textContent = items.length ? `${shown.length} of ${items.length} messages` : '';
      if (!shown.length) {
        mount(list, items.length
          ? empty('No matches', 'No message matches your search.')
          : empty('No messages yet', brain ? 'This brain has not received or sent anything recently.' : 'Messages your brains receive and send show up here. The feed keeps the latest few hundred, newest first.'));
        return;
      }
      mount(list, shown.map((e) => activityItem(e, { showBrain: !brain })));
    };

    brainSel.addEventListener('change', () => {
      brain = brainSel.value;
      ctx.replaceHash(brain ? `#/activity/${encodeURIComponent(brain)}` : '#/activity');
      items = null;
      memo.items = '';
      mount(list, spinner());
      poller.kick();
    });
    let t = null;
    searchIn.addEventListener('input', () => {
      clearTimeout(t);
      t = setTimeout(() => { search = searchIn.value.trim(); paint(); }, 150);
    });

    mount(root,
      pageHeader('Activity', 'Messages across all brains, newest first. Long messages are folded.'),
      h('div.toolbar', h('div.toolbar-group', brainSel, searchIn), count),
      list);

    const poller = ctx.poll(async (signal) => {
      const data = await get(`/api/activity${qs({ limit: 200, brain })}`, { signal });
      if (!changed(memo, 'items', data)) return;
      items = data || [];
      paint();
    }, 3000);
  },
};
