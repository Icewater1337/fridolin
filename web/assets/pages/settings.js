// Settings: a form over config.json. Only the fields you change are written;
// everything else in the file (including fields this page does not show) is
// sent back exactly as received.

import { put } from '../app/api.js';
import { setRestartRequired } from '../app/store.js';
import { configResource } from '../app/resources.js';
import { h, mount, paint } from '../lib/dom.js';
import { plural } from '../lib/format.js';
import { EFFORTS } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { field, toggle, select, textInput, listEditor } from '../components/fields.js';
import { mono, pageHeader, loading } from '../components/common.js';
import { googleClientCard } from '../components/calendar.js';
import { saveBar } from '../components/save-bar.js';
import { liveForm } from '../components/live-form.js';

// The bridge refuses a relative sandbox path: as a rule it would match nothing.
const absPath = (v) => (/^(\/|~(\/|$))/.test(v) ? '' : 'Absolute, or starting with ~/');

const SECTIONS = [
  {
    id: 'access',
    title: 'Access',
    intro: 'Who may talk to the bridge. Everyone else is ignored and shows up on the Chats page.',
    fields: [
      { type: 'allowLists', label: 'Allowed numbers and groups' },
      { key: 'allow_any_dm', type: 'bool', label: 'Let anyone message while the list is empty', help: 'Only matters while the allowed numbers (on the Chats page) are none. Off (the default): nobody can send direct messages until you add a number. On: anyone who knows the account’s number can use the assistant.' },
      { key: 'triggers', type: 'triggers', label: 'Group triggers', help: 'Words that address the bridge in a group; they are removed before the brain sees the message. An @-mention of the bridge’s account always counts, and direct messages never need a trigger. “Start” fits short shorthands like @c.' },
    ],
  },
  {
    id: 'claude',
    title: 'Claude',
    intro: 'How the bridge runs Claude Code.',
    fields: [
      { key: 'claude_path', type: 'text', mono: true, label: 'Claude CLI', help: '“claude” finds it on your PATH. Otherwise the full path to the claude binary.' },
      { key: 'brain_model', type: 'text', mono: true, label: 'Brain model', placeholder: 'CLI default', help: 'sonnet, opus, haiku or a full model id. Brains can override it.' },
      { key: 'brain_effort', type: 'select', label: 'Brain effort', options: EFFORTS.map(([v, l]) => [v, v === '' ? 'CLI default' : l]), help: 'How hard the model thinks. Brains can override it.' },
      { key: 'run_timeout_minutes', type: 'int', def: 30, min: 0, max: 1440, nullable: true, label: 'Stop a run after (minutes)', help: 'A run that takes longer is stopped and the chat is told, so one stuck run cannot hold up its brain’s other messages. 0 lets runs take as long as they take.' },
      { key: 'disallowed_tools', type: 'list', label: 'Blocked tools', placeholder: 'WebFetch', addLabel: 'Add tool', help: 'Tools brains may not use. A brain can override this list in its own settings.' },
      { key: 'dangerously_skip_permissions', type: 'bool', label: 'Let brains use tools without asking', danger: true, help: 'Nobody sits at the keyboard to approve a tool, so without this every tool call waits forever and brains can only chat. With it, a brain can run commands and edit files on its own — the sandbox (below) is the safety net that keeps it out of everything else.' },
    ],
  },
  {
    id: 'messages',
    title: 'Messages',
    intro: 'Queueing, batching and splitting.',
    fields: [
      { key: 'queue.mode', type: 'select', def: 'queue', label: 'When a brain is busy', options: [['queue', 'Queue — answer one after another'], ['interrupt', 'Interrupt — a newer request replaces the sender’s own']], help: 'Interrupt stops a run only for a newer request from the same person in the same chat. Other chats and other people still wait their turn; untagged group chatter and ingested files never interrupt.' },
      { key: 'queue.max_pending', type: 'int', def: 10, min: 1, label: 'Waiting messages per brain', help: 'Messages beyond this are turned away.' },
      { key: 'queue.notify', type: 'bool', def: true, label: 'Tell people their message is queued', help: 'Sends “⏳ queued” when a tagged message has to wait.' },
      { key: 'debounce.quiet_ms', type: 'int', def: 2000, min: 0, nullable: true, label: 'Gather messages for (ms)', help: 'Messages from the same person within this pause are answered together — several photos plus a question. 0 turns it off.' },
      { key: 'debounce.max_wait_ms', type: 'int', def: 10000, min: 0, nullable: true, label: 'Never gather longer than (ms)' },
      { key: 'max_chunk_size', type: 'int', def: 3900, min: 100, label: 'Longest single message (characters)', help: 'Counted in characters, the [1/3] numbering included; longer replies are split and numbered. At least 100; the default stays under the 4096 commonly given as WhatsApp’s limit.' },
      { key: 'max_media_mb', type: 'int', def: 100, min: 1, max: 2048, label: 'Largest attachment (MB)', help: 'Bigger files are not downloaded, and the sender is told. A download also has two minutes to finish. At most 2048.' },
      { key: 'otp_guard', type: 'bool', label: 'Hold back one-time codes', help: 'After the assistant asked for a code, a message that is only a 4–8 digit number is dropped before the model sees it, and kept in the data folder’s otp/ for the local script that asked for it.' },
      { key: 'otp_markers', type: 'list', label: 'Phrases that ask for a code', placeholder: 'verification code', addLabel: 'Add phrase', help: 'Empty uses the built-in list: 🔐, OTP, 2FA, one-time code, verification code.' },
      { key: 'ingest.enabled', type: 'bool', label: 'Accept files through /ingest', help: 'Lets local programs (a web upload, a recorder sync) hand files to a brain with POST /ingest. Every call needs a token: your programs send the one in scripts.token in the data folder (Authorization: Bearer …); a brain sends its own, and may hand files only to itself.' },
    ],
  },
  {
    id: 'sandbox',
    title: 'Sandbox',
    intro: 'macOS only. Every brain run is wrapped in sandbox-exec: brains cannot read or change other brains’ folders, your keys, mail, messages, browser profiles, Documents, Desktop or Downloads.',
    fields: [
      { key: 'sandbox.enabled', type: 'bool', label: 'Sandbox brains', help: 'Strongly recommended while brains may use tools without asking.' },
      { key: 'sandbox.deny_paths', type: 'list', mono: true, label: 'Also block', placeholder: '~/clients', validate: absPath, addLabel: 'Add folder', help: 'Extra folders brains must not read or change. Absolute, or starting with ~/.' },
      { key: 'sandbox.allow_paths', type: 'list', mono: true, label: 'Re-allow', placeholder: '~/Documents/assistant-share', validate: absPath, addLabel: 'Add folder', help: 'Folders brains may read again despite the built-in blocks (reading only), except other brains’ folders, the bridge’s own data and your CLAUDE.md files above a brain’s folder. Rarely needed. Absolute, or starting with ~/.' },
      { key: 'sandbox.git_config', type: 'select', def: 'read', label: 'Git settings (~/.gitconfig)', options: [['read', 'Readable — brains can commit in their folder'], ['deny', 'Blocked']] },
      { key: 'sandbox.credentials_dir', type: 'text', mono: true, label: 'Credentials folder', placeholder: 'not set', help: 'A folder for the brains’ calendar tokens, which the bridge uses for them. No brain may read it. Absolute, or starting with ~/.' },
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
      { key: 'data_dir', type: 'text', mono: true, restart: true, label: 'Data folder', help: 'Holds the WhatsApp pairing, the database, media and the dashboard token. Changing it does not move those files — the bridge starts fresh (and unpaired) in the new folder. At startup the folder is made private to your user (mode 0700), unless it is your home folder, one above it or another user’s folder.' },
      { key: 'log_level', type: 'select', restart: true, label: 'Log level', options: [['', 'Default (info)'], ['debug', 'Debug — very verbose'], ['info', 'Info'], ['warn', 'Warnings'], ['error', 'Errors only']] },
      { key: 'viewer', type: 'bool', label: 'Open a terminal viewer per brain', help: 'macOS, needs tmux. Opens a Terminal window per brain that follows its activity. Handy while tuning a prompt. Its tmux runs outside the sandbox, and brains can send it commands.' },
      { key: 'script_api.tokenless_send', type: 'bool', def: true, label: 'Let /send work without a token', help: 'For scripts written before /send had tokens: without one, it reaches only your allowed chats. While this is on, any program on this computer — a brain too — can message those chats. Scripts send the token in scripts.token in the data folder; brains get their own.' },
      { key: 'dashboard.require_token', type: 'bool', def: true, label: 'Require the dashboard token', confirmOff: true, help: 'Brains run on this computer and can reach this dashboard. The token is what stops a manipulated brain from changing these settings — or turning the sandbox off. Turning it off takes effect after a restart; turning it on, at once.' },
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
    const body = h('div', h('div.card', loading('Loading settings…')));
    const bar = saveBar({ save: btn('Save', { kind: 'primary', icon: 'check', onClick: () => save() }), onDiscard: () => form.discard(), floating: true });
    const where = h('span'); // the file, once the settings have loaded
    const googleCard = googleClientCard({ signal: ctx.signal });
    mount(root, pageHeader('Settings', where), body, bar);

    // Only the settings edited here count as a conflict: one changed
    // elsewhere that is not edited here is saved as it now is.
    const edited = (built) => built.controls.filter((c) => c.isDirty());
    const form = liveForm({
      build,
      edits: (d, built) => edited(built).map((c) => [c.key, getPath(d.config || {}, c.key)]),
      onChange: () => {
        const built = form.form();
        const n = built ? edited(built).length : 0;
        if (n) bar.show(plural(n, 'unsaved change'));
        else bar.hide();
      },
      staleNote: {
        title: 'The settings changed somewhere else',
        text: 'A setting you changed here was also changed in config.json (another tab, the command line or a brain action). Reload to see the current settings — your unsaved edits will be lost.',
        action: 'Reload settings',
      },
    });
    ctx.dirty = () => form.dirty();

    ctx.follow(configResource, (d) => {
      const first = !form.form();
      form.set(d);
      paint(where, d.path, (path) => ['Saved to ', mono(path, { copy: true }), '. Brains are configured on the Brains page, backups on the Backup page.']);
      if (!first) return;
      mount(body, form.el);
      const target = ctx.params.section && document.getElementById(`sec-${ctx.params.section}`);
      if (!target) return;
      target.scrollIntoView({ block: 'start' });
      ctx.focusAt(target.querySelector('h2'));
    }, 5000);

    function build(d, changed) {
      const cfg = d.config || {};
      const eff = d.effective || {};
      const controls = [];
      const nav = h('nav.section-nav', { 'aria-label': 'Settings sections' });
      // A chip scrolls to its section and names it in the address, so
      // #/settings/<section> links (the overview has some) land there too.
      const chip = (title, sectionId, el) => {
        const b = h('button.chip', { type: 'button' }, title);
        b.addEventListener('click', () => {
          el.scrollIntoView({ behavior: 'smooth', block: 'start' });
          ctx.replaceHash(`#/settings/${sectionId}`);
        });
        nav.appendChild(b);
      };
      const sections = SECTIONS.map((sec) => {
        const el = h('section.card.settings-section', { id: `sec-${sec.id}` },
          h('div.section-heading', h('h2', sec.title), sec.intro ? h('p.muted.section-intro', sec.intro) : null),
          h('div.settings-fields', sec.fields.map((f) => {
            const c = makeControl(f, cfg, eff, changed);
            controls.push(c);
            return c.el;
          })));
        chip(sec.title, sec.id, el);
        return el;
      });
      chip('Google sign-in', 'google', googleCard);
      return {
        el: h('div.page-stack', nav, sections, googleCard),
        value: () => controls.map((c) => c.value()),
        controls,
      };
    }

    // The edits go onto the newest settings the page has, so what changed
    // elsewhere meanwhile is kept. When config.json changed after that, the
    // page fetches it and saves once more, unless the change was to a
    // setting edited here. Such a clash is never saved over: the note
    // about it may be scrolled out of sight. A second refusal without a
    // clash (the file changed again, or could not be fetched) is no clash
    // either, and saving again is worth a try.
    const clash = () => {
      window.scrollTo({ top: 0, behavior: 'smooth' });
      toast('A setting you changed was also changed somewhere else. Nothing was saved; see the note at the top.');
    };
    async function save(again = true) {
      const built = form.form();
      if (form.stale()) { clash(); return; }
      const invalid = built.controls.find((c) => c.isDirty() && !c.isValid());
      if (invalid) { toast(`Check “${invalid.label}” — it has an invalid entry`); invalid.el.scrollIntoView({ block: 'center' }); return; }
      const latest = form.data();
      const next = JSON.parse(JSON.stringify(latest.config || {}));
      for (const c of edited(built)) setPath(next, c.key, c.serialize());
      let r;
      try {
        r = await put('/api/config', { etag: latest.etag, config: next });
      } catch (e) {
        if (e.status !== 409) throw e;
        await configResource.refresh();
        if (form.stale()) clash();
        else if (again) await save(false);
        else toast('config.json changed again while saving, so nothing was saved. Save once more.', 'warn');
        return;
      }
      form.saved();
      toast('Settings saved', 'ok');
      if (r) setRestartRequired(r.restart_required);
      await configResource.refresh();
    }

    ctx.onCleanup(() => bar.remove());
  },
};

// ── controls ─────────────────────────────────────────────────────────────

function makeControl(f, cfg, eff, onChange) {
  if (f.type === 'allowLists') return allowLists(f, cfg);
  const fileVal = getPath(cfg, f.key);
  const effVal = getPath(eff, f.key);
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
      const sw = toggle({ checked: v, label: f.label });
      input = sw;
      read = () => sw.input.checked;
      serialize = read;
      sw.input.addEventListener('change', async () => {
        if (f.confirmOff && !sw.input.checked) {
          const ok = await confirmDialog({
            title: 'Turn off the dashboard token?',
            message: h('div',
              h('p', 'Without the token, any program on this computer can change the bridge’s settings — including a brain that was tricked by a message into doing so. It could allow new chats, widen its own routing or switch the sandbox off.'),
              h('p', 'It takes effect after the bridge restarts. Until then the token is still required.'),
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
        tag ? h('span.field-tag', tag) : null);
      return finish(el);
    }
    case 'text': {
      initial = typeof fileVal === 'string' ? fileVal : '';
      const ph = typeof effVal === 'string' && effVal && effVal !== initial ? effVal : (f.placeholder || '');
      input = textInput({ value: initial, placeholder: ph, class: f.mono ? 'mono' : '', spellcheck: f.mono ? 'false' : 'true' });
      input.addEventListener('input', onChange);
      read = () => input.value.trim();
      serialize = read;
      break;
    }
    case 'int': {
      initial = (fileVal === undefined || fileVal === null || (!f.nullable && fileVal === 0)) ? '' : String(fileVal);
      const ph = effVal !== undefined && effVal !== null && effVal !== 0 ? String(effVal) : String(f.def ?? '');
      input = textInput({ value: initial, inputmode: 'numeric', placeholder: `${ph} (default)`, class: 'num' });
      input.addEventListener('input', () => { check(); onChange(); });
      const check = () => {
        const ok = input.value.trim() === '' || (/^\d+$/.test(input.value.trim()) && Number(input.value) >= (f.min ?? 0) && Number(input.value) <= (f.max ?? Infinity));
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
      input = select(opts, v);
      input.addEventListener('change', onChange);
      read = () => input.value;
      serialize = read;
      break;
    }
    case 'list': {
      const arr = Array.isArray(fileVal) ? fileVal : [];
      initial = JSON.stringify(arr);
      input = listEditor({ values: arr, placeholder: f.placeholder, mono: f.mono !== false, addLabel: f.addLabel || 'Add', onChange, validate: f.validate, empty: emptyListText(f, effVal) });
      read = () => JSON.stringify(input.getValues());
      serialize = () => input.getValues();
      isValid = () => input.isValid();
      break;
    }
    case 'triggers': {
      const raw = Array.isArray(fileVal) ? fileVal : [];
      const list = raw.map((t) => (typeof t === 'string' ? { text: t, match: 'anywhere' } : { text: t.text || '', match: t.match || 'anywhere' }));
      initial = JSON.stringify(list);
      input = triggersEditor(list, { onChange, defaults: Array.isArray(effVal) ? effVal : [] });
      read = () => JSON.stringify(input.getValues());
      serialize = () => input.getValues().map((t) => (t.match === 'anywhere' ? t.text : { text: t.text, match: t.match }));
      break;
    }
    default:
      throw new Error(`unknown field type ${f.type}`);
  }

  const el = field({ label: f.label, control: input, help: f.help, tag });
  return finish(el);

  function finish(el) {
    return {
      key: f.key,
      label: f.label,
      el,
      value: read,
      isDirty: () => read() !== initial,
      isValid,
      serialize,
    };
  }
}

// The allow lists are edited on the Chats page only, through the bridge's
// checks; Settings says how many there are. It never has unsaved changes.
function allowLists(f, cfg) {
  const n = (cfg.allowed_numbers || []).length;
  const g = (cfg.allowed_groups || []).length;
  const el = field({
    label: f.label,
    control: h('div.static', h('span', `${plural(n, 'number')} and ${plural(g, 'group')} may talk to the bridge. `, h('a', { href: '#/chats' }, 'Edit them on the Chats page'), '.')),
  });
  return { key: null, label: f.label, el, value: () => null, isDirty: () => false, isValid: () => true, serialize: () => undefined };
}

function emptyListText(f, effVal) {
  if (f.key === 'otp_markers') return 'Using the built-in phrases';
  if (f.key === 'disallowed_tools') return 'No tools blocked';
  if (Array.isArray(effVal) && effVal.length) return `Default: ${effVal.join(', ')}`;
  return 'None';
}

function triggersEditor(values, { onChange, defaults }) {
  const rows = h('div.list-rows');
  const emptyEl = h('div.list-empty.muted', `Empty — the default is used: ${(defaults || []).map((t) => (typeof t === 'string' ? t : t.text)).join(', ') || '@claude'}`);
  const el = h('div.list-editor.triggers-editor', emptyEl, rows);
  const paintEmpty = () => { emptyEl.hidden = rows.children.length > 0; };
  const add = (t = { text: '', match: 'anywhere' }, focus = false) => {
    const text = textInput({ value: t.text, placeholder: '@claude', class: 'mono', 'aria-label': 'Trigger word' });
    const match = select([['anywhere', 'Anywhere'], ['start', 'At the start'], ['word', 'Whole word']], t.match, { 'aria-label': 'Where it must appear' });
    const del = h('button.icon-btn', { type: 'button', title: 'Remove', 'aria-label': 'Remove' }, icon('x'));
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
  el.appendChild(h('div.list-add', btn('Add trigger', { icon: 'plus', small: true, kind: 'ghost', onClick: () => { add(undefined, true); onChange(); } })));
  el.getValues = () => [...rows.querySelectorAll('.trigger-row')].map((r) => ({ text: r.querySelector('input').value.trim(), match: r.querySelector('select').value })).filter((t) => t.text);
  return el;
}
