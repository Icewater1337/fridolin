// Routing controls shared by the add-brain wizard and the brain page:
// direct messages, default brain, and which groups the brain answers.

import { store, loadGroups } from './state.js';
import { h, mount, toggle, field, textInput, btn, spinner, mono, isGroupJid, nextId, loading } from './ui.js';

/**
 * routingEditor({routing, selfId, brains, allowedGroups, onChange})
 * brains: the /api/brains list (to see which brain holds DMs, default, groups).
 * Returns an element with getValue() → {handles_dms, default, groups}.
 */
export function routingEditor({ routing = {}, selfId = '', brains = [], allowedGroups = [], onChange }) {
  const others = brains.filter((b) => b.id && b.id !== selfId && b.manifest);
  const dmOwner = others.find((b) => b.manifest.routing && b.manifest.routing.handles_dms);
  const defOwner = others.find((b) => b.manifest.routing && b.manifest.routing.default);
  const claimed = new Map();
  for (const b of others) for (const g of (b.manifest.routing && b.manifest.routing.groups) || []) claimed.set(g, b.name || b.id);

  const changedFn = () => { if (onChange) onChange(); };
  const dmWarn = h('div.help.warn-text', { hidden: true });
  const defWarn = h('div.help.warn-text', { hidden: true });
  const dm = toggle({
    checked: !!routing.handles_dms,
    label: 'Handles direct messages',
    onChange: (v) => { dmWarn.hidden = !(v && dmOwner); changedFn(); },
  });
  const def = toggle({
    checked: !!routing.default,
    label: 'Default for unassigned chats',
    onChange: (v) => { defWarn.hidden = !(v && defOwner); changedFn(); },
  });
  if (dmOwner) dmWarn.textContent = `“${dmOwner.name || dmOwner.id}” handles direct messages now. Only one brain can — turn it off there first, or saving will fail.`;
  if (defOwner) defWarn.textContent = `“${defOwner.name || defOwner.id}” is the default now. Only one brain can be — turn it off there first, or saving will fail.`;
  dmWarn.hidden = !(routing.handles_dms && dmOwner);
  defWarn.hidden = !(routing.default && defOwner);

  const selected = new Set(routing.groups || []);
  const extra = new Set(); // JIDs typed in by hand
  const groupsBox = h('div.group-picks', h('div.muted.small', loading('Loading your WhatsApp groups…')));
  const note = h('div.help');
  const addIn = textInput({ placeholder: '120363000000000000@g.us', class: 'mono', 'aria-label': 'Group JID' });
  const addErr = h('div.form-error.small', { hidden: true });
  const addBtn = btn('Add', { small: true, icon: 'plus' });
  const addGroup = () => {
    const v = addIn.value.trim();
    addErr.hidden = true;
    if (!v) return;
    if (!isGroupJid(v)) { addErr.hidden = false; addErr.textContent = 'A group JID ends in @g.us.'; return; }
    extra.add(v);
    selected.add(v);
    addIn.value = '';
    paintGroups();
    changedFn();
  };
  addBtn.addEventListener('click', addGroup);
  addIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); addGroup(); } });

  function paintGroups() {
    const rows = new Map();
    for (const g of store.groups || []) {
      if (g.allowed || selected.has(g.jid)) rows.set(g.jid, { jid: g.jid, name: g.name, participants: g.participants, allowed: g.allowed });
    }
    for (const jid of allowedGroups || []) if (!rows.has(jid)) rows.set(jid, { jid, name: store.groupNames.get(jid) || '', allowed: true });
    for (const jid of [...selected, ...extra]) if (!rows.has(jid)) rows.set(jid, { jid, name: store.groupNames.get(jid) || '', allowed: (allowedGroups || []).includes(jid) });
    const list = [...rows.values()].sort((a, b) => (a.name || a.jid).localeCompare(b.name || b.jid));
    if (!list.length) {
      mount(groupsBox, h('div.muted.small.group-empty', 'No allowed groups yet. Allow a group on the ', h('a', { href: '#/chats' }, 'Chats'), ' page, or add its JID below.'));
    } else {
      mount(groupsBox, list.map((g) => {
        const owner = claimed.get(g.jid);
        const id = nextId('grp');
        const cb = h('input', { type: 'checkbox', id, checked: selected.has(g.jid), disabled: !!owner && !selected.has(g.jid) });
        cb.addEventListener('change', () => { if (cb.checked) selected.add(g.jid); else selected.delete(g.jid); changedFn(); });
        return h('label.group-pick', { for: id, class: owner && !selected.has(g.jid) ? 'disabled' : '' },
          cb,
          h('span.group-pick-main',
            h('span.group-pick-name', g.name || 'Unnamed group'),
            mono(g.jid)),
          h('span.group-pick-note.muted.small',
            owner ? `routed to ${owner}` : '',
            !owner && !g.allowed ? 'not allowed yet' : '',
            !owner && g.allowed && g.participants ? `${g.participants} people` : ''));
      }));
    }
    note.textContent = store.groups === null && store.groupsError
      ? `Group names are unavailable (${store.groupsError}). You can still pick allowed groups by JID.`
      : 'Only allowed groups are listed. A group can belong to one brain; members still need to tag the bridge (see Settings → Access → Triggers).';
  }

  loadGroups().finally(paintGroups);

  const el = h('div.routing-editor',
    h('div.field', dm, h('div.help', 'All direct messages from allowed numbers go to this brain. Only one brain can do this.'), dmWarn),
    h('div.field', def, h('div.help', 'Allowed groups that no brain claims come here — and direct messages too, when no brain handles them.'), defWarn),
    field({ label: 'Groups', control: groupsBox, wide: true }),
    note,
    h('div.field', h('label.field-label', { for: addIn.id }, 'Add a group by its JID'), h('div.inline-add', addIn, addBtn)),
    addErr);
  el.getValue = () => ({ handles_dms: dm.input.checked, default: def.input.checked, groups: [...selected] });
  return el;
}
