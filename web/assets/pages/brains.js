// Brains: a card per brain with its routing, live state and issues.

import { del, qs } from '../app/api.js';
import { brainsResource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { routingSummary, hasDMBrain, issueLabel } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { btn } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { toast } from '../components/toast.js';
import { mono, pageHeader, spinner } from '../components/common.js';
import { brainStatusPill, runtimeSummary, firstBrainHero } from '../components/brain.js';

export default {
  title: 'Brains',
  render(root, ctx) {
    const box = h('div', spinner());
    const cards = h('div.brain-cards');

    mount(root,
      pageHeader('Brains', 'Each brain is a folder with its own instructions, memory and history. Messages are routed to a brain by chat.',
        btn('Add brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' })),
      box);

    ctx.follow(brainsResource, (brains) => {
      if (!brains.length) {
        paint(box, 'none', firstBrainHero);
        return;
      }
      paint(box, 'list', () => cards);
      // An entry without an id (no approved brain.json) is known by its
      // folder. Whether a brain handles DMs is in each card, for the
      // fallback brain's, which gets them when none does.
      const noDMBrain = !hasDMBrain(brains);
      reconcile(cards, brains.map((b) => ({ b, noDMBrain })), ({ b }) => b.id || `path:${b.path}`, brainCard);
    }, 3000);
  },
};

function brainCard({ b, noDMBrain }) {
  const rt = b.runtime;
  const m = b.manifest || {};
  const routing = (rt && rt.routing) || m.routing;
  const href = b.id ? `#/brains/${encodeURIComponent(b.id)}` : null;
  const body = [
    h('div.bc-head',
      h('div.bc-title',
        h('h3', b.name || b.id || 'Unnamed brain'),
        b.id ? h('code.mono.muted', b.id) : null),
      h('div.bc-pills', brainStatusPill(b.status))),
    h('div.bc-path', icon('folder'), mono(b.path)),
    h('div.bc-facts',
      h('span', icon('chats'), routingSummary(routing, { fallback: b.fallback, noDMBrain })),
      m.model ? h('span', icon('sparkle'), m.model) : null,
      m.effort ? h('span', `effort: ${m.effort}`) : null,
      m.backup ? h('span', icon('backup'), m.backup.enabled ? 'Backed up' : 'No backup') : null),
    rt ? h('div.bc-runtime', runtimeSummary(rt))
      : (b.status === 'ok' ? null : h('div.bc-runtime', h('span.muted.small', 'Not running'))),
    (b.issues || []).length ? h('ul.issue-list', b.issues.map((is) => h('li', h('strong', `${issueLabel(is.kind)}: `), is.detail))) : null,
  ];
  if (href) return h('a.card.brain-card.link-card', { href }, body, icon('right', 'row-chevron'));
  return h('div.card.brain-card', body,
    h('p.muted.small', 'This entry has no approved manifest, so it cannot be opened here. Remove it, then add the folder again.'),
    h('div.actions', btn('Remove entry…', {
      kind: 'danger', small: true, icon: 'trash',
      onClick: async () => {
        const ok = await confirmDialog({
          title: 'Remove this entry?',
          message: h('p', 'The bridge forgets ', mono(b.path), '. Nothing in the folder is deleted.'),
          confirm: 'Remove entry',
          danger: true,
        });
        if (!ok) return;
        await del(`/api/brain-dirs${qs({ path: b.path })}`);
        toast('Entry removed', 'ok');
        await brainsResource.refresh();
      },
    })));
}
