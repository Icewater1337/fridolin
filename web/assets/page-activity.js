// Activity: messages across brains, newest first.

import { get, qs } from './api.js';
import { store, onStatus } from './state.js';
import { h, mount, icon, timeAgo, chatEl, longText, empty, changed, select, textInput, pageHeader, spinner, isGroupJid, shortJid } from './ui.js';

const expandedKeys = new Set();

/** One message row; shared with the overview and brain pages. */
export function activityItem(e, { compact = false, showBrain = true } = {}) {
  const key = `${e.ts}|${e.chat}|${e.type}|${(e.text || '').length}`;
  const out = e.type === 'out';
  const who = out
    ? h('span.act-who', 'Reply')
    : h('span.act-who', { title: e.from || '' }, senderLabel(e.from, e.chat));
  return h(`article.act-item.${out ? 'out' : 'in'}${compact ? '.compact' : ''}`,
    h('div.act-icon', { title: out ? 'Sent' : 'Received' }, icon(out ? 'out' : 'in')),
    h('div.act-body',
      h('div.act-meta',
        who,
        isGroupJid(e.chat) || out || senderLabel(e.from, e.chat) !== shortJid(e.chat) ? [h('span.act-sep', out ? 'to' : 'in'), chatEl(e.chat)] : h('span.act-sep', 'direct message'),
        showBrain && e.brain ? h('a.badge.neutral.act-brain', { href: `#/brains/${encodeURIComponent(e.brain)}`, title: 'Brain' }, e.brain) : null,
        h('span.act-time', timeAgo(e.ts))),
      longText(e.text, {
        limit: compact ? 200 : 360,
        lines: compact ? 3 : 6,
        expanded: expandedKeys.has(key),
        onToggle: (open) => { if (open) expandedKeys.add(key); else expandedKeys.delete(key); },
      })));
}

function senderLabel(from, chat) {
  if (!from) return 'Someone';
  if (/^\d{6,}$/.test(from)) return `+${from}`;
  if (from.includes('@')) return shortJid(from);
  if (!isGroupJid(chat) && from === chat) return shortJid(from);
  return from;
}

export default {
  title: 'Activity',
  render(root, ctx) {
    const memo = {};
    let brain = ctx.params[0] || '';
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
      history.replaceState(null, '', brain ? `#/activity/${encodeURIComponent(brain)}` : '#/activity');
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

    const poller = ctx.poll(async () => {
      const data = await get(`/api/activity${qs({ limit: 200, brain })}`);
      if (!changed(memo, 'items', data)) return;
      items = data || [];
      paint();
    }, 3000);
  },
};
