// Chats: who is waiting for access, the account's groups, and the allow lists.

import { get, post, put } from './api.js';
import { store, loadGroups, rememberName, setIgnoredCount } from './state.js';
import {
  h, mount, icon, pill, badge, btn, timeAgo, mono, plural, empty, changed, pageHeader, spinner,
  select, textInput, confirmDialog, toast, phone, callout, runBusy, shortJid, chatName,
} from './ui.js';

export default {
  title: 'Chats',
  render(root, ctx) {
    const memo = {};
    const choice = new Map(); // jid → chosen brain in a select, kept across refreshes
    let brains = [];
    let cfg = null;

    const waitingBox = h('div', spinner());
    const groupsBox = h('div', spinner());
    const numbersBox = h('div');
    const allowedGroupsBox = h('div');
    const refreshGroups = btn('Refresh', { small: true, kind: 'ghost', icon: 'refresh', onClick: async () => { await loadGroups(true); groupsLoaded = true; memo.groups = ''; paintGroups(); } });

    mount(root,
      pageHeader('Chats', 'Decide who can talk to the bridge and which brain answers each group.'),
      h('section.section',
        h('div.section-head', h('h2.section-title', 'Waiting for access')),
        h('p.section-sub.muted', 'People and groups that wrote to the bridge but are not allowed. Their messages were not read.'),
        waitingBox),
      h('section.section',
        h('div.section-head', h('h2.section-title', 'Your WhatsApp groups'), refreshGroups),
        h('p.section-sub.muted', 'Groups the bridge’s account is in. Allow a group and pick the brain that answers it.'),
        groupsBox),
      h('div.grid.two.lists-grid',
        h('section.card',
          h('div.card-head', h('div.card-title', icon('user'), h('h2', 'Allowed numbers'))),
          numbersBox),
        h('section.card',
          h('div.card-head', h('div.card-title', icon('users'), h('h2', 'Allowed groups'))),
          allowedGroupsBox)));

    const brainOptions = (withDefault) => [
      ...(withDefault ? [['', defaultLabel()]] : []),
      ...brains.filter((b) => b.id).map((b) => [b.id, b.name && b.name !== b.id ? `${b.name} (${b.id})` : b.id]),
    ];
    const defaultLabel = () => {
      const d = brains.find((b) => b.manifest && b.manifest.routing && b.manifest.routing.default);
      return d ? `Default brain (${d.name || d.id})` : 'Default brain';
    };
    const brainSelect = (jid, withDefault, current = '') => {
      const v = choice.has(jid) ? choice.get(jid) : current;
      const s = select(brainOptions(withDefault), v, { 'aria-label': 'Brain for this group', class: 'small-select' });
      s.addEventListener('change', () => choice.set(jid, s.value));
      return s;
    };
    const explicitOwner = (jid) => {
      const b = brains.find((x) => x.manifest && x.manifest.routing && (x.manifest.routing.groups || []).includes(jid));
      return b ? b.id : '';
    };

    // ── waiting ──
    ctx.poll(async () => {
      const [ignored, br] = await Promise.all([get('/api/chats/ignored'), get('/api/brains')]);
      const brainsChanged = changed(memo, 'brains', br);
      brains = br;
      for (const c of ignored) if (c.kind === 'group') rememberName(c.jid, c.name);
      setIgnoredCount(ignored.length);
      if (!changed(memo, 'ignored', ignored) && !brainsChanged) return;
      paintWaiting(ignored);
      if (brainsChanged) { memo.groups = ''; paintGroups(); }
    }, 3000);

    function paintWaiting(list) {
      if (!list.length) {
        mount(waitingBox, h('div.card', empty('Nobody is waiting', 'When someone who is not allowed writes to the bridge — or the bridge is added to a group — they appear here so you can let them in.')));
        return;
      }
      mount(waitingBox, h('div.waiting-list', list.map((c) => {
        const isGroup = c.kind === 'group';
        const title = isGroup ? (c.name || 'Unnamed group') : (c.push_name || phone(c.sender) || shortJid(c.jid));
        const sel = isGroup ? brainSelect(c.jid, true) : null;
        const allow = btn(isGroup ? 'Allow' : 'Allow this number', {
          kind: 'primary', small: true, icon: 'check',
          onClick: async () => {
            const body = isGroup ? { jid: c.jid, kind: 'group', brain: sel.value } : { jid: c.jid, kind: 'dm', number: c.sender };
            await post('/api/chats/allow', body);
            toast(isGroup ? `Allowed ${title}` : `Allowed ${phone(c.sender) || title}`, 'ok');
            memo.ignored = '';
            memo.cfg = '';
            pollAll();
          },
        });
        const dismiss = btn('Dismiss', {
          small: true, kind: 'ghost',
          onClick: async () => {
            await post('/api/chats/dismiss', { jid: c.jid });
            toast('Dismissed — it stays hidden even if it writes again', 'info');
            memo.ignored = '';
            pollAll();
          },
        });
        return h('div.card.waiting-row',
          h('div.waiting-icon', icon(isGroup ? 'users' : 'user')),
          h('div.waiting-main',
            h('div.waiting-title', h('strong', title), badge(isGroup ? 'group' : 'direct message', 'neutral')),
            h('div.muted.small',
              isGroup && c.push_name ? `Last from ${c.push_name} · ` : '',
              !isGroup && c.push_name && c.sender ? `${phone(c.sender)} · ` : '',
              plural(c.count, 'message'), ' · last ', timeAgo(c.last_seen)),
            h('div.small', mono(c.jid))),
          h('div.waiting-actions', sel, allow, dismiss));
      })));
    }

    // ── groups ──
    let groupsLoaded = false;
    ctx.poll(async () => {
      await loadGroups();
      groupsLoaded = true;
      paintGroups();
    }, 30000);

    function paintGroups() {
      if (!groupsLoaded) return;
      const groups = store.groups;
      if (!changed(memo, 'groups', { groups, err: store.groupsError, brains: memo.brains })) return;
      if (!groups) {
        mount(groupsBox, h('div.card', empty('Groups are not available', store.groupsError ? `WhatsApp says: ${store.groupsError}. Connect WhatsApp to see the groups the bridge is in.` : 'Connect WhatsApp to see the groups the bridge is in.')));
        return;
      }
      if (!groups.length) {
        mount(groupsBox, h('div.card', empty('No groups', 'The bridge’s account is not in any group yet. Add it to a group on your phone; the group then also appears under “Waiting for access” once someone writes.')));
        return;
      }
      mount(groupsBox, h('div.table-wrap.card.flush', h('table.table.groups-table',
        h('thead', h('tr', h('th', 'Group'), h('th', 'People'), h('th', 'Status'), h('th', 'Brain'), h('th.right', ''))),
        h('tbody', groups.map((g) => {
          const owner = explicitOwner(g.jid);
          const sel = brainSelect(g.jid, !g.allowed, owner);
          const action = g.allowed
            ? btn('Route', {
              small: true,
              title: 'Send this group to the chosen brain',
              onClick: async () => {
                if (!sel.value || sel.value === owner) { toast('Pick a different brain first', 'info'); return; }
                await post('/api/chats/allow', { jid: g.jid, kind: 'group', brain: sel.value });
                toast(`${g.name || 'Group'} now goes to ${sel.value}`, 'ok');
                choice.delete(g.jid);
                await loadGroups(true);
                memo.groups = '';
                pollAll();
              },
            })
            : btn('Allow', {
              small: true, kind: 'primary', icon: 'check',
              onClick: async () => {
                await post('/api/chats/allow', { jid: g.jid, kind: 'group', brain: sel.value });
                toast(`Allowed ${g.name || 'the group'}`, 'ok');
                choice.delete(g.jid);
                await loadGroups(true);
                memo.groups = '';
                memo.cfg = '';
                pollAll();
              },
            });
          return h('tr',
            h('td', h('div', h('strong', g.name || 'Unnamed group')), h('div.small', mono(g.jid))),
            h('td', g.participants ? String(g.participants) : '—'),
            h('td', g.allowed ? pill('Allowed', 'ok') : pill('Not allowed', 'neutral')),
            h('td', g.allowed && !owner && g.brain ? h('div.muted.small', `via default: ${g.brain}`) : null, sel),
            h('td.right', action));
        })))));
    }

    // ── allow lists (config) ──
    ctx.poll(async () => {
      const c = await get('/api/config');
      cfg = c;
      if (!changed(memo, 'cfg', { file: c.config, locked: c.env_locked, eff: c.effective.allowed_numbers, eff2: c.effective.allowed_groups, any: c.effective.allow_any_dm })) return;
      paintLists();
    }, 5000);

    function paintLists() {
      const file = cfg.config || {};
      const eff = cfg.effective || {};
      const locked = cfg.env_locked || {};
      const numbers = locked.allowed_numbers ? (eff.allowed_numbers || []) : (file.allowed_numbers || []);
      const groups = locked.allowed_groups ? (eff.allowed_groups || []) : (file.allowed_groups || []);
      // Only an explicit true opens an empty list; unset means nobody.
      const anyDM = eff.allow_any_dm === true;

      mount(numbersBox,
        locked.allowed_numbers ? callout('info', null, `Set by the environment variable ${locked.allowed_numbers}. Change it there.`) : null,
        numbers.length ? h('ul.plain-list', numbers.map((n) => h('li',
          h('div', mono(phone(n))),
          locked.allowed_numbers ? null : removeBtn(`Remove ${phone(n)}?`, 'They can no longer message the bridge directly. Messages they already sent are kept.', (c) => { c.allowed_numbers = (c.allowed_numbers || []).filter((x) => x !== n); }))))
          : h('p.muted', anyDM ? 'Empty — so anyone who messages this WhatsApp account can use the assistant. Add your number.' : 'Empty — nobody can send direct messages yet.'),
        locked.allowed_numbers ? null : addForm({
          placeholder: '15551234567',
          label: 'Add number',
          help: 'Digits with country code, no + or spaces.',
          normalize: (v) => v.replace(/[\s()+.-]/g, ''),
          validate: (v) => (/^\d{6,15}$/.test(v) ? '' : 'Use digits only, with the country code (e.g. 15551234567).'),
          apply: (c, v) => { c.allowed_numbers = [...new Set([...(c.allowed_numbers || []), v])]; },
        }));

      mount(allowedGroupsBox,
        locked.allowed_groups ? callout('info', null, `Set by the environment variable ${locked.allowed_groups}. Change it there.`) : null,
        groups.length ? h('ul.plain-list', groups.map((g) => h('li',
          h('div', chatName(g) ? h('div', h('strong', chatName(g))) : null, h('div.small', mono(g))),
          h('span.muted.small', explicitOwner(g) ? `→ ${explicitOwner(g)}` : '→ default'),
          locked.allowed_groups ? null : removeBtn('Stop answering in this group?', 'The bridge ignores the group from now on. Its brain keeps the history.', (c) => { c.allowed_groups = (c.allowed_groups || []).filter((x) => x !== g); }))))
          : h('p.muted', 'No groups allowed. The bridge ignores every group until you allow one.'),
        locked.allowed_groups ? null : addForm({
          placeholder: '120363000000000000@g.us',
          label: 'Add group',
          help: 'A group JID ends in @g.us. Easier: allow groups from the lists above.',
          normalize: (v) => v.trim(),
          validate: (v) => (/^[0-9-]+@g\.us$/.test(v) ? '' : 'A group JID looks like 120363000000000000@g.us.'),
          apply: (c, v) => { c.allowed_groups = [...new Set([...(c.allowed_groups || []), v])]; },
        }));
    }

    function removeBtn(title, message, mutate) {
      return btn('', {
        kind: 'ghost', small: true, icon: 'trash', title: 'Remove',
        onClick: async () => {
          if (!await confirmDialog({ title, message, confirm: 'Remove', danger: true })) return;
          await saveConfig(mutate);
          toast('Removed', 'ok');
        },
      });
    }

    function addForm({ placeholder, label, help, normalize, validate, apply }) {
      const input = textInput({ placeholder, class: 'mono', 'aria-label': label });
      const err = h('div.form-error.small', { hidden: true });
      const submit = btn(label, { small: true, icon: 'plus', type: 'submit' });
      const form = h('form.add-form', h('div.inline-add', input, submit), err, h('div.help', help));
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        const v = normalize(input.value);
        const msg = validate(v);
        err.hidden = !msg;
        err.textContent = msg;
        if (msg) return;
        runBusy(submit, async () => {
          await saveConfig((c) => apply(c, v));
          input.value = '';
          toast('Added', 'ok');
        });
      });
      return form;
    }

    async function saveConfig(mutate) {
      const fresh = await get('/api/config');
      const c = JSON.parse(JSON.stringify(fresh.config || {}));
      mutate(c);
      try {
        await put('/api/config', { etag: fresh.etag, config: c });
      } catch (e) {
        if (e.status === 409) throw new Error('The configuration changed at the same moment. Try again.');
        throw e;
      }
      memo.cfg = '';
      pollAll();
    }

    function pollAll() {
      refresh();
    }

    async function refresh() {
      try {
        const [ignored, br, c] = await Promise.all([get('/api/chats/ignored'), get('/api/brains'), get('/api/config')]);
        brains = br;
        memo.brains = JSON.stringify(br);
        cfg = c;
        memo.ignored = JSON.stringify(ignored);
        setIgnoredCount(ignored.length);
        paintWaiting(ignored);
        memo.cfg = '';
        paintLists();
        memo.groups = '';
        paintGroups();
      } catch (e) {
        if (e.status !== 401) toast(e.message);
      }
    }
  },
};
