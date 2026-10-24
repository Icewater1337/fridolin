// Chats: who is waiting for access, the account's groups, and the allow
// lists. This is the one page that edits the allow lists and who is heard
// in each allowed group (group_members); every change goes through the
// bridge (/api/chats/allow, /api/chats/revoke and /api/chats/members),
// which checks it, and the page shows the bridge's answer when it refuses
// one.

import { post, errorStatus } from '../app/api.js';
import { store, loadGroups, rememberName } from '../app/store.js';
import { brainsResource, configResource, ignoredResource } from '../app/resources.js';
import { h, mount, paint, reconcile, syncOptions } from '../lib/dom.js';
import { plural } from '../lib/format.js';
import { brainLabel, defaultBrainOption } from '../lib/brain.js';
import { phone, phoneDigits, shortJid } from '../lib/jid.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn, runBusy } from '../components/buttons.js';
import { confirmDialog, openDialog } from '../components/dialog.js';
import { field, listEditor, select, textInput } from '../components/fields.js';
import { pill, badge, timeAgo, mono, empty, pageHeader, spinner } from '../components/common.js';
import { chatName } from '../components/chat.js';
import { errorMessage } from '../lib/errors.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Brain, ConfigFile, ConfigResponse, IgnoredChat, WhatsAppGroup } from '../../types/api.js' */

/**
 * A row of the waiting chats, with the brains a group can go to.
 * @typedef {{c: IgnoredChat, options: [string, string][] | null}} WaitingItem
 */
/**
 * A row of the groups table: owner is the brain whose routing names it, '' for none.
 * @typedef {{g: WhatsAppGroup, owner: string, options: [string, string][]}} GroupItem
 */

export default /** @satisfies {PageModule} */ ({
  title: 'Chats',
  render(root, ctx) {
    /** @type {Brain[] | null} */
    let brains = null;
    /** @type {IgnoredChat[] | null} */
    let ignored = null;
    /** @type {ConfigResponse | null} */
    let cfg = null;

    const waitingBox = h('div', spinner());
    const waitingList = h('div.waiting-list');
    const groupsBox = h('div', spinner());
    const groupRows = h('tbody');
    const numbersBox = h('div');
    const numbersList = h('ul.plain-list');
    const allowedGroupsBox = h('div');
    const allowedGroupsList = h('ul.plain-list');
    // Refresh has the bridge ask WhatsApp; the other reloads take its list.
    const refreshGroups = btn('Refresh', { small: true, kind: 'ghost', icon: 'refresh', onClick: () => loadGroups(true, { fresh: true }) });

    // The add boxes are built once, so what is typed in them stays while
    // the lists refresh.
    const addNumber = addForm({
      placeholder: '15551234567',
      label: 'Add number',
      help: 'With the country code, e.g. 15551234567; a +, spaces and dashes are fine.',
      add: (v) => post('/api/chats/allow', { kind: 'dm', number: v }),
    });
    const addGroup = addForm({
      placeholder: '120363000000000000@g.us',
      label: 'Add group',
      help: 'A group JID looks like 120363000000000000@g.us. Easier: allow groups from the lists above.',
      add: async (v) => { await post('/api/chats/allow', { kind: 'group', jid: v }); await loadGroups(true); },
    });

    mount(root,
      pageHeader('Chats', 'Decide who can talk to the bridge and which brain answers each group.'),
      h('section.section',
        h('div.section-head', h('div.section-heading',
          h('h2.section-title', 'Waiting for access'),
          h('p.section-sub.muted', 'People and groups that wrote to the bridge but are not allowed. Their messages were not read.'))),
        waitingBox),
      h('section.section',
        h('div.section-head', h('div.section-heading',
          h('h2.section-title', 'Your WhatsApp groups'),
          h('p.section-sub.muted', 'Groups the bridge’s account is in. Allow a group and pick the brain that answers it.')), refreshGroups),
        groupsBox),
      h('div.grid.two.lists-grid',
        h('section.card',
          h('div.card-head', h('div.card-title', icon('user'), h('h2', 'Allowed numbers'))),
          numbersBox, addNumber),
        h('section.card',
          h('div.card-head', h('div.card-title', icon('users'), h('h2', 'Allowed groups'))),
          allowedGroupsBox, addGroup)));

    // The shell follows the waiting chats (for the sidebar badge) and
    // reloads the groups; the page shares those polls instead of running
    // its own.
    const refreshAll = () => Promise.all([ignoredResource.refresh(), brainsResource.refresh(), configResource.refresh()]);

    // A group that names no brain goes to the fallback brain, which is
    // the default brain or else the first: the option names it either way.
    /** @type {(withDefault: boolean) => [string, string][]} */
    const brainOptions = (withDefault) => [
      ...(withDefault ? [/** @type {[string, string]} */ (['', defaultBrainOption(brains)])] : []),
      ...(brains || []).filter((b) => b.id).map((b) => /** @type {[string, string]} */ ([b.id, brainLabel(b)])),
    ];
    /** @param {string} jid */
    const explicitOwner = (jid) => {
      const b = (brains || []).find((x) => x.manifest && x.manifest.routing && (x.manifest.routing.groups || []).includes(jid));
      return b ? b.id : '';
    };

    ctx.follow(ignoredResource, (list) => {
      ignored = list || [];
      for (const c of ignored) if (c.kind === 'group') rememberName(c.jid, c.name);
      paintWaiting();
    }, 3000);
    // The waiting rows, the groups table and the allow lists show which
    // brain owns a group.
    ctx.follow(brainsResource, (list) => {
      brains = list || [];
      paintWaiting();
      paintGroups();
      paintLists();
    }, 3000);
    ctx.follow(configResource, (c) => {
      cfg = c;
      paintLists();
    }, 5000);
    // The shell reloads the groups every minute; the page shows them once known.
    loadGroups();
    ctx.watch(store, (s) => s.groups, () => paintGroups());
    ctx.watch(store, (s) => s.groupsError, () => paintGroups());
    // The allowed groups show their names once the groups have loaded.
    ctx.watch(store, (s) => s.names, () => paintLists());

    // ── waiting ──
    function paintWaiting() {
      if (!ignored || !brains) return;
      if (!ignored.length) {
        paint(waitingBox, 'none', () => h('div.card', empty('Nobody is waiting', 'When someone who is not allowed writes to the bridge — or the bridge is added to a group — they appear here so you can let them in.')));
        return;
      }
      paint(waitingBox, 'list', () => waitingList);
      reconcile(waitingList, ignored.map((c) => ({ c, options: c.kind === 'group' ? brainOptions(true) : null })), (x) => x.c.jid, waitingRow);
    }

    // A row keeps its brain choice while it refreshes.
    /** @param {WaitingItem} item */
    function waitingRow(item) {
      let { c } = item;
      const isGroup = c.kind === 'group';
      const title = () => (isGroup ? (c.name || 'Unnamed group') : (c.push_name || phone(c.sender) || shortJid(c.jid)));
      const sel = isGroup ? select([], '', { 'aria-label': 'Brain for this group', class: 'small-select' }) : null;
      const head = h('div.waiting-title');
      const meta = h('div.muted.small');
      const allow = btn(isGroup ? 'Allow' : 'Allow this number', {
        kind: 'primary', small: true, icon: 'check',
        onClick: async () => {
          const body = isGroup ? { jid: c.jid, kind: 'group', brain: sel.value } : { jid: c.jid, kind: 'dm', number: c.sender };
          await post('/api/chats/allow', body);
          toast(isGroup ? `Allowed ${title()}` : `Allowed ${phone(c.sender) || title()}`, 'ok');
          if (isGroup) await loadGroups(true);
          await refreshAll();
        },
      });
      const dismiss = btn('Dismiss', {
        small: true, kind: 'ghost',
        onClick: async () => {
          await post('/api/chats/dismiss', { jid: c.jid });
          toast('Dismissed — it stays hidden even if it writes again', 'info');
          await ignoredResource.refresh();
        },
      });
      const row = h('div.card.waiting-row',
        h('div.waiting-icon', icon(isGroup ? 'users' : 'user')),
        h('div.waiting-main', head, meta, h('div.small', mono(c.jid))),
        h('div.waiting-actions', sel, allow, dismiss));
      /** @param {WaitingItem} next */
      const update = (next) => {
        c = next.c;
        mount(head, h('strong', title()), badge(isGroup ? 'group' : 'direct message', 'neutral'));
        mount(meta,
          isGroup && c.push_name ? `Last from ${c.push_name} · ` : '',
          !isGroup && c.push_name && c.sender ? `${phone(c.sender)} · ` : '',
          plural(c.count, 'message'), ' · last ', timeAgo(c.last_seen));
        if (sel) syncOptions(sel, next.options, '');
      };
      update(item);
      return Object.assign(row, { update });
    }

    // ── groups ──
    function paintGroups() {
      const { groups, groupsError } = store.get();
      if (!brains || (groups === null && !groupsError)) return;
      if (!groups) {
        paint(groupsBox, { unavailable: groupsError }, () => h('div.card', empty('Groups are not available', groupsError ? `WhatsApp says: ${groupsError}. Connect WhatsApp to see the groups the bridge is in.` : 'Connect WhatsApp to see the groups the bridge is in.')));
        return;
      }
      if (!groups.length) {
        paint(groupsBox, 'none', () => h('div.card', empty('No groups', 'The bridge’s account is not in any group yet. Add it to a group on your phone; the group then also appears under “Waiting for access” once someone writes.')));
        return;
      }
      paint(groupsBox, 'table', () => h('div.table-wrap.card.flush', h('table.table.groups-table',
        h('thead', h('tr', h('th', 'Group'), h('th', 'People'), h('th', 'Status'), h('th', 'Brain'), h('th.right', ''))),
        groupRows)));
      reconcile(groupRows, groups.map((g) => ({ g, owner: explicitOwner(g.jid), options: brainOptions(!g.allowed) })), (x) => x.g.jid, groupRow);
    }

    // A row keeps the brain picked in it while it refreshes, until the
    // group's brain changes (here or elsewhere), which it then shows.
    /** @param {GroupItem} item */
    function groupRow(item) {
      /** @type {GroupItem | null} */
      let cur = null;
      const sel = select([], '', { 'aria-label': 'Brain for this group', class: 'small-select' });
      const nameCell = h('td');
      const peopleCell = h('td');
      const statusCell = h('td');
      const note = h('div.muted.small');
      const actionCell = h('td.right');
      const route = btn('Route', {
        small: true,
        title: 'Send this group to the chosen brain',
        onClick: async () => {
          const { g, owner } = cur;
          if (!sel.value || sel.value === owner) { toast('Pick a different brain first', 'info'); return; }
          await post('/api/chats/allow', { jid: g.jid, kind: 'group', brain: sel.value });
          toast(`${g.name || 'Group'} now goes to ${sel.value}`, 'ok');
          await loadGroups(true);
          await refreshAll();
        },
      });
      const allow = btn('Allow', {
        small: true, kind: 'primary', icon: 'check',
        onClick: async () => {
          const { g } = cur;
          await post('/api/chats/allow', { jid: g.jid, kind: 'group', brain: sel.value });
          toast(`Allowed ${g.name || 'the group'}`, 'ok');
          await loadGroups(true);
          await refreshAll();
        },
      });
      const tr = h('tr', nameCell, peopleCell, statusCell, h('td', note, sel), actionCell);
      /** @param {GroupItem} next */
      const update = (next) => {
        const prev = cur;
        cur = next;
        const { g, owner } = next;
        mount(nameCell, h('div', h('strong', g.name || 'Unnamed group')), h('div.small', mono(g.jid)));
        peopleCell.textContent = g.participants ? String(g.participants) : '—';
        mount(statusCell, g.allowed ? pill('Allowed', 'ok') : pill('Not allowed', 'neutral'));
        note.textContent = g.allowed && !owner && g.brain ? `via default: ${g.brain}` : '';
        const picked = sel.value;
        syncOptions(sel, next.options, owner);
        if (!prev || picked === prev.owner || owner !== prev.owner) sel.value = next.options.some(([v]) => v === owner) ? owner : sel.value;
        mount(actionCell, g.allowed ? route : allow);
      };
      update(item);
      return Object.assign(tr, { update });
    }

    // ── allow lists (config) ──
    function paintLists() {
      if (!cfg || !brains) return;
      const file = /** @type {Partial<ConfigFile>} */ (cfg.config || {});
      const eff = /** @type {Partial<ConfigFile>} */ (cfg.effective || {});
      const numbers = file.allowed_numbers || [];
      const groups = file.allowed_groups || [];
      const members = file.group_members || {};
      // Only an explicit true opens an empty list; unset means nobody.
      const anyDM = eff.allow_any_dm === true;

      if (!numbers.length) {
        paint(numbersBox, { anyDM }, () => h('p.muted', anyDM
          ? ['Empty — so anyone who messages this WhatsApp account can use the assistant (', h('a', { href: '#/settings/access' }, 'Settings → Access'), '). Add your number.']
          : 'Empty — nobody can send direct messages yet.'));
      } else {
        paint(numbersBox, 'list', () => numbersList);
        reconcile(numbersList, numbers.map((n) => ({ n, last: numbers.length === 1 && anyDM })), (x) => x.n, ({ n, last }) => h('li',
          h('div', mono(phone(n))),
          removeBtn(`Remove ${phone(n)}?`,
            last ? 'They can no longer message the bridge directly. This is the last number: with “Let anyone message while the list is empty” on, anyone could message the bridge then.'
              : 'They can no longer message the bridge directly. Messages they already sent are kept.',
            { kind: 'dm', number: n }, `Removed ${phone(n)}`)));
      }

      if (!groups.length) {
        paint(allowedGroupsBox, 'none', () => h('p.muted', 'No groups allowed. The bridge ignores every group until you allow one.'));
      } else {
        paint(allowedGroupsBox, 'list', () => allowedGroupsList);
        // An entry is null for a group without one: allowed_numbers decide.
        const items = groups.map((g) => ({ g, name: chatName(g), owner: explicitOwner(g), entry: Object.hasOwn(members, g) ? members[g] : null, numbers: numbers.length, anyDM }));
        reconcile(allowedGroupsList, items, (x) => x.g, ({ g, name, owner, entry, numbers: n, anyDM: any }) => h('li',
          h('div', name ? h('div', h('strong', name)) : null, h('div.small', mono(g)), h('div.small.muted.heard', heardSummary(entry, n, any))),
          h('span.muted.small', owner ? `→ ${owner}` : '→ default'),
          btn('Members', { small: true, kind: 'ghost', icon: 'users', title: 'Who is heard in this group', onClick: () => editMembers(g, name, entry) }),
          removeBtn('Stop answering in this group?', 'The bridge ignores the group from now on. Its brain keeps the history, and allowing the group again sends it to the same brain.',
            { kind: 'group', jid: g }, 'Removed the group')));
      }
    }

    // ── who is heard in a group (group_members) ──

    // Whose messages in a group reach its brain, as the bridge decides
    // (groupSenderAllowed): its entry, or without one the allowed numbers.
    /**
     * @param {string[] | null} entry the group's group_members entry
     * @param {number} numbers how many numbers are allowed
     * @param {boolean} anyDM
     */
    function heardSummary(entry, numbers, anyDM) {
      if (entry === null) {
        if (!numbers) return anyDM ? 'Heard: every member, since anyone may message the bridge' : 'Heard: nobody, until you allow a number';
        return 'Heard: the allowed numbers';
      }
      if (entry.length === 1 && entry[0] === '*') return 'Heard: every member';
      if (!entry.length) return 'Heard: nobody';
      return `Heard: ${entry.map(phone).join(', ')}`;
    }

    // The editor checks each number as the bridge does (validateGroupMembers),
    // and the bridge checks the list again when it saves.
    /**
     * @param {string} g the group's JID
     * @param {string} name
     * @param {string[] | null} entry
     */
    function editMembers(g, name, entry) {
      const everyone = entry !== null && entry.length === 1 && entry[0] === '*';
      const mode = entry === null ? '' : (everyone ? '*' : 'list');
      const modeSel = select([
        ['', 'Whom the allowed numbers admit'],
        ['*', 'Every member of the group'],
        ['list', 'Only these numbers'],
      ], mode, { 'aria-label': 'Who is heard' });
      const modeHelp = h('div.help');
      const list = listEditor({
        values: mode === 'list' ? entry : [],
        placeholder: '15551234567',
        addLabel: 'Add number',
        validate: (v) => {
          if (v === '*') return 'Pick “Every member of the group” instead';
          return phoneDigits(v) ? '' : 'Not a phone number: use digits with the country code, e.g. 15551234567';
        },
        empty: 'No numbers: nobody but the bridge’s own account is heard.',
      });
      const listField = field({ label: 'Numbers', control: list, help: 'With the country code; a +, spaces and dashes are fine. The allowed numbers do not count here, so list your own too.' });
      const err = h('div.form-error.small', { hidden: true });
      const sync = () => {
        listField.hidden = modeSel.value !== 'list';
        modeHelp.textContent = {
          '': 'As without an entry: the allowed numbers on this page are heard, and while that list is empty and “Let anyone message” is on, anyone.',
          '*': 'Everyone in the group, also whoever its admins add later, can start a run with every permission the brain has.',
          list: 'Only these members can start a run or stop one; what the others write is not read, not even into the brain’s history.',
        }[modeSel.value];
      };
      modeSel.addEventListener('change', () => { err.hidden = true; sync(); });
      list.addEventListener('input', () => { err.hidden = true; });
      sync();
      return openDialog({
        title: `Who is heard in ${name || 'this group'}`,
        body: h('div.stack',
          field({ label: 'Who is heard', control: modeSel }),
          modeHelp,
          listField,
          err,
          h('p.help', 'Messages from the bridge’s own account always get through.')),
        actions: [
          { label: 'Cancel', value: null },
          {
            label: 'Save', kind: 'primary',
            onClick: async () => {
              let members = null;
              if (modeSel.value === '*') members = ['*'];
              if (modeSel.value === 'list') {
                if (!list.isValid()) {
                  err.hidden = false;
                  err.textContent = 'Some entries are no phone numbers: fix or remove the ones marked.';
                  return false;
                }
                members = list.getValues();
              }
              try {
                await post('/api/chats/members', { jid: g, members });
              } catch (ex) {
                if (errorStatus(ex) !== 400 && errorStatus(ex) !== 422) throw ex;
                err.hidden = false;
                err.textContent = errorMessage(ex);
                return false;
              }
              toast(`Saved who is heard in ${name || 'the group'}`, 'ok');
              await configResource.refresh();
              return true;
            },
          },
        ],
      });
    }

    /**
     * @param {string} title
     * @param {string} message
     * @param {Record<string, string>} body what /api/chats/revoke takes
     * @param {string} done the toast once it is removed
     */
    function removeBtn(title, message, body, done) {
      return btn('', {
        kind: 'ghost', small: true, icon: 'trash', title: 'Remove',
        onClick: async () => {
          if (!await confirmDialog({ title, message, confirm: 'Remove', danger: true })) return;
          try {
            await post('/api/chats/revoke', body);
            toast(done, 'ok');
          } catch (ex) {
            // Gone already, e.g. removed in another tab: the refresh shows it.
            if (errorStatus(ex) !== 404) throw ex;
            toast(errorMessage(ex), 'info');
          }
          await Promise.all([configResource.refresh(), loadGroups(true)]);
        },
      });
    }

    // The bridge checks what is typed; a refusal shows under the box.
    /** @param {{placeholder: string, label: string, help: string, add: (value: string) => Promise<unknown>}} options */
    function addForm({ placeholder, label, help, add }) {
      const input = textInput({ placeholder, class: 'mono', 'aria-label': label });
      const err = h('div.form-error.small', { hidden: true });
      const submit = btn(label, { small: true, icon: 'plus', type: 'submit' });
      const form = h('form.add-form', h('div.inline-add', input, submit), err, h('div.help', help));
      input.addEventListener('input', () => { err.hidden = true; });
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        if (!input.value.trim()) { input.focus(); return; }
        runBusy(submit, async () => {
          try {
            await add(input.value);
          } catch (ex) {
            if (errorStatus(ex) !== 400) throw ex;
            err.hidden = false;
            err.textContent = errorMessage(ex);
            input.focus();
            return;
          }
          input.value = '';
          toast('Added', 'ok');
          await refreshAll();
        });
      });
      return form;
    }
  },
});
