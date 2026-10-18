// Add a brain: choose a folder → check it → configure → done.

import { get, post } from '../app/api.js';
import { folderBrowser } from '../components/folder-browser.js';
import { routingEditor } from '../components/routing-editor.js';
import { h, mount } from '../lib/dom.js';
import { prettyJSON } from '../lib/format.js';
import { routingSummary, slugify, BRAIN_ID_RE, EFFORTS, DEFAULT_BACKUP_EXCLUDES } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast, toastError } from '../components/toast.js';
import { btn, runBusy } from '../components/buttons.js';
import { field, textInput, select, toggle, linesArea, linesOf } from '../components/fields.js';
import { tabs } from '../components/tabs.js';
import { callout, mono, spinner, loading } from '../components/common.js';

const STEPS = [['folder', 'Folder'], ['inspect', 'Check'], ['configure', 'Configure']];

export default {
  title: 'Add brain',
  render(root, ctx) {
    let step = 'folder';
    let picked = null; // {path, create}
    let report = null; // inspect response
    let caps = null;
    let pickAbort = null;
    let tab = 'browse';
    let configure = null; // the configure step's form: {value(), base}

    const stepper = h('ol.stepper');
    const body = h('div.wizard-body');
    mount(root,
      h('header.page-header',
        h('div.page-heading',
          h('a.back-link', { href: '#/brains' }, icon('left'), 'Brains'),
          h('h1', 'Add a brain'),
          h('p.page-sub', 'Turn a folder on this computer into a brain. The bridge adds brain.json and a prompt file if they are missing; nothing else in the folder changes.'))),
      stepper,
      body);

    // Only edits in the configure step need asking about: until then the
    // wizard holds nothing but the folder picked.
    ctx.dirty = () => step === 'configure' && !!configure && JSON.stringify(configure.value()) !== configure.base;
    ctx.onCleanup(() => { if (pickAbort) pickAbort.abort(); });

    function paintStepper() {
      const idx = STEPS.findIndex(([k]) => k === step);
      mount(stepper, STEPS.map(([k, label], i) => h(`li.${i < idx || step === 'done' ? 'done' : i === idx ? 'current' : 'todo'}`,
        { 'aria-current': i === idx ? 'step' : null },
        h('span.step-num', i < idx || step === 'done' ? icon('check') : String(i + 1)),
        h('span.step-label', label))));
    }

    function go(next) {
      step = next;
      configure = null;
      paintStepper();
      window.scrollTo(0, 0);
      if (step === 'folder') renderFolder();
      else if (step === 'inspect') renderInspect();
      else if (step === 'configure') renderConfigure();
    }

    // ── step 1: folder ──
    async function renderFolder() {
      if (!caps) {
        mount(body, h('div.card', loading('Loading…')));
        try { caps = await get('/api/fs/capabilities', { signal: ctx.signal }); } catch (e) { if (ctx.dead) return; caps = { picker: '', home: '' }; toastError(e); }
        if (step !== 'folder') return;
      }
      const pickerName = caps.picker === 'osascript' ? 'Finder' : 'a folder dialog';
      const typed = textInput({ placeholder: '~/assistant/personal', class: 'mono', 'aria-label': 'Folder path' });
      const typedForm = h('form.inline-add', typed, btn('Continue', { kind: 'primary', type: 'submit', icon: 'right' }));
      typedForm.addEventListener('submit', (e) => {
        e.preventDefault();
        const v = typed.value.trim();
        if (!v) { typed.focus(); return; }
        picked = { path: v, create: null };
        go('inspect');
      });
      const chooser = tabs({
        label: 'How to choose the folder',
        items: [['browse', 'Browse folders'], ['type', 'Type a path']],
        selected: tab,
        onSelect: (k, panel, { by }) => {
          tab = k;
          if (k === 'browse') {
            mount(panel, folderBrowser({
              start: '',
              pickLabel: 'Use this folder',
              newLabel: 'Create here',
              newHint: 'A new folder is created when you add the brain, not before.',
              onPick: (path, { create }) => { picked = { path, create }; go('inspect'); },
              signal: ctx.signal,
            }));
            return;
          }
          mount(panel,
            h('p.muted', 'An absolute path, or one starting with ~/ for your home folder. It may not exist yet.'),
            typedForm);
          // Not while the arrow keys move between the tabs: that needs the focus.
          if (by !== 'key') ctx.timeout(() => typed.focus(), 30);
        },
      });
      mount(body, h('div.card.wizard-card',
        h('h2', 'Choose a folder'),
        h('p.muted', 'Use a folder of its own, inside your home folder — for example ', h('code.mono', '~/assistant/personal'), '. Avoid Documents, Desktop and Downloads: macOS protects them and the sandbox blocks them.'),
        caps.picker ? h('div.choose-row',
          btn('Choose folder…', { kind: 'primary', icon: 'folder', onClick: () => nativePick() }),
          h('span.muted.small', `Opens ${pickerName} on the computer running the bridge.`)) : null,
        caps.picker ? h('div.or-sep', h('span', 'or')) : null,
        chooser.list,
        chooser.panel));
    }

    async function nativePick() {
      pickAbort = new AbortController();
      const stopBtn = btn('Stop waiting', { onClick: () => { pickAbort.abort(); } });
      mount(body, h('div.card.wizard-card.waiting',
        spinner('large'),
        h('h2', 'A folder dialog opened on your computer'),
        h('p.muted', 'It may be hidden behind other windows. Choose a folder there — or cancel the dialog to come back here.'),
        h('div.actions', stopBtn)));
      try {
        // The dialog waits for a person, so the request has no deadline.
        const r = await post('/api/fs/pick', undefined, { signal: pickAbort.signal, timeout: 0 });
        if (ctx.dead) return;
        if (r && r.path) {
          picked = { path: r.path, create: false };
          go('inspect');
          return;
        }
        toast('No folder was chosen', 'info');
      } catch (e) {
        if (e.name === 'AbortError') toast('Stopped waiting for the folder dialog.', 'info');
        else toast(e.message);
      } finally {
        pickAbort = null;
      }
      if (!ctx.dead && step === 'folder') renderFolder();
    }

    // ── step 2: inspect ──
    async function renderInspect() {
      mount(body, h('div.card.wizard-card', loading('Checking ', mono(picked.path), '…')));
      try {
        report = await post('/api/brains/inspect', { path: picked.path }, { signal: ctx.signal });
      } catch (e) {
        if (ctx.dead) return;
        mount(body, h('div.card.wizard-card', callout('bad', 'Could not check that folder', e.message), h('div.actions', btn('Back', { icon: 'left', onClick: () => go('folder') }))));
        return;
      }
      if (ctx.dead) return;
      const r = report;
      const problems = r.problems || [];
      const warnings = r.warnings || [];
      const notDir = r.exists && !r.is_dir;
      if (notDir) problems.push('This path is a file, not a folder.');
      const facts = [];
      // The bridge does not look into a folder that cannot be a brain, so
      // there are no facts about it to show.
      if (!problems.length) {
        if (!r.exists) facts.push(['folderPlus', 'New folder — it will be created when you add the brain.']);
        else if (r.is_dir) facts.push(['folder', r.entries ? `Existing folder with ${r.entries} item${r.entries === 1 ? '' : 's'}.` : 'Existing, empty folder.']);
        if (r.is_git) facts.push(['git', 'Git repository — the brain’s files (history, memories) will show up as changes. Consider ignoring them.']);
        if (r.has_prompt) facts.push(['check', 'Has brain-prompt.md — it is kept as it is.']);
        else if (!r.manifest) facts.push(['edit', 'A starter brain-prompt.md will be created for you to edit.']);
        if (r.has_history) facts.push(['check', 'Has conversation history — it is kept and the brain picks up from it.']);
      }

      const actions = h('div.actions.wizard-actions', btn('Choose another folder', { icon: 'left', onClick: () => go('folder') }));
      let manifestBox = null;
      if (r.manifest && !problems.length) {
        const m = r.manifest;
        manifestBox = h('div.callout.info',
          icon('info', 'callout-icon'),
          h('div.callout-body',
            h('strong', 'This folder already has a brain.json'),
            h('div', 'You can add it as it is. Check what it asks for — especially which chats it wants to answer.'),
            h('dl.kv',
              h('dt', 'Name'), h('dd', m.name || '—'),
              h('dt', 'Id'), h('dd', mono(m.id || '—')),
              h('dt', 'Chats'), h('dd', routingSummary(m.routing)),
              m.routing && m.routing.groups && m.routing.groups.length ? [h('dt', 'Groups'), h('dd', m.routing.groups.map((g) => h('div', mono(g))))] : null,
              h('dt', 'Model'), h('dd', m.model || 'inherit'),
              m.disallowed_tools ? [h('dt', 'Blocked tools'), h('dd', m.disallowed_tools.length ? m.disallowed_tools.join(', ') : 'none (all tools allowed)')] : null),
            h('details', h('summary', 'Show brain.json'), h('pre.code', prettyJSON(m)))));
        actions.append(btn('Use the brain.json already in this folder', { kind: 'primary', icon: 'check', onClick: () => submit({ useExisting: true }) }));
      } else if (!problems.length) {
        actions.append(btn('Continue', { kind: 'primary', icon: 'right', onClick: () => go('configure') }));
      }
      mount(body, h('div.card.wizard-card',
        h('h2', 'Check the folder'),
        h('div.path-line', mono(r.abs_path || picked.path, { copy: true })),
        h('ul.facts', facts.map(([i, t]) => h('li', icon(i), h('span', t)))),
        problems.map((p) => callout('bad', null, p)),
        warnings.map((w) => callout('warn', null, w)),
        manifestBox,
        actions));
    }

    // ── step 3: configure ──
    async function renderConfigure() {
      mount(body, h('div.card.wizard-card', loading('Loading…')));
      let brains = [];
      let cfg = null;
      try {
        [brains, cfg] = await Promise.all([get('/api/brains', { signal: ctx.signal }), get('/api/config', { signal: ctx.signal })]);
      } catch (e) {
        if (ctx.dead || e.status === 401) return;
        toastError(e);
      }
      if (step !== 'configure') return;
      const r = report;
      const taken = new Set(brains.map((b) => b.id));
      let idTouched = false;
      const nameIn = textInput({ value: r.suggested ? r.suggested.name : '', placeholder: 'Personal', required: true, spellcheck: 'true' });
      const idIn = textInput({ value: r.suggested ? r.suggested.id : '', class: 'mono', placeholder: 'personal', maxlength: 40, required: true });
      const idErr = h('div.form-error.small', { hidden: true });
      const checkId = () => {
        const v = idIn.value;
        let msg = '';
        if (!BRAIN_ID_RE.test(v)) msg = 'Use 1–40 lowercase letters, digits, - or _, starting with a letter or digit.';
        else if (taken.has(v)) msg = 'Another brain already uses this id.';
        idErr.hidden = !msg;
        idErr.textContent = msg;
        idIn.classList.toggle('invalid', !!msg);
        return !msg;
      };
      nameIn.addEventListener('input', () => {
        if (idTouched) return;
        let id = slugify(nameIn.value);
        const base = id;
        for (let i = 2; taken.has(id); i++) id = `${base}-${i}`;
        idIn.value = id;
        checkId();
      });
      idIn.addEventListener('input', () => {
        idTouched = true;
        const cleaned = idIn.value.toLowerCase().replace(/[^a-z0-9_-]/g, '');
        if (cleaned !== idIn.value) idIn.value = cleaned;
        checkId();
      });
      const eff = (cfg && cfg.effective) || {};
      const allowedGroups = (cfg && cfg.config && cfg.config.allowed_groups) || eff.allowed_groups || [];
      const noDM = !brains.some((b) => b.manifest && b.manifest.routing && b.manifest.routing.handles_dms);
      const routing = routingEditor({
        routing: { handles_dms: noDM, default: brains.length === 0, groups: [] },
        brains,
        allowedGroups,
        signal: ctx.signal,
      });
      const modelIn = textInput({ placeholder: eff.brain_model ? `inherit (${eff.brain_model})` : 'inherit', class: 'mono' });
      const effortSel = select(EFFORTS.map(([v, l]) => [v, v === '' && eff.brain_effort ? `Inherit (${eff.brain_effort})` : l]), '');
      const backupSw = toggle({ checked: true, label: 'Include in Google Drive backups' });
      const excludes = linesArea(DEFAULT_BACKUP_EXCLUDES, { rows: 5 });

      // The manifest the brain is added with.
      const value = () => ({
        version: 1,
        id: idIn.value,
        name: nameIn.value.trim(),
        routing: routing.getValue(),
        model: modelIn.value.trim(),
        effort: effortSel.value,
        backup: { enabled: backupSw.input.checked, exclude: linesOf(excludes) },
      });
      const form = h('form.wizard-card.card.form',
        h('h2', 'Configure the brain'),
        h('div.path-line.muted', icon('folder'), mono(r.abs_path), !r.exists ? ' (will be created)' : ''),
        h('div.form-grid',
          field({ label: 'Name', control: nameIn, help: 'Shown in the dashboard.' }),
          h('div', field({ label: 'Id', control: idIn, help: 'Permanent. Used for history, logs, sandbox rules and backups.' }), idErr)),
        h('fieldset.fieldset', h('legend', 'Which chats does it answer?'), routing),
        h('fieldset.fieldset', h('legend', 'Model'),
          h('div.form-grid',
            field({ label: 'Model', control: modelIn, help: 'e.g. sonnet, opus, or a full model id. Empty uses the global brain model.' }),
            field({ label: 'Effort', control: effortSel, help: 'How hard the model thinks. Inherit uses the global setting.' }))),
        h('fieldset.fieldset', h('legend', 'Backup'),
          h('div.field', backupSw, h('div.help', 'Backs up this folder with the daily backup, once Google Drive is connected.')),
          h('details.details', h('summary', 'Files to leave out of backups'),
            field({ label: 'Exclude patterns', control: excludes, help: 'One per line, relative to the brain folder. A trailing / means a folder.' }))),
        h('div.actions.wizard-actions',
          btn('Back', { icon: 'left', onClick: () => go('inspect') }),
          btn('Add brain', { kind: 'primary', type: 'submit', icon: 'check' })));
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        if (!nameIn.value.trim()) { nameIn.focus(); toast('Give the brain a name'); return; }
        if (!checkId()) { idIn.focus(); return; }
        runBusy(form.querySelector('button[type=submit]'), () => submit({ manifest: value() }));
      });
      mount(body, form);
      checkId();
      configure = { value, base: JSON.stringify(value()) };
      nameIn.focus();
    }

    async function submit({ manifest, useExisting = false }) {
      const r = report;
      const res = await post('/api/brains', {
        path: r.abs_path || picked.path,
        manifest: useExisting ? {} : manifest,
        create_dir: !r.exists,
        use_existing: useExisting,
      });
      step = 'done';
      configure = null;
      paintStepper();
      const m = useExisting ? r.manifest : manifest;
      const quiet = m && m.routing && !m.routing.handles_dms && !m.routing.default && !(m.routing.groups || []).length;
      mount(body, h('div.card.wizard-card.done-card',
        h('div.done-icon', icon('check')),
        h('h2', `${(m && m.name) || res.id} is ready`),
        h('p', 'The brain is registered and will answer from ', mono(res.path), '.'),
        (res.created || []).length ? h('div',
          h('p.muted', 'Created:'),
          h('ul.created-list', res.created.map((c) => h('li', mono(c))))) : h('p.muted', 'No files were created — the folder already had everything.'),
        quiet ? callout('warn', 'No chats yet', 'This brain does not answer any chat yet. Give it direct messages or groups on its page.') : null,
        callout('info', 'Next: tell it who it is', 'Edit ', h('code.mono', (m && m.prompt_file) || 'brain-prompt.md'), ' in the folder. It is the brain’s instructions — who it works for, what it does, how it should answer.'),
        h('div.actions',
          btn('Open the brain', { kind: 'primary', href: `#/brains/${encodeURIComponent(res.id)}`, icon: 'right' }),
          btn('Add another brain', { onClick: () => { picked = null; report = null; go('folder'); } }))));
      toast(`Brain ${res.id} added`, 'ok');
      return true;
    }

    go('folder');
  },
};
