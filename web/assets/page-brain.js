// One brain: live run and queue, settings (brain.json), on-disk changes
// waiting for approval, recent messages, and removal.

import { get, post, put, del, qs } from './api.js';
import { routingEditor } from './routing-editor.js';
import { activityItem } from './page-activity.js';
import { calendarCard } from './calendar.js';
import {
  h, mount, icon, pill, badge, btn, callout, timeAgo, fmtDuration, mono, chatEl, plural, field,
  textInput, select, toggle, linesArea, linesOf, outcomePill, brainStatusPill, empty, changed,
  spinner, confirmDialog, toast, prettyJSON, EFFORTS, runBusy, phone, loading, isGroupJid,
} from './ui.js';

export default {
  title: (params) => `Brain ${params[0] || ''}`,
  render(root, ctx) {
    const id = ctx.params[0];
    const memo = {};
    let brain = null;
    let brains = [];
    let cfg = null;
    let formDirty = false;
    let formBase = ''; // manifest JSON the form was built from

    const head = h('div');
    const issuesBox = h('div.alerts');
    const driftBox = h('div');
    const liveBox = h('div');
    const formBox = h('div', h('div.card', loading('Loading settings…')));
    const actBox = h('div.activity-list.compact');
    const dangerBox = h('div');

    mount(root,
      head,
      issuesBox,
      driftBox,
      h('section.section', h('h2.section-title', 'Now'), liveBox),
      h('section.section', h('h2.section-title', 'Settings'), formBox),
      h('section.section', h('h2.section-title', 'Calendar'), calendarCard(id)),
      h('section.section',
        h('div.section-head', h('h2.section-title', 'Recent messages'), btn('All of this brain’s activity', { href: `#/activity/${encodeURIComponent(id)}`, small: true, kind: 'ghost', icon: 'right' })),
        actBox),
      dangerBox);

    ctx.dirty = () => formDirty;

    get('/api/config').then((c) => { cfg = c; if (brain && !formDirty) renderForm(true); }).catch(() => {});

    const poller = ctx.poll(async () => {
      const list = await get('/api/brains');
      brains = list;
      const b = list.find((x) => x.id === id) || null;
      if (!changed(memo, 'brain', b)) return;
      brain = b;
      if (!b) {
        mount(head, h('header.page-header', h('div.page-heading', h('a.back-link', { href: '#/brains' }, icon('left'), 'Brains'), h('h1', id))));
        mount(issuesBox, empty('No such brain', `There is no brain with the id “${id}”. It may have been removed.`, btn('All brains', { href: '#/brains', kind: 'primary' })));
        mount(driftBox); mount(liveBox); mount(formBox); mount(dangerBox);
        return;
      }
      renderHead();
      renderIssues();
      renderDrift();
      renderLive();
      renderForm(false);
      renderDanger();
    }, 3000);

    ctx.poll(async () => {
      const items = await get(`/api/activity${qs({ limit: 30, brain: id })}`);
      if (!changed(memo, 'act', items)) return;
      mount(actBox, items.length ? items.map((e) => activityItem(e, { compact: true, showBrain: false })) : empty('No messages yet', 'Messages this brain receives and sends show up here.'));
    }, 3000);

    function renderHead() {
      const b = brain;
      mount(head, h('header.page-header',
        h('div.page-heading',
          h('a.back-link', { href: '#/brains' }, icon('left'), 'Brains'),
          h('h1', b.name || b.id),
          h('div.head-meta',
            brainStatusPill(b.status),
            b.source === 'inline' ? badge('config.json', 'neutral', 'Configured inline in config.json') : badge('brain.json', 'neutral', 'Settings live in the folder’s brain.json'),
            h('code.mono.muted', b.id)),
          h('div.path-line', icon('folder'), mono(b.path, { copy: true })))));
    }

    function renderIssues() {
      const issues = (brain.issues || []).filter((is) => is.kind !== 'drift');
      mount(issuesBox, issues.map((is) => callout('bad', { missing: 'Folder missing', invalid: 'Invalid configuration', unapproved: 'Not approved' }[is.kind] || is.kind, is.detail)));
    }

    // ── on-disk changes ──
    function renderDrift() {
      const b = brain;
      if (b.status !== 'drift' && !(b.issues || []).some((is) => is.kind === 'drift')) { mount(driftBox); return; }
      if (!b.disk_manifest) {
        const d = (b.issues || []).find((is) => is.kind === 'drift');
        mount(driftBox, callout('warn', 'brain.json on disk cannot be read', d ? d.detail : '', h('div.muted.small', 'The bridge keeps running the approved version. Fix the file, or save the settings below to overwrite it.')));
        return;
      }
      const a = b.manifest || {};
      const d = b.disk_manifest;
      const rows = fieldDiff(a, d);
      const sensitive = rows.some((r) => /^(routing|disallowed_tools|prompt_file|reply_chat)/.test(r.path));
      const approveBtn = btn('Approve changes', {
        kind: 'primary', icon: 'check',
        onClick: async () => {
          if (sensitive) {
            const ok = await confirmDialog({ title: 'Approve these changes?', message: 'They change which chats reach this brain or what it may do. Only approve changes you or someone you trust made.', confirm: 'Approve', danger: false });
            if (!ok) return;
          }
          await post(`/api/brains/${encodeURIComponent(id)}/approve`);
          toast('Changes approved — the brain now runs the new brain.json', 'ok');
          memo.brain = '';
          poller.kick();
        },
      });
      const { left, right } = lineDiff(prettyJSON(sortKeys(a)), prettyJSON(sortKeys(d)));
      mount(driftBox, h('section.card.drift-card',
        h('div.card-head', h('div.card-title', icon('alert'), h('h2', 'brain.json changed on disk')), pill('Waiting for approval', 'warn')),
        h('p', 'Someone — possibly the brain itself — edited ', mono(`${b.path}/brain.json`), '. The bridge keeps running the version you approved until you accept the change.'),
        sensitive ? callout('warn', 'Check routing and tool changes carefully', 'A brain can edit its own brain.json. Changes to routing decide which chats reach it; changes to tools decide what it may do.') : null,
        rows.length ? h('div.table-wrap', h('table.table.diff-table',
          h('thead', h('tr', h('th', 'Setting'), h('th', 'Approved'), h('th', 'On disk'))),
          h('tbody', rows.map((r) => h(`tr${/^(routing|disallowed_tools)/.test(r.path) ? '.sensitive' : ''}`,
            h('td', h('code.mono', r.path)),
            h('td.diff-old', h('code.mono', r.a)),
            h('td.diff-new', h('code.mono', r.b))))))) : h('p.muted', 'Only formatting differs.'),
        h('details.details', h('summary', 'Side-by-side JSON'),
          h('div.diff-cols',
            h('div', h('div.diff-label', 'Approved'), h('pre.code.diff', left.map((l) => h(`span.diff-line${l.changed ? '.del' : ''}`, l.text)))),
            h('div', h('div.diff-label', 'On disk'), h('pre.code.diff', right.map((l) => h(`span.diff-line${l.changed ? '.add' : ''}`, l.text)))))),
        h('div.actions', approveBtn, h('span.muted.small', 'To reject, edit the file back — or save the settings below, which overwrites it with the approved version plus your edits.'))));
    }

    // ── live state ──
    function renderLive() {
      const rt = brain.runtime;
      if (!rt) {
        mount(liveBox, h('div.card', h('p.muted', brain.status === 'ok' ? 'This brain is not running yet.' : 'This brain is not running until the problem above is fixed.')));
        return;
      }
      const run = rt.running;
      const queue = rt.queue || [];
      const last = rt.last;
      const stop = (clear) => btn(clear ? 'Stop & clear queue' : 'Stop', {
        kind: clear ? 'danger' : '', icon: 'stop', small: true,
        onClick: async () => {
          if (clear && queue.length) {
            const ok = await confirmDialog({ title: 'Stop and clear the queue?', message: `The current run stops and ${plural(queue.length, 'waiting message')} will not be answered.`, confirm: 'Stop & clear', danger: true });
            if (!ok) return;
          }
          const r = await post(`/api/runs/${encodeURIComponent(id)}/stop`, { clear_queue: clear });
          toast(r && r.stopped ? 'Stopped' : 'Nothing was running', r && r.stopped ? 'ok' : 'info');
          memo.brain = '';
          poller.kick();
        },
      });
      mount(liveBox, h('div.grid.two',
        h('div.card',
          h('div.card-head', h('div.card-title', h('h3', 'Current run')), run ? pill('Working', 'info', { pulse: true }) : pill('Idle', 'neutral')),
          run ? h('div.card-body',
            h('div.run-meta', h('span', 'From ', h('strong', run.sender ? phoneish(run.sender) : 'someone')), isGroupJid(run.chat) ? [h('span', 'in'), chatEl(run.chat)] : h('span.muted', '(direct message)'), run.silent ? badge('silent', 'neutral', 'Untagged group message: the brain reads along but does not reply') : null),
            run.preview ? h('blockquote.preview', run.preview) : null,
            h('div.muted.small', timeAgo(run.started, 'Started '), run.pid ? ` · pid ${run.pid}` : ''),
            h('div.actions', stop(false), stop(true)))
            : h('div.card-body', h('p.muted', 'Waiting for messages.'), queue.length ? h('div.actions', stop(true)) : null)),
        h('div.card',
          h('div.card-head', h('div.card-title', h('h3', 'Queue')), queue.length ? badge(`${queue.length} waiting`, 'warn') : badge('empty', 'neutral')),
          queue.length ? h('ol.queue-list', queue.map((q) => h('li',
            h('div.run-meta', h('strong', q.sender ? phoneish(q.sender) : 'someone'), isGroupJid(q.chat) ? [h('span.muted', 'in'), chatEl(q.chat)] : h('span.muted', '(direct message)'), q.silent ? badge('silent', 'neutral') : null, h('span.muted.small', timeAgo(q.queued))),
            q.preview ? h('div.preview-line', q.preview) : null)))
            : h('p.muted', 'No messages waiting.'),
          h('div.last-run',
            h('h4', 'Last run'),
            last ? h('div',
              h('div.run-meta', outcomePill(last), chatEl(last.chat), h('span.muted.small', timeAgo(last.finished)), h('span.muted.small', `took ${fmtDuration(last.seconds)}`)),
              last.error ? h('p.error-text.small', last.error) : null)
              : h('p.muted', 'No runs since the bridge started.')))));
    }

    // ── settings form ──
    function renderForm(force) {
      const m = brain.manifest;
      if (!m) { mount(formBox, h('div.card', h('p.muted', 'This brain has no approved settings to edit.'))); return; }
      const base = JSON.stringify(m);
      if (!force && base === formBase && formBox.querySelector('form')) return;
      if (formDirty && base !== formBase) {
        const note = formBox.querySelector('.form-stale');
        if (note) note.hidden = false;
        return;
      }
      formBase = base;
      formDirty = false;
      const eff = (cfg && cfg.effective) || {};
      const allowedGroups = (cfg && cfg.config && cfg.config.allowed_groups) || eff.allowed_groups || [];
      const markDirty = () => { formDirty = true; saveBar.hidden = false; };

      const nameIn = textInput({ value: m.name || '', spellcheck: 'true' });
      const routing = routingEditor({ routing: m.routing || {}, selfId: id, brains, allowedGroups, onChange: markDirty });
      const modelIn = textInput({ value: m.model || '', class: 'mono', placeholder: eff.brain_model ? `inherit (${eff.brain_model})` : 'inherit' });
      const effortSel = select(EFFORTS.map(([v, l]) => [v, v === '' && eff.brain_effort ? `Inherit (${eff.brain_effort})` : l]), m.effort || '');
      const override = Array.isArray(m.disallowed_tools);
      const globalTools = eff.disallowed_tools && eff.disallowed_tools.length ? eff.disallowed_tools.join(', ') : 'none';
      const toolsIn = textInput({ value: override ? m.disallowed_tools.join(', ') : '', class: 'mono', placeholder: 'e.g. WebFetch, Bash', disabled: !override });
      const toolsSw = toggle({
        checked: override,
        label: 'Override global tool restrictions',
        onChange: (v) => { toolsIn.disabled = !v; if (v) toolsIn.focus(); },
      });
      const promptIn = textInput({ value: m.prompt_file || '', class: 'mono', placeholder: 'brain-prompt.md' });
      const replyIn = textInput({ value: m.reply_chat || '', class: 'mono', placeholder: 'derived from routing' });
      const backupSw = toggle({ checked: m.backup ? !!m.backup.enabled : true, label: 'Include in Google Drive backups' });
      const excludes = linesArea((m.backup && m.backup.exclude) || [], { rows: 5, placeholder: 'node_modules/\n*.log' });

      const saveBtn = btn('Save settings', { kind: 'primary', type: 'submit', icon: 'check' });
      const saveBar = h('div.save-bar', { hidden: true },
        h('span', 'Unsaved changes'),
        btn('Discard', { onClick: () => { formDirty = false; renderForm(true); } }),
        saveBtn);
      const stale = h('div.callout.warn.form-stale', { hidden: true },
        icon('alert', 'callout-icon'),
        h('div.callout-body', h('strong', 'These settings changed elsewhere'), h('div', 'Saving now would overwrite that change.'),
          h('div.actions', btn('Load the new settings', { small: true, onClick: () => { formDirty = false; renderForm(true); } }))));

      const form = h('form.card.form.brain-form',
        stale,
        brain.source === 'inline' ? callout('info', null, 'Saving moves this brain’s settings into ', mono(`${brain.path}/brain.json`), '. From then on the folder carries its own settings.') : null,
        brain.status === 'drift' ? callout('warn', null, 'brain.json on disk differs from the approved version (see above). Saving overwrites it.') : null,
        h('div.form-grid',
          field({ label: 'Name', control: nameIn }),
          field({ label: 'Id', control: h('div.static', mono(m.id)), help: 'Ids cannot change. To use a new id, remove the brain and add the folder again.' })),
        h('fieldset.fieldset', h('legend', 'Chats'), routing),
        h('fieldset.fieldset', h('legend', 'Model and tools'),
          h('div.form-grid',
            field({ label: 'Model', control: modelIn, help: 'Empty uses the global brain model.' }),
            field({ label: 'Effort', control: effortSel })),
          h('div.field', toolsSw, h('div.help', `Off: use the global list of blocked tools (${globalTools}).`)),
          field({ label: 'Blocked tools', control: toolsIn, help: 'Comma-separated tool names this brain may not use. Leave empty to allow every tool.' })),
        h('fieldset.fieldset', h('legend', 'Files and replies'),
          h('div.form-grid',
            field({ label: 'Prompt file', control: promptIn, help: 'The brain’s instructions, relative to its folder.' }),
            field({ label: 'Reply chat', control: replyIn, help: 'Where replies go for files handed in through /ingest. This brain handles the files even when that chat belongs to another brain. Default: your first allowed number for the brain that gets DMs, else its first group.' }))),
        h('fieldset.fieldset', h('legend', 'Backup'),
          h('div.field', backupSw),
          field({ label: 'Leave out of backups', control: excludes, help: 'One pattern per line, relative to the brain folder. A trailing / means a folder.' })),
        saveBar);
      form.addEventListener('input', markDirty);
      form.addEventListener('change', markDirty);
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        if (!nameIn.value.trim()) { nameIn.focus(); toast('The brain needs a name'); return; }
        const manifest = {
          ...m,
          name: nameIn.value.trim(),
          routing: routing.getValue(),
          model: modelIn.value.trim(),
          effort: effortSel.value,
          prompt_file: promptIn.value.trim(),
          reply_chat: replyIn.value.trim(),
          backup: { enabled: backupSw.input.checked, exclude: linesOf(excludes) },
        };
        if (toolsSw.input.checked) manifest.disallowed_tools = toolsIn.value.split(',').map((s) => s.trim()).filter(Boolean);
        else delete manifest.disallowed_tools;
        runBusy(saveBtn, async () => {
          await put(`/api/brains/${encodeURIComponent(id)}`, { manifest });
          formDirty = false;
          saveBar.hidden = true;
          toast(brain.source === 'inline' ? 'Saved — settings now live in brain.json' : 'Settings saved', 'ok');
          memo.brain = '';
          formBase = '';
          poller.kick();
        });
      });
      mount(formBox, form);
    }

    function renderDanger() {
      if (memo.danger) return;
      memo.danger = '1';
      mount(dangerBox, h('section.section.danger-zone',
        h('h2.section-title', 'Remove'),
        h('div.card.danger-card',
          h('div', h('strong', 'Remove this brain'), h('p.muted', 'The bridge stops routing messages to it. The folder and everything in it stay exactly as they are — you can add it again later.')),
          btn('Remove brain…', {
            kind: 'danger', icon: 'trash',
            onClick: async () => {
              const ok = await confirmDialog({
                title: `Remove ${brain ? brain.name || id : id}?`,
                message: h('div',
                  h('p', 'The bridge stops sending messages to this brain. Its chats fall back to the default brain, if there is one.'),
                  h('p', h('strong', 'Nothing is deleted: '), 'the folder ', mono(brain ? brain.path : ''), ' and its files, memory and history stay where they are.')),
                confirm: 'Remove brain',
                danger: true,
              });
              if (!ok) return;
              await del(`/api/brains/${encodeURIComponent(id)}`);
              formDirty = false;
              ctx.dirty = null;
              toast(`Removed ${id}. Its folder was left untouched.`, 'ok');
              ctx.navigate('#/brains');
            },
          }))));
    }
  },
};

function phoneish(s) {
  return /^\d{6,}$/.test(s) ? phone(s) : s;
}

// ── diff helpers ─────────────────────────────────────────────────────────

function sortKeys(v) {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (v && typeof v === 'object') {
    const out = {};
    for (const k of Object.keys(v).sort()) out[k] = sortKeys(v[k]);
    return out;
  }
  return v;
}

function flatten(v, prefix, out) {
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    for (const k of Object.keys(v)) flatten(v[k], prefix ? `${prefix}.${k}` : k, out);
  } else {
    out.set(prefix, v);
  }
  return out;
}

function show(v) {
  if (v === undefined) return '(not set)';
  if (Array.isArray(v)) return v.length ? v.map((x) => JSON.stringify(x)).join('\n') : '[]';
  return JSON.stringify(v);
}

function fieldDiff(a, b) {
  const fa = flatten(a, '', new Map());
  const fb = flatten(b, '', new Map());
  const keys = [...new Set([...fa.keys(), ...fb.keys()])].sort();
  const rows = [];
  for (const k of keys) {
    const x = show(fa.get(k));
    const y = show(fb.get(k));
    const empty = (s) => s === '(not set)' || s === '""' || s === '[]' || s === 'null';
    if (x !== y && !(empty(x) && empty(y))) rows.push({ path: k, a: x, b: y });
  }
  return rows;
}

/** Line diff via LCS; marks lines that only appear on one side. */
function lineDiff(aText, bText) {
  const a = aText.split('\n');
  const b = bText.split('\n');
  const n = a.length;
  const m = b.length;
  const dp = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  }
  const keepA = new Array(n).fill(false);
  const keepB = new Array(m).fill(false);
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) { keepA[i] = true; keepB[j] = true; i++; j++; } else if (dp[i + 1][j] >= dp[i][j + 1]) i++; else j++;
  }
  return {
    left: a.map((text, k) => ({ text, changed: !keepA[k] })),
    right: b.map((text, k) => ({ text, changed: !keepB[k] })),
  };
}
