// Settings: a form over config.json. Only the fields you change are written;
// everything else in the file (including fields this page does not show) is
// sent back exactly as received.

import { get, put } from './api.js';
import { setRestartRequired } from './state.js';
import {
  h, mount, icon, btn, callout, field, toggle, select, textInput, listEditor, mono, pageHeader,
  spinner, confirmDialog, toast, runBusy, EFFORTS, nextId, plural, loading,
} from './ui.js';

const digits = (v) => (/^\d{6,15}$/.test(v) ? '' : 'Digits only, with country code, no + (e.g. 15551234567)');
const groupJid = (v) => (/@g\.us$/.test(v) ? '' : 'A group JID ends in @g.us');

const SECTIONS = [
  {
    id: 'access',
    title: 'Access',
    intro: 'Who may talk to the bridge. Everyone else is ignored and shows up on the Chats page.',
    fields: [
      { key: 'allowed_numbers', type: 'list', label: 'Allowed numbers', placeholder: '15551234567', validate: digits, addLabel: 'Add number', help: 'Phone numbers that may send direct messages. Digits with the country code, no + or spaces.' },
      { key: 'allow_any_dm', type: 'bool', def: true, label: 'Let anyone message while the list is empty', help: 'Only matters while “Allowed numbers” is empty. Off: nobody can send direct messages until you add a number. On: anyone who knows the account’s number can use the assistant.' },
      { key: 'allowed_groups', type: 'list', label: 'Allowed groups', placeholder: '120363000000000000@g.us', validate: groupJid, addLabel: 'Add group', help: 'Group JIDs the bridge answers in. Every other group is ignored. Easier: allow groups on the Chats page.' },
      { key: 'triggers', type: 'triggers', label: 'Group triggers', help: 'Words that address the bridge in a group; they are removed before the brain sees the message. An @-mention of the bridge’s account always counts, and direct messages never need a trigger. “Start” fits short shorthands like @c.' },
    ],
  },
  {
    id: 'claude',
    title: 'Claude',
    intro: 'How the bridge runs Claude Code.',
    fields: [
      { key: 'claude_path', type: 'text', mono: true, label: 'Claude CLI', help: '“claude” finds it on your PATH. Otherwise the full path to the claude binary.' },
      { key: 'brain_mode', type: 'bool', label: 'Brain mode', help: 'On: chats go to brains — folders with their own prompt, memory and history. Off: classic mode, one Claude session per chat in one shared folder.' },
      { key: 'brain_model', type: 'text', mono: true, label: 'Brain model', placeholder: 'CLI default', help: 'sonnet, opus, haiku or a full model id. Brains can override it.' },
      { key: 'brain_effort', type: 'select', label: 'Brain effort', options: EFFORTS.map(([v, l]) => [v, v === '' ? 'CLI default' : l]), help: 'How hard the model thinks. Brains can override it.' },
      { key: 'model', type: 'text', mono: true, label: 'Model in classic mode', placeholder: 'CLI default' },
      { key: 'disallowed_tools', type: 'list', label: 'Blocked tools', placeholder: 'WebFetch', addLabel: 'Add tool', help: 'Tools brains may not use. A brain can override this list in its own settings.' },
      { key: 'dangerously_skip_permissions', type: 'bool', label: 'Let brains use tools without asking', danger: true, help: 'Nobody sits at the keyboard to approve a tool, so without this every tool call waits forever and brains can only chat. With it, a brain can run commands and edit files on its own — the sandbox (below) is the safety net that keeps it out of everything else.' },
    ],
  },
  {
    id: 'messages',
    title: 'Messages',
    intro: 'Queueing, batching and splitting.',
    fields: [
      { key: 'queue.mode', type: 'select', def: 'queue', label: 'When a brain is busy', options: [['queue', 'Queue — answer one after another'], ['interrupt', 'Interrupt — stop and answer the newest']] },
      { key: 'queue.max_pending', type: 'int', def: 10, min: 1, label: 'Waiting messages per brain', help: 'Messages beyond this are turned away.' },
      { key: 'queue.notify', type: 'bool', def: true, label: 'Tell people their message is queued', help: 'Sends “⏳ queued” when a tagged message has to wait.' },
      { key: 'debounce.quiet_ms', type: 'int', def: 2000, min: 0, nullable: true, label: 'Gather messages for (ms)', help: 'Messages from the same person within this pause are answered together — several photos plus a question. 0 turns it off.' },
      { key: 'debounce.max_wait_ms', type: 'int', def: 10000, min: 0, nullable: true, label: 'Never gather longer than (ms)' },
      { key: 'max_chunk_size', type: 'int', def: 3900, min: 100, label: 'Longest single message (characters)', help: 'Longer replies are split and numbered [1/3]. WhatsApp’s limit is about 4096.' },
      { key: 'otp_guard', type: 'bool', label: 'Hold back one-time codes', help: 'After the assistant asked for a code, a message that is only a 4–8 digit number is dropped before the model sees it.' },
      { key: 'otp_markers', type: 'list', label: 'Phrases that ask for a code', placeholder: 'verification code', addLabel: 'Add phrase', help: 'Empty uses the built-in list: 🔐, OTP, 2FA, one-time code, verification code.' },
      { key: 'ingest.enabled', type: 'bool', label: 'Accept files through /ingest', help: 'Lets local programs (a web upload, a recorder sync) hand files to a brain with POST /ingest.' },
    ],
  },
  {
    id: 'sandbox',
    title: 'Sandbox',
    intro: 'macOS only. Every brain run is wrapped in sandbox-exec: brains cannot read other brains’ folders, your keys, mail, messages, browser profiles, Documents, Desktop or Downloads.',
    fields: [
      { key: 'sandbox.enabled', type: 'bool', label: 'Sandbox brains', help: 'Strongly recommended while brains may use tools without asking.' },
      { key: 'sandbox.deny_paths', type: 'list', mono: true, label: 'Also block', placeholder: '~/clients', addLabel: 'Add folder', help: 'Extra folders brains must not read.' },
      { key: 'sandbox.allow_paths', type: 'list', mono: true, label: 'Re-allow', placeholder: '~/Documents/assistant-share', addLabel: 'Add folder', help: 'Folders to open again after the built-in blocks. Rarely needed.' },
      { key: 'sandbox.git_config', type: 'select', def: 'read', label: 'Git settings (~/.gitconfig)', options: [['read', 'Readable — brains can commit in their folder'], ['deny', 'Blocked']] },
      { key: 'sandbox.credentials_dir', type: 'text', mono: true, label: 'Credentials folder', placeholder: 'not set', help: 'A folder of per-brain credential files. Each brain may read only its own calendar credentials there.' },
    ],
  },
  {
    id: 'startup',
    title: 'Startup checks',
    intro: 'Files the assistant cannot work without — credentials, typically. Missing ones are logged loudly and shown on the overview.',
    fields: [
      { key: 'startup_checks.required_files', type: 'list', mono: true, label: 'Files that must exist', placeholder: '~/.config/assistant/credentials.json', addLabel: 'Add file' },
      { key: 'startup_checks.notify_whatsapp', type: 'bool', label: 'Also tell me on WhatsApp' },
      { key: 'startup_checks.notify_jid', type: 'text', mono: true, label: 'Send the alert to', placeholder: 'your first allowed number', help: 'A chat JID, e.g. 15551234567@s.whatsapp.net.' },
      { key: 'startup_checks.hint', type: 'text', label: 'Add this hint to the alert', placeholder: 'e.g. how to restore the files' },
    ],
  },
  {
    id: 'advanced',
    title: 'Advanced',
    intro: '',
    fields: [
      { key: 'http_listen', type: 'text', mono: true, restart: true, label: 'Listen address', placeholder: '127.0.0.1:8765', help: 'Loopback addresses only. Changing it moves this dashboard to the new address after a restart.' },
      { key: 'data_dir', type: 'text', mono: true, restart: true, label: 'Data folder', help: 'Holds the WhatsApp pairing, the database, media and the dashboard token. Changing it does not move those files — the bridge starts fresh (and unpaired) in the new folder.' },
      { key: 'log_level', type: 'select', restart: true, label: 'Log level', options: [['', 'Default (info)'], ['debug', 'Debug — very verbose'], ['info', 'Info'], ['warn', 'Warnings'], ['error', 'Errors only']] },
      { key: 'viewer', type: 'bool', label: 'Open a terminal viewer per brain', help: 'macOS, needs tmux. Opens a Terminal window per brain that follows its activity. Handy while tuning a prompt.' },
      { key: 'dashboard.require_token', type: 'bool', def: true, label: 'Require the dashboard token', confirmOff: true, help: 'Brains run on this computer and can reach this dashboard. The token is what stops a manipulated brain from changing these settings — or turning the sandbox off.' },
    ],
  },
];

function getPath(obj, key) {
  return key.split('.').reduce((o, k) => (o && typeof o === 'object' ? o[k] : undefined), obj);
}

function setPath(obj, key, value) {
  const parts = key.split('.');
  let o = obj;
  for (const p of parts.slice(0, -1)) {
    if (!o[p] || typeof o[p] !== 'object') o[p] = {};
    o = o[p];
  }
  if (value === undefined) delete o[parts[parts.length - 1]];
  else o[parts[parts.length - 1]] = value;
}

export default {
  title: 'Settings',
  render(root, ctx) {
    let data = null; // GET /api/config response the form was built from
    let conflict = false; // a stale/conflict banner is showing
    let controls = [];
    const top = h('div');
    const body = h('div.page-stack', h('div.card', loading('Loading settings…')));
    const countEl = h('span');
    const saveBtn = btn('Save', { kind: 'primary', icon: 'check', onClick: () => save() });
    const saveBar = h('div.save-bar.floating', { hidden: true },
      icon('edit'), countEl,
      btn('Discard', { onClick: () => build(data) }),
      saveBtn);

    mount(root, pageHeader('Settings', null), top, body, saveBar);
    const sub = root.querySelector('.page-heading');

    const dirtyCount = () => controls.filter((c) => c.isDirty()).length;
    ctx.dirty = () => dirtyCount() > 0;
    const onChange = () => {
      const n = dirtyCount();
      saveBar.hidden = n === 0;
      countEl.textContent = `${plural(n, 'unsaved change')}`;
    };

    function build(d) {
      data = d;
      const cfg = d.config || {};
      const eff = d.effective || {};
      const locked = d.env_locked || {};
      controls = [];
      const nav = h('nav.section-nav', { 'aria-label': 'Settings sections' });
      const sections = SECTIONS.map((sec) => {
        const id = nextId(`sec-${sec.id}`);
        const b = h('button.chip', { type: 'button' }, sec.title);
        nav.appendChild(b);
        const el = h('section.card.settings-section', { id },
          h('h2', sec.title),
          sec.intro ? h('p.muted.section-intro', sec.intro) : null,
          h('div.settings-fields', sec.fields.map((f) => {
            const c = makeControl(f, cfg, eff, locked[f.key], onChange);
            controls.push(c);
            return c.el;
          })));
        b.addEventListener('click', () => el.scrollIntoView({ behavior: 'smooth', block: 'start' }));
        return el;
      });
      mount(body, nav, sections);
      mount(sub.querySelector('.page-sub') || sub.appendChild(h('p.page-sub')), 'Saved to ', mono(d.path, { copy: true }), '. Brains are configured on the Brains page, backups on the Backup page.');
      onChange();
      conflict = false;
      mount(top);
    }

    async function save() {
      const invalid = controls.find((c) => c.isDirty() && !c.isValid());
      if (invalid) { toast(`Check “${invalid.label}” — it has an invalid entry`); invalid.el.scrollIntoView({ block: 'center' }); return; }
      const next = JSON.parse(JSON.stringify(data.config || {}));
      for (const c of controls) if (c.isDirty()) setPath(next, c.key, c.serialize());
      try {
        const r = await put('/api/config', { etag: data.etag, config: next });
        const fresh = await get('/api/config');
        build(fresh);
        toast('Settings saved', 'ok');
        if (r && r.restart_required && r.restart_required.length) setRestartRequired(r.restart_required);
      } catch (e) {
        if (e.status === 409) {
          conflict = true;
          mount(top, callout('bad', 'The settings changed somewhere else',
            'config.json was changed while you were editing (another tab, the command line or a brain action). Reload to see the current settings — your unsaved edits will be lost.',
            h('div.actions', btn('Reload settings', { kind: 'primary', icon: 'refresh', onClick: async () => build(await get('/api/config')) }))));
          window.scrollTo({ top: 0, behavior: 'smooth' });
          return;
        }
        throw e;
      }
    }

    ctx.poll(async () => {
      const d = await get('/api/config');
      if (!data) { build(d); return; }
      if (d.etag === data.etag) return;
      if (!ctx.dirty()) { build(d); return; }
      if (conflict) return;
      conflict = true;
      mount(top, callout('warn', 'The settings changed somewhere else',
        'Saving now would fail. Reload to see the current settings — your unsaved edits will be lost.',
        h('div.actions', btn('Reload settings', { icon: 'refresh', onClick: async () => build(await get('/api/config')) }))));
    }, 5000);

    ctx.onCleanup(() => saveBar.remove());
  },
};

// ── controls ─────────────────────────────────────────────────────────────

function makeControl(f, cfg, eff, lockedBy, onChange) {
  const fileVal = getPath(cfg, f.key);
  const effVal = getPath(eff, f.key);
  const disabled = !!lockedBy;
  const tag = f.restart ? 'restart' : null;
  let input;
  let read;
  let initial;
  let serialize;
  let isValid = () => true;
  let extra = null;

  switch (f.type) {
    case 'bool': {
      const v = fileVal !== undefined && fileVal !== null ? !!fileVal : (effVal !== undefined && effVal !== null ? !!effVal : !!f.def);
      initial = v;
      const sw = toggle({ checked: lockedBy ? !!effVal : v, label: f.label, disabled });
      input = sw;
      read = () => sw.input.checked;
      serialize = read;
      sw.input.addEventListener('change', async () => {
        if (f.confirmOff && !sw.input.checked) {
          const ok = await confirmDialog({
            title: 'Turn off the dashboard token?',
            message: h('div',
              h('p', 'Without the token, any program on this computer can change the bridge’s settings — including a brain that was tricked by a message into doing so. It could allow new chats, widen its own routing or switch the sandbox off.'),
              h('p', h('strong', 'Only do this on a machine where nothing untrusted runs.'))),
            confirm: 'Turn it off',
            danger: true,
          });
          if (!ok) sw.input.checked = true;
        }
        onChange();
      });
      if (f.danger) extra = h('div.callout.warn.inline-callout', icon('alert', 'callout-icon'), h('div.callout-body', f.help));
      const el = h('div.field.bool-field', sw,
        f.danger ? extra : (f.help ? h('div.help', f.help) : null),
        tag ? h('span.field-tag', tag) : null,
        lockedBy ? h('div.help.locked', icon('lock'), `Set by the environment variable ${lockedBy} — change it there.`) : null);
      return finish(el);
    }
    case 'text': {
      initial = typeof fileVal === 'string' ? fileVal : '';
      const ph = typeof effVal === 'string' && effVal && effVal !== initial ? effVal : (f.placeholder || '');
      input = textInput({ value: lockedBy ? (effVal || '') : initial, placeholder: ph, class: f.mono ? 'mono' : '', disabled, spellcheck: f.mono ? 'false' : 'true' });
      input.addEventListener('input', onChange);
      read = () => input.value.trim();
      serialize = read;
      break;
    }
    case 'int': {
      initial = (fileVal === undefined || fileVal === null || (!f.nullable && fileVal === 0)) ? '' : String(fileVal);
      const ph = effVal !== undefined && effVal !== null && effVal !== 0 ? String(effVal) : String(f.def ?? '');
      input = textInput({ value: lockedBy ? String(effVal ?? '') : initial, inputmode: 'numeric', placeholder: `${ph} (default)`, disabled, class: 'num' });
      input.addEventListener('input', () => { check(); onChange(); });
      const check = () => {
        const ok = input.value.trim() === '' || (/^\d+$/.test(input.value.trim()) && Number(input.value) >= (f.min ?? 0));
        input.classList.toggle('invalid', !ok);
        return ok;
      };
      isValid = check;
      read = () => input.value.trim();
      serialize = () => (read() === '' ? undefined : Number(read()));
      break;
    }
    case 'select': {
      const opts = f.options;
      const has = (v) => opts.some(([x]) => x === v);
      let v = typeof fileVal === 'string' ? fileVal : '';
      if (!has(v)) v = has(effVal) ? effVal : (f.def ?? '');
      initial = v;
      input = select(opts, lockedBy ? (effVal || '') : v, { disabled });
      input.addEventListener('change', onChange);
      read = () => input.value;
      serialize = read;
      break;
    }
    case 'list': {
      const arr = Array.isArray(lockedBy ? effVal : fileVal) ? (lockedBy ? effVal : fileVal) : [];
      initial = JSON.stringify(arr);
      input = listEditor({ values: arr, placeholder: f.placeholder, mono: f.mono !== false, disabled, addLabel: f.addLabel || 'Add', onChange, validate: f.validate, empty: emptyListText(f, effVal) });
      read = () => JSON.stringify(input.getValues());
      serialize = () => input.getValues();
      isValid = () => input.isValid();
      break;
    }
    case 'triggers': {
      const raw = Array.isArray(lockedBy ? effVal : fileVal) ? (lockedBy ? effVal : fileVal) : [];
      const list = raw.map((t) => (typeof t === 'string' ? { text: t, match: 'anywhere' } : { text: t.text || '', match: t.match || 'anywhere' }));
      initial = JSON.stringify(list);
      input = triggersEditor(list, { disabled, onChange, defaults: Array.isArray(effVal) ? effVal : [] });
      read = () => JSON.stringify(input.getValues());
      serialize = () => input.getValues().map((t) => (t.match === 'anywhere' ? t.text : { text: t.text, match: t.match }));
      break;
    }
    default:
      throw new Error(`unknown field type ${f.type}`);
  }

  const el = field({ label: f.label, control: input, help: f.help, locked: lockedBy, tag });
  return finish(el);

  function finish(el) {
    return {
      key: f.key,
      label: f.label,
      el,
      isDirty: () => !lockedBy && read() !== initial,
      isValid,
      serialize,
    };
  }
}

function emptyListText(f, effVal) {
  if (f.key === 'otp_markers') return 'Using the built-in phrases';
  if (f.key === 'allowed_numbers') return 'Empty — see the switch below';
  if (f.key === 'allowed_groups') return 'No groups allowed';
  if (f.key === 'disallowed_tools') return 'No tools blocked';
  if (Array.isArray(effVal) && effVal.length) return `Default: ${effVal.join(', ')}`;
  return 'None';
}

function triggersEditor(values, { disabled, onChange, defaults }) {
  const rows = h('div.list-rows');
  const emptyEl = h('div.list-empty.muted', `Empty — the default is used: ${(defaults || []).map((t) => (typeof t === 'string' ? t : t.text)).join(', ') || '@claude'}`);
  const el = h('div.list-editor.triggers-editor', emptyEl, rows);
  const paintEmpty = () => { emptyEl.hidden = rows.children.length > 0; };
  const add = (t = { text: '', match: 'anywhere' }, focus = false) => {
    const text = textInput({ value: t.text, placeholder: '@claude', class: 'mono', disabled, 'aria-label': 'Trigger word' });
    const match = select([['anywhere', 'Anywhere'], ['start', 'At the start'], ['word', 'Whole word']], t.match, { disabled, 'aria-label': 'Where it must appear' });
    const del = h('button.icon-btn', { type: 'button', title: 'Remove', 'aria-label': 'Remove', disabled }, icon('x'));
    const row = h('div.list-row.trigger-row', text, match, del);
    text.addEventListener('input', onChange);
    match.addEventListener('change', onChange);
    del.addEventListener('click', () => { row.remove(); paintEmpty(); onChange(); });
    rows.appendChild(row);
    paintEmpty();
    if (focus) text.focus();
  };
  values.forEach((v) => add(v));
  paintEmpty();
  if (!disabled) el.appendChild(h('div.list-add', btn('Add trigger', { icon: 'plus', small: true, kind: 'ghost', onClick: () => { add(undefined, true); onChange(); } })));
  el.getValues = () => [...rows.querySelectorAll('.trigger-row')].map((r) => ({ text: r.querySelector('input').value.trim(), match: r.querySelector('select').value })).filter((t) => t.text);
  return el;
}
