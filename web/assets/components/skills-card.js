// A brain's skills and their policy: each skill's risk, the steps that
// stop for your yes, its vault paths and triggers, as approved. A skill
// the brain wrote or changed itself counts as read-only until you
// approve it here, or edit its policy, which writes it into the file.

import { get, post, put } from '../app/api.js';
import { h, paint } from '../lib/dom.js';
import { btn } from './buttons.js';
import { openDialog } from './dialog.js';
import { field, linesArea, linesOf, select } from './fields.js';
import { toast } from './toast.js';
import { badge, callout, empty, loading, mono } from './common.js';
import { riskBadge } from './policy.js';

const STATUS = {
  approved: ['approved', 'ok', 'The policy in the file is the one you approved.'],
  starter: ['starter', 'ok', 'As Fridolin wrote it into the new brain.'],
  changed: ['changed', 'warn', 'The file declares another policy; the one you approved applies until you approve this one.'],
  new: ['not approved', 'warn', 'Counts as read-only, its triggers for nothing, until you approve it.'],
  legacy: ['no frontmatter', 'neutral', 'A skill from before the format: read-only, no triggers.'],
  invalid: ['invalid', 'bad', 'Its frontmatter does not read.'],
};

const list = (v) => (v && v.length ? v.join(', ') : '—');

function policyLines(p) {
  return h('dl.kv',
    h('dt', 'Risk'), h('dd', riskBadge(p.risk)),
    h('dt', 'Asks before'), h('dd', list(p.approve)),
    h('dt', 'Triggers'), h('dd', list(p.triggers)),
    h('dt', 'Vault'), h('dd', list(p.vault)));
}

/** skillsCard(brainId, {signal}) is the card of brainId's skills. */
export function skillsCard(brainId, { signal } = {}) {
  const el = h('div.stack', h('div.card', loading('Reading skills…')));
  const base = `/api/brains/${encodeURIComponent(brainId)}/skills`;

  async function refresh() {
    let data;
    try {
      data = await get(base, { signal });
    } catch (e) {
      if (e && e.status === 404) { paint(el, 'gone', () => h('div.card', empty('Not running', 'Skills show once the brain runs.'))); return; }
      throw e;
    }
    render(data);
  }

  function edit(s) {
    const p = s.declared;
    const risk = select((s.risks || ['read', 'write', 'irreversible']).map((r) => [r, r]), p.risk || 'read');
    const approve = linesArea(p.approve, { rows: 3 });
    const triggers = linesArea(p.triggers, { rows: 3 });
    const vault = linesArea(p.vault, { rows: 3 });
    return openDialog({
      title: `Policy of ${s.name}`,
      body: h('div.form',
        h('p.muted.small', 'Saving writes this into the skill’s frontmatter (metadata.fridolin) and approves it.'),
        field({ label: 'Risk', control: risk, help: 'read runs at once; write runs and reports; irreversible steps stop for your yes.' }),
        field({ label: 'Steps that ask first', control: approve, help: 'One step name a line, as the skill names them.' }),
        field({ label: 'Triggers', control: triggers, help: 'Words or phrases, one a line, that make a message a run of this skill.' }),
        field({ label: 'Vault paths', control: vault, help: 'The only vault paths it uses, globs allowed, one a line.' })),
      actions: [
        { label: 'Cancel', value: null },
        {
          label: 'Save and approve', kind: 'primary',
          onClick: async () => {
            await put(`${base}/policy`, { path: s.path, risk: risk.value, approve: linesOf(approve), triggers: linesOf(triggers), vault: linesOf(vault) });
            toast('Skill policy saved', 'ok');
            await refresh();
            return true;
          },
        },
      ],
    });
  }

  function skillCard(s, risks) {
    const [label, kind, why] = STATUS[s.status] || [s.status, 'neutral', ''];
    const declared = { ...s.fridolin, risks };
    const actions = [];
    if (s.status === 'new' || s.status === 'changed') {
      actions.push(btn('Approve as declared', {
        kind: 'primary', small: true, icon: 'check',
        onClick: async () => {
          await post(`${base}/approve`, { path: s.path, hash: s.hash });
          toast(`${s.name} approved`, 'ok');
          await refresh();
        },
      }));
    }
    if (s.status !== 'invalid') {
      actions.push(btn('Edit policy…', { small: true, icon: 'edit', onClick: () => edit({ ...s, declared, risks }) }));
    }
    return h('article.card.skill-card',
      h('div.act-meta', h('strong', s.name), mono(s.path), badge(label, kind, why)),
      s.description ? h('p.muted.small', s.description) : null,
      s.error ? callout('bad', 'The frontmatter does not read', h('p', s.error)) : null,
      h('div.skill-policy',
        h('div', h('div.small.muted', 'Applies'), policyLines(s.effective)),
        s.status === 'changed' || s.status === 'new' ? h('div', h('div.small.muted', 'The file declares'), policyLines(s.fridolin)) : null),
      actions.length ? h('div.actions', actions) : null);
  }

  function render(data) {
    const skills = (data && data.skills) || [];
    if (!skills.length) {
      paint(el, 'none', () => h('div.card', empty('No skills', 'Skills are markdown files in the brain’s skills/ folder.')));
      return;
    }
    paint(el, skills.map((s) => `${s.path}:${s.status}:${s.hash}`).join(','), () => skills.map((s) => skillCard(s, data.risks)));
  }

  refresh().catch((e) => paint(el, 'error', () => h('div.card', callout('bad', 'Could not read the skills', h('p', e.message || String(e))))));
  return el;
}
