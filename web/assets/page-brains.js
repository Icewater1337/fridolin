// Brains list, plus moving inline (config.json) brains to brain.json files.

import { get, post } from './api.js';
import {
  h, mount, icon, pill, badge, btn, callout, timeAgo, mono, plural, routingSummary, outcomePill,
  brainStatusPill, empty, changed, pageHeader, spinner, openDialog, toast,
} from './ui.js';

export default {
  title: 'Brains',
  render(root, ctx) {
    const memo = {};
    const banner = h('div');
    const list = h('div.brain-cards', spinner());

    mount(root,
      pageHeader('Brains', 'Each brain is a folder with its own instructions, memory and history. Messages are routed to a brain by chat.',
        btn('Add brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' })),
      banner,
      list);

    const poller = ctx.poll(async () => {
      const brains = await get('/api/brains');
      if (!changed(memo, 'brains', brains)) return;
      renderBanner(brains);
      renderList(brains);
    }, 3000);

    function renderBanner(brains) {
      const inline = brains.filter((b) => b.source === 'inline');
      if (!inline.length) { mount(banner); return; }
      mount(banner, h('div.callout.info',
        icon('info', 'callout-icon'),
        h('div.callout-body',
          h('strong', `Move ${plural(inline.length, 'brain')} to folder manifests`),
          h('div', 'These brains are still configured inside config.json. Moving them writes a brain.json into each brain’s folder, so a folder carries its own settings and can be backed up and restored on its own. Ids stay the same, so history and sessions carry over.'),
          h('div.actions', btn('Review the move…', { kind: 'primary', small: true, onClick: () => migrate() })))));
    }

    async function migrate() {
      const rep = await post('/api/brains/migrate', { dry_run: true });
      const rows = (rep && rep.brains) || [];
      const writable = rows.filter((r) => r.action === 'written' || r.action === 'kept');
      const label = { written: ['Will write brain.json', 'ok'], kept: ['brain.json already matches', 'ok'], conflict: ['Conflict', 'bad'], skipped: ['Skipped', 'warn'] };
      const body = h('div',
        h('p', 'This is what will happen. Nothing has changed yet.'),
        h('div.report', rows.map((r) => {
          const [l, k] = label[r.action] || [r.action, 'neutral'];
          return h('div.report-row',
            h('div.report-main', h('strong', r.id), mono(r.dir)),
            pill(l, k),
            r.detail ? h('div.muted.small', r.detail) : null,
            r.warning ? h('div.warn-text.small', icon('alert'), ' ', r.warning) : null);
        })),
        rows.some((r) => r.action === 'conflict') ? callout('warn', null, 'Brains with a conflict stay in config.json. Remove or merge the existing brain.json by hand, then try again — or run ', h('code.mono', 'whatsapp-claude-bridge migrate --force'), ' to overwrite it.') : null);
      const result = await openDialog({
        title: 'Move brains to their folders',
        body,
        wide: true,
        actions: [
          { label: 'Cancel', value: null },
          writable.length ? {
            label: `Move ${plural(writable.length, 'brain')}`,
            kind: 'primary',
            onClick: async () => post('/api/brains/migrate', { dry_run: false }),
          } : null,
        ].filter(Boolean),
      });
      if (result && result.brains) {
        const moved = result.brains.filter((r) => r.action === 'written' || r.action === 'kept').length;
        toast(`Moved ${plural(moved, 'brain')} to brain.json`, 'ok');
        memo.brains = '';
        poller.kick();
      }
    }

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
      h('div.bc-pills',
        brainStatusPill(b.status),
        b.source === 'inline' ? badge('config.json', 'neutral', 'Configured inline in config.json') : null)),
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
