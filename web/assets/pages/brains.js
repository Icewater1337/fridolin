// Brains: a card per brain with its routing, live state and issues.

import { get } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { routingSummary } from '../lib/brain.js';
import { changed } from '../lib/memo.js';
import { icon } from '../components/icons.js';
import { btn } from '../components/buttons.js';
import { pill, badge, timeAgo, mono, pageHeader, spinner } from '../components/common.js';
import { outcomePill, brainStatusPill } from '../components/brain.js';

export default {
  title: 'Brains',
  render(root, ctx) {
    const memo = {};
    const list = h('div.brain-cards', spinner());

    mount(root,
      pageHeader('Brains', 'Each brain is a folder with its own instructions, memory and history. Messages are routed to a brain by chat.',
        btn('Add brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' })),
      list);

    ctx.poll(async (signal) => {
      const brains = await get('/api/brains', { signal });
      if (!changed(memo, 'brains', brains)) return;
      renderList(brains);
    }, 3000);

    function renderList(brains) {
      if (!brains.length) {
        mount(list, h('div.card.hero',
          h('div.hero-icon', icon('sparkle')),
          h('div.hero-body',
            h('h2', 'No brains yet'),
            h('p', 'Add a folder as a brain. It gets a prompt file you can edit, its own memory and its own conversation history. You decide which chats it answers.'),
            h('div.actions', btn('Add your first brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' })))));
        return;
      }
      mount(list, brains.map(brainCard));
    }
  },
};

function brainCard(b) {
  const rt = b.runtime;
  const m = b.manifest || {};
  const routing = (rt && rt.routing) || m.routing;
  const queue = rt ? (rt.queue || []).length : 0;
  const href = b.id ? `#/brains/${encodeURIComponent(b.id)}` : null;
  const body = [
    h('div.bc-head',
      h('div.bc-title',
        h('h3', b.name || b.id || 'Unnamed brain'),
        b.id ? h('code.mono.muted', b.id) : null),
      h('div.bc-pills', brainStatusPill(b.status))),
    h('div.bc-path', icon('folder'), mono(b.path)),
    h('div.bc-facts',
      h('span', icon('chats'), routingSummary(routing)),
      m.model ? h('span', icon('sparkle'), m.model) : null,
      m.effort ? h('span', `effort: ${m.effort}`) : null,
      m.backup ? h('span', icon('backup'), m.backup.enabled ? 'Backed up' : 'No backup') : null),
    rt ? h('div.bc-runtime',
      rt.running ? pill('Working', 'info', { pulse: true, title: rt.running.preview || '' }) : pill('Idle', 'neutral'),
      queue ? badge(`${queue} queued`, 'warn') : null,
      outcomePill(rt.last),
      rt.last ? h('span.muted.small', timeAgo(rt.last.finished)) : null)
      : (b.status === 'ok' ? null : h('div.bc-runtime', h('span.muted.small', 'Not running'))),
    (b.issues || []).length ? h('ul.issue-list', b.issues.map((is) => h('li', h('strong', `${is.kind}: `), is.detail))) : null,
  ];
  if (href) return h('a.card.brain-card.link-card', { href }, body, icon('right', 'row-chevron'));
  return h('div.card.brain-card', body, h('p.muted.small', 'This entry has no approved manifest, so it cannot be opened here. Remove it from brain_dirs in config.json and add the folder again.'));
}
