// One brain: live run and queue, settings (brain.json), on-disk changes
// waiting for approval, recent messages, and removal.

import { post, put, del, qs } from '../app/api.js';
import { store } from '../app/store.js';
import { brainsResource, configResource, resource } from '../app/resources.js';
import { routingEditor } from '../components/routing-editor.js';
import { showActivity } from '../components/activity-item.js';
import { calendarCard } from '../components/calendar.js';
import { h, mount, paint } from '../lib/dom.js';
import { fmtDuration, plural, prettyJSON } from '../lib/format.js';
import { phoneish, isGroupJid } from '../lib/jid.js';
import { OBSERVE, issueLabel, brainLabel, takeover } from '../lib/brain.js';
import { sortKeys, fieldDiff, lineDiff } from '../lib/diff.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn, runBusy } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { field, textInput, select, toggle } from '../components/fields.js';
import { brainSettingsFields } from '../components/brain-fields.js';
import { pill, badge, callout, timeAgo, mono, empty, loading } from '../components/common.js';
import { chatEl, chatName } from '../components/chat.js';
import { outcomePill, brainStatusPill } from '../components/brain.js';
import { saveBar } from '../components/save-bar.js';
import { liveForm } from '../components/live-form.js';
import { grantsEditor } from '../components/grants-editor.js';

export default {
  title: (params) => `Brain ${params.id}`,
  render(root, ctx) {
    const id = ctx.params.id;
    // The brain, followed on its own; null when there is none.
    const one = resource(`/api/brains/${encodeURIComponent(id)}`, { missing: null });
    let brain; // undefined until it is in
    let brains = null; // the /api/brains list, once it is in: the other brains' routing
    let cfg = null;

    const head = h('div');
    const issuesBox = h('div.alerts');
    const driftBox = h('div');
    const liveBox = h('div');
    const formBox = h('div', h('div.card', loading('Loading settings…')));
    const actBox = h('div.activity-list.compact');
    const dangerBox = h('div');

    // The brain's own sections, which a brain that does not exist lacks.
    const sections = [
      h('section.section', h('h2.section-title', 'Now'), liveBox),
      h('section.section', h('h2.section-title', 'Settings'), formBox),
      h('section.section', h('h2.section-title', 'Calendar'), calendarCard(id, { signal: ctx.signal })),
      h('section.section',
        h('div.section-head', h('h2.section-title', 'Recent messages'), btn('All of this brain’s activity', { href: `#/activity/${encodeURIComponent(id)}`, small: true, kind: 'ghost', icon: 'right' })),
        actBox),
    ];
    mount(root, head, issuesBox, driftBox, sections, dangerBox);

    const refresh = () => Promise.all([one.refresh(), brainsResource.refresh()]);
    const settings = liveForm({
      build: buildForm,
      edits: (d) => d.manifest,
      onChange: ({ dirty }) => { const f = settings.form(); if (f) f.bar[dirty ? 'show' : 'hide'](); },
    });
    ctx.dirty = () => settings.dirty();

    // The form shows the inherited model, effort and groups from the config,
    // which seldom changes.
    ctx.follow(configResource, (c) => {
      cfg = c;
      paintAll();
    }, 60000);

    // The page shows its brain's runs as they happen, and only the routing
    // of the others, for the form, which changes when someone edits them.
    // A brain that is not there is asked after every 30 s only: the
    // browser logs each 404 as an error, and someone has to add it first.
    ctx.follow(one, (b) => {
      brain = b;
      paintAll();
    }, () => (brain === null ? 30000 : 3000));
    ctx.follow(brainsResource, (list) => {
      brains = list || [];
      paintAll();
    }, 60000);

    function paintAll() {
      if (brain === undefined) return;
      if (!brain) {
        if (settings.dirty()) settings.discard(); // its form is gone from the page, and so are the edits
        paint(head, { missing: id }, () => h('header.page-header', h('div.page-heading', h('a.back-link', { href: '#/brains' }, icon('left'), 'Brains'), h('h1', id))));
        paint(issuesBox, 'missing', () => empty('No such brain', `There is no brain with the id “${id}”. It may have been removed.`, btn('All brains', { href: '#/brains', kind: 'primary' })));
        [driftBox, liveBox, formBox, dangerBox].forEach((box) => paint(box, 'missing', () => null));
        sections.forEach((s) => { s.hidden = true; });
        return;
      }
      sections.forEach((s) => { s.hidden = false; });
      ctx.setTitle(brain.name || brain.id);
      paint(head, { name: brain.name, id: brain.id, status: brain.status, path: brain.path }, renderHead);
      paint(issuesBox, (brain.issues || []).filter((is) => is.kind !== 'drift'), renderIssues);
      paintDrift();
      paintLive();
      paintForm();
      paint(dangerBox, 'shown', renderDanger);
    }

    // ── recent messages ──
    let items = null;
    const paintActivity = () => {
      if (!items) return;
      if (!items.length) {
        paint(actBox, 'none', () => empty('No messages yet', 'Messages this brain receives and sends show up here.'));
        return;
      }
      paint(actBox, 'messages', () => null);
      showActivity(actBox, items, { compact: true, showBrain: false });
    };
    ctx.follow(resource(`/api/activity${qs({ limit: 30, brain: id })}`), (data) => {
      items = data || [];
      paintActivity();
    }, 3000);
    // Chat labels show group names once the groups have loaded.
    ctx.watch(store, (s) => s.names, () => {
      paintActivity();
      paintLive();
    });

    function renderHead(b) {
      return h('header.page-header',
        h('div.page-heading',
          h('a.back-link', { href: '#/brains' }, icon('left'), 'Brains'),
          h('h1', b.name || b.id),
          h('div.head-meta',
            brainStatusPill(b.status),
            h('code.mono.muted', b.id)),
          h('div.path-line', icon('folder'), mono(b.path, { copy: true }))));
    }

    function renderIssues(issues) {
      return issues.map((is) => callout('bad', issueLabel(is.kind), is.detail));
    }

    // ── on-disk changes ──
    function paintDrift() {
      const b = brain;
      const drift = (b.issues || []).find((is) => is.kind === 'drift');
      if (b.status !== 'drift' && !drift) { paint(driftBox, null, () => null); return; }
      paint(driftBox, { manifest: b.manifest, disk: b.disk_manifest || null, detail: drift ? drift.detail : '', path: b.path }, renderDrift);
    }

    function renderDrift({ manifest, disk, detail, path }) {
      if (!disk) {
        return callout('warn', 'brain.json on disk cannot be read', detail, h('div.muted.small', 'The bridge keeps running the approved version. Fix the file, or save the settings below to overwrite it.'));
      }
      const a = manifest || {};
      const rows = fieldDiff(a, disk);
      const sensitive = rows.some((r) => /^(routing|disallowed_tools|prompt_file|reply_chat|role|grants)/.test(r.path));
      const approveBtn = btn('Approve changes', {
        kind: 'primary', icon: 'check',
        onClick: async () => {
          if (sensitive) {
            const ok = await confirmDialog({ title: 'Approve these changes?', message: 'They change which chats reach this brain, what it may do or which other brains it may reach. Only approve changes you or someone you trust made.', confirm: 'Approve', danger: false });
            if (!ok) return;
          }
          // The bridge approves only the brain.json shown here: if the brain
          // changed it again meanwhile, it answers 409 and the page shows
          // the new changes.
          try {
            await post(`/api/brains/${encodeURIComponent(id)}/approve`, { manifest: disk });
          } catch (e) {
            if (e.status !== 409) throw e;
            toast(e.message, 'warn');
            await refresh();
            return;
          }
          toast('Changes approved — the brain now runs the new brain.json', 'ok');
          await refresh();
        },
      });
      const { left, right } = lineDiff(prettyJSON(sortKeys(a)), prettyJSON(sortKeys(disk)));
      return h('section.card.drift-card',
        h('div.card-head', h('div.card-title', icon('alert'), h('h2', 'brain.json changed on disk')), pill('Waiting for approval', 'warn')),
        h('p', 'Someone — possibly the brain itself — edited ', mono(`${path}/brain.json`), '. The bridge keeps running the version you approved until you accept the change.'),
        sensitive ? callout('warn', 'Check routing and tool changes carefully', 'A brain can edit its own brain.json. Changes to routing decide which chats reach it; changes to tools decide what it may do.') : null,
        rows.length ? h('div.table-wrap', h('table.table.diff-table',
          h('thead', h('tr', h('th', 'Setting'), h('th', 'Approved'), h('th', 'On disk'))),
          h('tbody', rows.map((r) => h(`tr${/^(routing|disallowed_tools|role|grants)/.test(r.path) ? '.sensitive' : ''}`,
            h('td', h('code.mono', r.path)),
            h('td.diff-old', h('code.mono', r.a)),
            h('td.diff-new', h('code.mono', r.b))))))) : h('p.muted', 'Only formatting differs.'),
        h('details.details', h('summary', 'Side-by-side JSON'),
          h('div.diff-cols',
            h('div', h('div.diff-label', 'Approved'), h('pre.code.diff', left.map((l) => h(`span.diff-line${l.changed ? '.del' : ''}`, l.text)))),
            h('div', h('div.diff-label', 'On disk'), h('pre.code.diff', right.map((l) => h(`span.diff-line${l.changed ? '.add' : ''}`, l.text)))))),
        h('div.actions', approveBtn, h('span.muted.small', 'To reject, edit the file back — or save the settings below, which overwrites it with the approved version plus your edits.')));
    }

    // ── live state ──
    function paintLive() {
      if (!brain) return;
      const rt = brain.runtime || null;
      // The chats it names, so their labels follow the names learnt.
      const chats = rt ? [rt.running && rt.running.chat, ...(rt.queue || []).map((q) => q.chat), rt.last && rt.last.chat].filter(Boolean) : [];
      paint(liveBox, { rt, status: brain.status, names: chats.map(chatName) }, renderLive);
    }

    function renderLive({ rt, status }) {
      if (!rt) {
        return h('div.card', h('p.muted', status === 'ok' ? 'This brain is not running yet.' : 'This brain is not running until the problem above is fixed.'));
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
          const r = await post(`/api/brains/${encodeURIComponent(id)}/stop`, { clear_queue: clear });
          toast(r && r.stopped ? 'Stopped' : 'Nothing was running', r && r.stopped ? 'ok' : 'info');
          await refresh();
        },
      });
      return h('div.grid.two',
        h('div.card',
          h('div.card-head', h('div.card-title', h('h3', 'Current run')), run ? pill('Working', 'info', { pulse: true }) : pill('Idle', 'neutral')),
          run ? h('div.card-body',
            h('div.run-meta', h('span', 'From ', h('strong', run.sender ? phoneish(run.sender) : 'someone')), isGroupJid(run.chat) ? [h('span', 'in'), chatEl(run.chat)] : h('span.muted', '(direct message)'), run.silent ? badge('silent', 'neutral', 'Untagged group message: an observe-only run, which sends nothing') : null),
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
              : h('p.muted', 'No runs since the bridge started.'))));
    }

    // ── settings form ──
    // What the form shows: the approved manifest it edits, and what it needs
    // to explain it (the inherited settings, the other brains' routing).
    function paintForm() {
      if (!brains) return; // it says which groups the other brains have
      const m = brain.manifest;
      if (!m) {
        if (settings.dirty()) settings.discard(); // nothing left on the page to save
        paint(formBox, 'none', () => h('div.card', h('p.muted', 'This brain has no approved settings to edit.')));
        return;
      }
      paint(formBox, 'form', () => settings.el);
      const eff = (cfg && cfg.effective) || {};
      settings.set({
        manifest: m,
        drift: brain.status === 'drift',
        eff: { brain_model: eff.brain_model || '', brain_effort: eff.brain_effort || '', disallowed_tools: eff.disallowed_tools || [] },
        allowedGroups: (cfg && cfg.config && cfg.config.allowed_groups) || eff.allowed_groups || [],
        others: brains.filter((b) => b.id && b.id !== id && b.manifest).map((b) => ({ id: b.id, name: b.name, manifest: { routing: b.manifest.routing, role: b.manifest.role || '' } })),
      });
    }

    function buildForm({ manifest: m, drift, eff, allowedGroups, others }, changed) {
      const nameIn = textInput({ value: m.name || '', spellcheck: 'true' });
      const routing = routingEditor({ routing: m.routing || {}, selfId: id, brains: others, allowedGroups, onChange: changed, signal: ctx.signal });
      const fields = brainSettingsFields(m, eff);
      const observeSel = select(OBSERVE, m.observe || 'history', { name: 'observe' });
      const override = Array.isArray(m.disallowed_tools);
      const globalTools = eff.disallowed_tools.length ? eff.disallowed_tools.join(', ') : 'none';
      const toolsIn = textInput({ value: override ? m.disallowed_tools.join(', ') : '', class: 'mono', placeholder: 'e.g. WebFetch, Bash', disabled: !override });
      const toolsSw = toggle({
        checked: override,
        label: 'Override global tool restrictions',
        onChange: (v) => { toolsIn.disabled = !v; if (v) toolsIn.focus(); },
      });
      const promptIn = textInput({ value: m.prompt_file || '', class: 'mono', placeholder: 'brain-prompt.md' });
      const replyIn = textInput({ value: m.reply_chat || '', class: 'mono', placeholder: 'derived from routing' });
      // The core brain and its grants: the one brain that may reach others.
      const isCore = m.role === 'core';
      const otherCore = others.find((b) => b.manifest.role === 'core');
      const grants = grantsEditor({ grants: m.grants || {}, brains: others, onChange: changed });
      const grantsBox = h('div', { hidden: !isCore },
        field({ label: 'Grants', control: grants, help: 'What this brain may do with each other brain. A delegated task runs in that brain, with its own instructions, memory and sandbox, and its answer comes back here only; that brain cannot reach this one or any other.' }));
      const coreSw = toggle({
        checked: isCore,
        disabled: !isCore && !!otherCore,
        label: 'Core brain',
        onChange: (v) => { grantsBox.hidden = !v; },
      });

      // The manifest a save would write.
      const value = () => {
        const manifest = {
          ...m,
          name: nameIn.value.trim(),
          routing: routing.getValue(),
          ...fields.value(),
          observe: observeSel.value,
          prompt_file: promptIn.value.trim(),
          reply_chat: replyIn.value.trim(),
        };
        if (toolsSw.input.checked) manifest.disallowed_tools = toolsIn.value.split(',').map((t) => t.trim()).filter(Boolean);
        else delete manifest.disallowed_tools;
        delete manifest.role;
        delete manifest.grants;
        if (coreSw.input.checked) {
          manifest.role = 'core';
          const g = grants.getValue();
          if (Object.keys(g).length) manifest.grants = g;
        }
        return manifest;
      };

      const saveBtn = btn('Save settings', { kind: 'primary', type: 'submit', icon: 'check' });
      const bar = saveBar({ save: saveBtn, onDiscard: () => settings.discard() });
      const form = h('form.card.form.brain-form',
        drift ? callout('warn', null, 'brain.json on disk differs from the approved version (see above). Saving overwrites it.') : null,
        h('div.form-grid',
          field({ label: 'Name', control: nameIn }),
          field({ label: 'Id', control: h('div.static', mono(m.id)), help: 'Ids cannot change. To use a new id, remove the brain and add the folder again.' })),
        h('fieldset.fieldset', h('legend', 'Chats'), routing,
          field({ label: 'Untagged group messages', control: observeSel, help: 'Messages in a group that nobody addressed to the brain. They go into its history either way, for its next run there, and get no reply. A run of its own for each lets the brain take notes as the conversation goes, but costs a full Claude run, with tools, on whatever any member the group admits (Chats → Allowed groups → Members) writes; a message addressed to the brain stops it.' })),
        h('fieldset.fieldset', h('legend', 'Model and tools'),
          fields.model,
          h('div.field', toolsSw, h('div.help', `Off: use the global list of blocked tools (${globalTools}).`)),
          field({ label: 'Blocked tools', control: toolsIn, help: 'Comma-separated tool names this brain may not use. Leave empty to allow every tool.' })),
        h('fieldset.fieldset', h('legend', 'Files and replies'),
          h('div.form-grid',
            field({ label: 'Prompt file', control: promptIn, help: 'The brain’s instructions, relative to its folder.' }),
            field({ label: 'Reply chat', control: replyIn, help: 'Where replies go for files handed in through /ingest. This brain handles the files even when that chat belongs to another brain. Default: your first allowed number for the brain that gets DMs (the DM brain, or the fallback brain when none handles DMs), else its first group.' }))),
        h('fieldset.fieldset.grants', h('legend', 'Other brains'),
          h('div.field', coreSw, h('div.help', otherCore && !isCore
            ? `${otherCore.name || otherCore.id} is the core brain, and there is one. To make this one the core, turn it off there first.`
            : 'The core brain may hand tasks to the other brains and read their notes, as far as you grant it below. Other brains cannot reach any brain.')),
          grantsBox),
        h('fieldset.fieldset', h('legend', 'Backup'), fields.backup),
        bar);
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        if (!nameIn.value.trim()) { nameIn.focus(); toast('The brain needs a name'); return; }
        runBusy(saveBtn, async () => {
          await put(`/api/brains/${encodeURIComponent(id)}`, { manifest: value() });
          settings.saved();
          toast('Settings saved', 'ok');
          await refresh();
        });
      });
      return { el: form, value, bar };
    }

    // Which brain answers this brain's chats once it is gone (see
    // takeover), from the list as the page has it when the dialog opens.
    // Removing the DM brain, or a fallback brain that answers the DMs no
    // brain handles, hands your direct messages to that brain: it warns.
    function removalNotes() {
      const stops = 'The bridge stops sending messages to this brain. ';
      if (!brains) return h('p', stops, 'Its chats go to the fallback brain, if another brain is left.');
      const { brain: next, dms, copy } = takeover(brains, id);
      if (copy) return h('p', stops, 'Another folder with this id stays in the list, ', mono(next.path), ': it takes this brain’s place, for the chats its own brain.json names.');
      if (!next) return h('p', stops, 'No other brain takes its chats over, so they go unanswered until you add one.');
      const name = h('strong', brainLabel(next));
      return [
        brains.some((b) => b.id === id && b.fallback)
          ? h('p', stops, 'It is the fallback brain, so its chats and those no other brain claims go to ', name, ', which becomes the fallback.')
          : h('p', stops, 'Its chats go to the fallback brain, ', name, '.'),
        dms ? callout('warn', null, `${brainLabel(next)} will also answer your direct messages, since no other brain handles them. To give them to another brain, turn on “Handles direct messages” there first.`) : null,
      ];
    }

    function renderDanger() {
      return h('section.section.danger-zone',
        h('h2.section-title', 'Remove'),
        h('div.card.danger-card',
          h('div', h('strong', 'Remove this brain'), h('p.muted', 'The bridge stops routing messages to it. The folder and everything in it stay exactly as they are — you can add it again later.')),
          btn('Remove brain…', {
            kind: 'danger', icon: 'trash',
            onClick: async () => {
              const ok = await confirmDialog({
                title: `Remove ${brain ? brain.name || id : id}?`,
                message: h('div',
                  removalNotes(),
                  h('p', h('strong', 'Nothing is deleted: '), 'the folder ', mono(brain ? brain.path : ''), ' and its files, memory and history stay where they are.')),
                confirm: 'Remove brain',
                danger: true,
              });
              if (!ok) return;
              await del(`/api/brains/${encodeURIComponent(id)}`);
              ctx.dirty = null; // its settings are gone with it
              toast(`Removed ${id}. Its folder was left untouched.`, 'ok');
              ctx.navigate('#/brains');
            },
          })));
    }
  },
};
