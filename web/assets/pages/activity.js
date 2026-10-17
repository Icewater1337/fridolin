// Activity: messages across brains, newest first.

import { qs } from '../app/api.js';
import { store, chatName } from '../app/store.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, syncOptions } from '../lib/dom.js';
import { select, textInput } from '../components/fields.js';
import { empty, pageHeader, spinner } from '../components/common.js';
import { showActivity } from '../components/activity-item.js';

export default {
  title: 'Activity',
  render(root, ctx) {
    let brain = ctx.params.brain || '';
    let search = '';
    let items = null;
    let unfollow = () => {};

    const brainSel = select([['', 'All brains']], brain, { 'aria-label': 'Filter by brain' });
    const searchIn = textInput({ type: 'search', placeholder: 'Search messages', 'aria-label': 'Search messages' });
    const list = h('div.activity-list', spinner());
    const count = h('span.muted.small');

    const fillBrains = (s) => {
      const opts = [['', 'All brains'], ...(s.brains || []).map((b) => [b.id, b.name && b.name !== b.id ? `${b.name} (${b.id})` : b.id])];
      if (brain && !opts.some(([v]) => v === brain)) opts.push([brain, brain]);
      syncOptions(brainSel, opts, brain);
    };
    ctx.watch(store, (s) => s.status, (s) => { if (s) fillBrains(s); });

    const paintList = () => {
      if (!items) return;
      const q = search.toLowerCase();
      const shown = q ? items.filter((e) => `${e.text} ${e.from} ${e.chat} ${e.brain} ${chatName(e.chat)}`.toLowerCase().includes(q)) : items;
      count.textContent = items.length ? `${shown.length} of ${items.length} messages` : '';
      // The empty states and the messages share the card.
      if (!shown.length) {
        paint(list, { searching: items.length > 0, brain }, ({ searching }) => (searching
          ? empty('No matches', 'No message matches your search.')
          : empty('No messages yet', brain ? 'This brain has not received or sent anything recently.' : 'Messages your brains receive and send show up here. The feed keeps the latest few hundred, newest first.')));
        return;
      }
      paint(list, 'messages', () => null);
      showActivity(list, shown, { showBrain: !brain });
    };
    // Chat labels and the search use group names once the groups have loaded.
    ctx.watch(store, (s) => s.names, () => paintList());

    // One feed per brain filter; the page follows the one it shows.
    const follow = () => {
      unfollow();
      const feed = resource(`/api/activity${qs({ limit: 200, brain })}`);
      unfollow = feed.follow((data) => {
        items = data || [];
        paintList();
      }, 3000);
    };
    ctx.onCleanup(() => unfollow());

    brainSel.addEventListener('change', () => {
      brain = brainSel.value;
      ctx.replaceHash(brain ? `#/activity/${encodeURIComponent(brain)}` : '#/activity');
      items = null;
      paint(list, 'loading', () => spinner());
      follow();
    });
    searchIn.addEventListener('input', ctx.debounce(() => { search = searchIn.value.trim(); paintList(); }, 150));

    mount(root,
      pageHeader('Activity', 'Messages across all brains, newest first. Long messages are folded.'),
      h('div.toolbar', h('div.toolbar-group', brainSel, searchIn), count),
      list);
    follow();
  },
};
