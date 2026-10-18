// Backup: Google Drive via rclone — connect, schedule, per-brain settings,
// run now, progress, and restore into a new folder.

import { get, post, put, qs } from '../app/api.js';
import { resource } from '../app/resources.js';
import { folderBrowser } from '../components/folder-browser.js';
import { liveForm } from '../components/live-form.js';
import { h, mount, paint, reconcile, syncOptions } from '../lib/dom.js';
import { fmtDuration, fmtBytes, plural, absTime } from '../lib/format.js';
import { brainLabel } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn, copyButton, runBusy } from '../components/buttons.js';
import { openDialog, confirmDialog } from '../components/dialog.js';
import { field, textInput, toggle, select, linesArea, linesOf, nextId } from '../components/fields.js';
import { pill, callout, timeAgo, mono, empty, pageHeader, spinner, loading } from '../components/common.js';

// What a backup job is working on, as the progress card names it.
const jobLabel = (id, brains) => {
  if (id === '_bridge') return 'Bridge settings';
  if (id && id.startsWith('restore:')) return `Restoring ${id.slice(8)}`;
  const b = (brains || []).find((x) => x.id === id);
  return b ? (b.name || b.id) : id;
};

function versionDate(v) {
  const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})$/.exec(v || '');
  if (!m) return null;
  return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]);
}

function stamp(d = new Date()) {
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}

export default {
  title: 'Backup',
  render(root, ctx) {
    const statusRes = resource('/api/backup/status');
    let backupStatus = null;
    let mode = '';
    const regions = {};
    const main = h('div.page-stack', h('div.card', loading('Checking backup status…')));
    mount(root, pageHeader('Backup', 'Daily backups of your brains to your own Google Drive, with older versions kept for a while.'), main);

    // The schedule exists only while Drive is connected; its edits go when
    // the connection does, so leaving the page asks only about edits that
    // are on screen, and a reconnect shows the schedule again.
    const schedule = liveForm({ build: buildSchedule, onChange: ({ dirty }) => { const f = schedule.form(); if (f) f.bar.hidden = !dirty; } });
    ctx.dirty = () => schedule.dirty();

    // Polls faster while a backup, a restore or the Drive sign-in runs.
    const busy = () => !!(backupStatus && (backupStatus.job || (backupStatus.connect && backupStatus.connect.state === 'running')));
    ctx.follow(statusRes, (s) => {
      backupStatus = s;
      const nextMode = !s.rclone || !s.rclone.found ? 'norclone' : s.connected ? 'connected' : 'connect';
      if (nextMode !== mode) {
        if (mode === 'connected') schedule.discard();
        mode = nextMode;
        layout();
      }
      paintMode();
    }, () => (busy() ? 2000 : 8000));

    // Resolves once the page shows what the server has now.
    const refresh = () => statusRes.refresh();

    // A new layout has new, empty regions, which paint() fills.
    function layout() {
      for (const k of Object.keys(regions)) delete regions[k];
      if (mode === 'norclone') {
        regions.rclone = h('div');
        mount(main, regions.rclone);
      } else if (mode === 'connect') {
        regions.connect = h('div');
        mount(main, regions.connect);
      } else {
        regions.status = h('div');
        regions.job = h('div');
        regions.brains = h('div');
        regions.brainRows = h('tbody');
        regions.restore = restoreForm();
        mount(main,
          regions.status,
          regions.job,
          h('section.section', h('h2.section-title', 'What gets backed up'), regions.brains),
          h('section.section', h('h2.section-title', 'Schedule'), schedule.el),
          h('section.section', h('h2.section-title', 'Restore'), regions.restore));
      }
    }

    function paintMode() {
      const st = backupStatus;
      if (mode === 'norclone') paint(regions.rclone, st.rclone || {}, renderRclone);
      else if (mode === 'connect') paint(regions.connect, st.connect || null, renderConnect);
      else {
        paint(regions.status, {
          settings: st.settings || {}, next: st.next_run || '', rclone: st.rclone, running: !!st.job,
          brains: (st.brains || []).map((b) => ({ enabled: b.enabled, last: b.last || null })), bridge: st.bridge || null,
        }, renderStatus);
        paint(regions.job, st.job ? { job: st.job, names: (st.brains || []).map((b) => [b.id, b.name]) } : null, renderJob);
        schedule.set({ settings: st.settings || {}, inc: st.include_bridge_config });
        paintBrains();
        regions.restore.setBrains(st.brains || []);
      }
    }

    // ── rclone missing ──
    function renderRclone(rc) {
      if (rc.error) {
        // backup.rclone_path names an rclone that cannot run; the bridge
        // uses no other one instead.
        return h('div.card.hero',
          h('div.hero-icon', icon('download')),
          h('div.hero-body',
            h('h2', 'The configured rclone does not work'),
            callout('bad', null, rc.error),
            h('p', 'Fix ', mono('backup.rclone_path'), ' in config.json, or remove it to use the rclone on this computer’s PATH.'),
            h('div.actions', btn('Check again', { kind: 'primary', icon: 'refresh', onClick: () => refresh() }))));
      }
      const hint = rc.install_hint || 'See https://rclone.org/install/';
      const isCmd = !/^see /i.test(hint);
      return h('div.card.hero',
        h('div.hero-icon', icon('download')),
        h('div.hero-body',
          h('h2', 'Install rclone first'),
          h('p', 'Backups use rclone, a free open-source tool that copies files to Google Drive. Install it on the computer running the bridge:'),
          isCmd ? h('div.code-line', h('code.mono', hint), copyButton(hint)) : h('p', mono(hint)),
          h('div.actions', btn('Check again', { kind: 'primary', icon: 'refresh', onClick: () => refresh() }))));
    }

    // ── connecting ──
    function renderConnect(c) {
      const privacy = h('ul.facts',
        h('li', icon('shield'), h('span', 'The bridge asks only for access to files it creates (Google’s “drive.file” permission). It cannot see anything else in your Drive.')),
        h('li', icon('lock'), h('span', 'Your WhatsApp session, tokens and .env files are never uploaded.')),
        h('li', icon('backup'), h('span', 'Each brain folder is copied to a folder in your Drive. Older versions of changed files are kept for 30 days by default.')));
      if (c && c.state === 'running') {
        return h('div.card.wizard-card.waiting',
          spinner('large'),
          h('h2', 'Approve access in the browser tab that just opened'),
          h('p.muted', 'Sign in to Google and allow access. This page notices by itself when you are done.'),
          c.auth_url ? h('p', 'If no tab opened, ', h('a', { href: c.auth_url, target: '_blank', rel: 'noopener noreferrer' }, 'open this link', icon('external')), ' on the computer running the bridge.') : h('p.muted.small', 'Starting rclone…'),
          privacy);
      }
      return h('div.card.hero',
        h('div.hero-icon', icon('backup')),
        h('div.hero-body',
          h('h2', 'Connect Google Drive'),
          h('p', 'Back up every brain folder — prompts, memories, history and files — once a day.'),
          c && c.state === 'error' ? callout('bad', 'Connecting failed', c.error || 'Unknown error') : null,
          privacy,
          h('div.actions', btn(c && c.state === 'error' ? 'Try again' : 'Connect Google Drive', {
            kind: 'primary', icon: 'external',
            onClick: async () => {
              await post('/api/backup/connect');
              await refresh();
            },
          }))));
    }

    // ── connected: status ──
    function renderStatus({ settings: s, next, rclone, running, brains, bridge }) {
      const anyEnabled = brains.some((b) => b.enabled);
      const results = [...brains.map((b) => b.last), bridge].filter(Boolean);
      const lastOK = results.filter((r) => r.ok).sort((a, b) => new Date(b.finished) - new Date(a.finished))[0];
      return h('div.card.backup-status',
        h('div.card-head',
          h('div.card-title', icon('backup'), h('h2', 'Google Drive')),
          pill('Connected', 'ok')),
        h('div.card-body',
          h('p', 'Backups go to the folder ', h('strong', s.root || 'fridolin-backup'), ' in your Google Drive.'),
          h('dl.kv',
            h('dt', 'Schedule'), h('dd', s.enabled ? `Every day at ${s.daily_at || '03:30'}` : 'Off — only when you click “Back up now”'),
            next ? [h('dt', 'Next run'), h('dd', timeAgo(next))] : null,
            h('dt', 'Last good backup'), h('dd', lastOK ? timeAgo(lastOK.finished) : h('span.muted', 'none yet')),
            h('dt', 'rclone'), h('dd', h('span.muted.small', rclone.version || rclone.path || 'found'))),
          !anyEnabled && brains.length ? callout('warn', null, 'No brain has backups turned on. Turn them on below.') : null,
          h('div.actions',
            btn('Back up now', {
              kind: 'primary', icon: 'backup', disabled: running,
              onClick: async () => { await post('/api/backup/run', {}); toast('Backup started', 'ok'); await refresh(); },
            }),
            btn('Disconnect…', {
              kind: 'ghost', icon: 'x',
              onClick: async () => {
                const ok = await confirmDialog({
                  title: 'Disconnect Google Drive?',
                  message: 'Backups stop until you connect again. Everything already in your Drive stays there.',
                  confirm: 'Disconnect', danger: true,
                });
                if (!ok) return;
                await post('/api/backup/disconnect');
                toast('Google Drive disconnected', 'ok');
                await refresh();
              },
            }))));
    }

    // ── job progress ──
    function renderJob(shown) {
      if (!shown) return null;
      const { job: j } = shown;
      const brains = shown.names.map(([id, name]) => ({ id, name }));
      const done = j.done || [];
      const queue = j.queue || [];
      const total = done.length + queue.length + (j.current ? 1 : 0);
      const pct = total ? Math.round((done.length / total) * 100) : 0;
      const bar = h('progress.progress', { max: 100, value: pct, 'aria-label': 'Backup progress', 'aria-valuetext': `${done.length} of ${total} done` });
      const restoring = j.current && j.current.startsWith('restore:');
      return h('div.card.job-card',
        h('div.card-head',
          h('div.card-title', spinner(), h('h2', restoring ? jobLabel(j.current) : 'Backing up…')),
          btn('Cancel', {
            small: true, icon: 'stop',
            onClick: async () => { await post('/api/backup/cancel'); toast('Cancelling…', 'info'); await refresh(); },
          })),
        restoring ? h('p.muted', 'Copying files from Google Drive. Large folders can take a while.') : [
          bar,
          h('div.job-items',
            done.map((id) => h('span.job-item.done', icon('check'), jobLabel(id, brains))),
            j.current ? h('span.job-item.current', spinner(), jobLabel(j.current, brains)) : null,
            queue.map((id) => h('span.job-item.queued', icon('clock'), jobLabel(id, brains)))),
        ],
        h('p.muted.small', timeAgo(j.started, 'Started ')));
    }

    // ── schedule ──
    function buildSchedule({ settings: s, inc }) {
      const enabled = toggle({ checked: !!s.enabled, label: 'Back up every day' });
      const time = h('input', { type: 'time', id: nextId('t'), value: s.daily_at || '03:30', step: 60, required: true });
      const rootIn = textInput({ value: s.root || '', placeholder: 'fridolin-backup', class: 'mono' });
      const keep = textInput({ value: String(s.keep_versions_days ?? 30), inputmode: 'numeric', class: 'num' });
      const incSw = toggle({ checked: inc !== false, label: 'Also back up the bridge’s settings' });
      const saveBtn = btn('Save schedule', { kind: 'primary', type: 'submit', icon: 'check' });
      const bar = h('div.actions', { hidden: true }, btn('Discard', { onClick: () => schedule.discard() }), saveBtn);
      const value = () => ({
        enabled: enabled.input.checked,
        daily_at: time.value,
        root: rootIn.value.trim(),
        keep_versions_days: keep.value.trim(),
        include_bridge_config: incSw.input.checked,
      });
      const form = h('form.card.form',
        h('div.field', enabled, h('div.help', 'Runs at the time below while the computer is awake. A run missed while it slept happens as soon as it wakes. Switched on, or moved to a time already past today, it first runs tomorrow.')),
        h('div.form-grid',
          field({ label: 'Time of day', control: time, help: '24-hour time on this computer.' }),
          field({ label: 'Keep old versions for (days)', control: keep, help: 'Counted from the backup that replaced or deleted the files. 0 keeps them forever.' }),
          field({ label: 'Folder in Google Drive', control: rootIn, help: 'A plain folder name. Changing it starts a fresh backup in the new folder.' })),
        h('div.field', incSw, h('div.help', 'config.json and the approved brain settings — enough to set up the bridge again on a new computer. Never the WhatsApp session or tokens.')),
        bar);
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        const v = value();
        if (!/^\d+$/.test(v.keep_versions_days)) { toast('Days must be a whole number (0 = keep forever)'); keep.focus(); return; }
        if (!/^\d{2}:\d{2}$/.test(v.daily_at)) { toast('Pick a time of day'); time.focus(); return; }
        if (/[:\\]|^\/|\.\./.test(v.root)) { toast('Use a plain folder name for the Drive folder'); rootIn.focus(); return; }
        runBusy(saveBtn, async () => {
          await put('/api/backup/settings', { ...v, keep_versions_days: Number(v.keep_versions_days) });
          schedule.saved();
          toast('Backup schedule saved', 'ok');
          await refresh();
        });
      });
      return { el: form, value, bar };
    }

    // ── per-brain ──
    function paintBrains() {
      const st = backupStatus;
      const brains = st.brains || [];
      if (!brains.length) {
        paint(regions.brains, 'none', () => h('div.card', empty('No brains yet', 'Add a brain first; its folder can then be backed up.', btn('Add a brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' }))));
        return;
      }
      paint(regions.brains, 'table', () => h('div.card.flush.table-wrap', h('table.table.backup-table',
        h('thead', h('tr', h('th', 'Brain'), h('th', 'Backup'), h('th', 'Last result'), h('th.right', ''))),
        regions.brainRows)));
      const running = !!st.job;
      const rows = [
        ...brains.map((b) => ({ brain: b, running })),
        { bridge: st.bridge || null, inc: st.include_bridge_config !== false },
      ];
      reconcile(regions.brainRows, rows, (r) => (r.brain ? `brain:${r.brain.id}` : 'bridge'), (r) => (r.brain ? brainRow(r) : bridgeRow(r)));
    }

    function brainRow({ brain: b, running }) {
      const sw = toggle({
        checked: b.enabled,
        label: b.enabled ? 'On' : 'Off',
        onChange: async (v, input) => {
          input.disabled = true;
          try {
            await put('/api/backup/settings', { brain: b.id, brain_enabled: v });
            toast(`${b.name || b.id}: backups ${v ? 'on' : 'off'}`, 'ok');
            await refresh();
          } catch (e) {
            input.checked = !v;
            toast(e.message);
          } finally {
            input.disabled = false;
          }
        },
      });
      return h('tr',
        h('td', h('div', h('strong', b.name || b.id)), h('div.small', mono(b.dir))),
        h('td', sw),
        h('td', resultCell(b.last, b, running)),
        h('td.right.nowrap',
          btn(`Excludes${b.exclude && b.exclude.length ? ` (${b.exclude.length})` : ''}`, { small: true, kind: 'ghost', icon: 'edit', onClick: () => editExcludes(b) }),
          btn('Back up', {
            small: true, icon: 'backup', disabled: running, title: 'Back up only this brain now',
            onClick: async () => { await post('/api/backup/run', { brains: [b.id], include_bridge: false }); toast(`Backing up ${b.name || b.id}`, 'ok'); await refresh(); },
          })));
    }

    function bridgeRow({ bridge, inc }) {
      return h('tr.muted-row',
        h('td', h('div', h('strong', 'Bridge settings')), h('div.small.muted', 'config.json and approved brain.json files')),
        h('td', inc ? pill('On', 'ok') : pill('Off', 'neutral')),
        h('td', resultCell(bridge)),
        h('td'));
    }

    function resultCell(r, b, running) {
      if (!r) return h('span.muted', 'Not backed up yet');
      let state;
      if (r.ok) state = pill('OK', 'ok');
      else if (r.error === 'cancelled') state = pill('Cancelled', 'warn');
      else if (r.refused) state = pill('Not mirrored', 'warn');
      else state = pill('Failed', 'bad');
      return h('div.result',
        h('div', state, ' ', timeAgo(r.finished)),
        h('div.muted.small', r.ok ? `${fmtBytes(r.bytes)} in ${plural(r.files, 'file')} · ${fmtDuration(r.seconds)}${r.trigger === 'scheduled' ? ' · scheduled' : ''}` : fmtDuration(r.seconds)),
        r.error && r.error !== 'cancelled' ? h('div.error-text.small', r.error) : null,
        r.refused && b ? h('div', btn('Mirror the deletion…', {
          small: true, icon: 'backup', disabled: running, title: 'Back up this brain including the deletion',
          onClick: () => mirrorDeletion(b),
        })) : null);
    }

    // A run refused as a mass deletion is only mirrored when the owner says
    // so: it removes the missing files from the latest copy in Drive.
    async function mirrorDeletion(b) {
      const name = b.name || b.id;
      const days = (backupStatus.settings || {}).keep_versions_days ?? 30;
      const ok = await confirmDialog({
        title: `Mirror the deletion in ${name}?`,
        message: h('div',
          h('p', 'Most of the files backed up from ', mono(b.dir), ' are gone from that folder. Check it first: a brain, a disk that is not mounted or a moved folder can empty it by accident.'),
          h('p', 'Mirroring removes the missing files from the latest copy in Google Drive. ',
            days > 0 ? `Their previous copies stay in the older versions for ${plural(days, 'day')}.` : 'Their previous copies stay in the older versions.')),
        confirm: 'Mirror the deletion', danger: true,
      });
      if (!ok) return;
      await post('/api/backup/run', { brains: [b.id], include_bridge: false, force: true });
      toast(`Backing up ${name}, deletions included`, 'ok');
      await refresh();
    }

    async function editExcludes(b) {
      const ta = linesArea(b.exclude || [], { rows: 8, placeholder: 'node_modules/\n*.log' });
      const saved = await openDialog({
        title: `Leave out of ${b.name || b.id}’s backup`,
        body: h('div',
          h('p.muted', 'One pattern per line, relative to the brain folder. A trailing / means a folder; * matches anything in a name.'),
          field({ label: 'Patterns', control: ta }),
          h('p.muted.small', 'Examples: ', mono('node_modules/'), ' ', mono('*.log'), ' ', mono('/big-exports/'))),
        actions: [
          { label: 'Cancel', value: null },
          { label: 'Save', kind: 'primary', onClick: async () => { await put('/api/backup/settings', { brain: b.id, brain_exclude: linesOf(ta) }); return true; } },
        ],
      });
      if (saved) { toast('Saved', 'ok'); await refresh(); }
    }

    // ── restore ──
    // Built once per connected layout, so a brain chosen and a folder typed
    // stay while the status refreshes; setBrains() only updates the choice.
    function restoreForm() {
      let versions = null;
      let version = 'current';
      const brainSel = select([['', 'Choose a brain…'], ['__other', 'Another id…']], '');
      const otherIn = textInput({ placeholder: 'brain id', class: 'mono', hidden: true, 'aria-label': 'Brain id' });
      const versionsBox = h('div.versions', h('p.muted.small', 'Choose a brain to see its backups.'));
      const targetIn = textInput({ placeholder: '~/restored/brain-name', class: 'mono' });
      const browse = btn('Browse…', { icon: 'folder', onClick: () => pickTarget() });
      const go = btn('Restore', { kind: 'primary', icon: 'download', type: 'submit' });
      const currentId = () => (brainSel.value === '__other' ? otherIn.value.trim() : brainSel.value);
      const loadVersions = async () => {
        const id = currentId();
        versions = null;
        version = 'current';
        if (!id) { mount(versionsBox, h('p.muted.small', 'Choose a brain to see its backups.')); return; }
        if (!targetIn.dataset.touched) targetIn.value = `~/restored/${id}-${stamp()}`;
        mount(versionsBox, h('div.muted', loading('Asking Google Drive for the versions…')));
        try {
          // rclone may take up to a minute to list them.
          const r = await get(`/api/backup/versions${qs({ brain: id })}`, { signal: ctx.signal, timeout: 90000 });
          if (currentId() !== id) return;
          versions = r.versions || [];
          paintVersions();
        } catch (e) {
          if (ctx.dead) return;
          mount(versionsBox, callout('bad', 'Could not list the versions', e.message));
        }
      };
      const paintVersions = () => {
        const name = nextId('ver');
        const opt = (value, title, sub) => {
          const id = nextId('v');
          const r = h('input', { type: 'radio', name, id, value, checked: value === version });
          r.addEventListener('change', () => { version = value; });
          return h('label.version-opt', { for: id }, r, h('span', h('strong', title), sub ? h('span.muted.small', sub) : null));
        };
        mount(versionsBox,
          opt('current', 'Latest full copy', 'The whole folder as of the last backup.'),
          versions.length ? h('details.details', h('summary', `Older versions (${versions.length})`),
            h('p.muted.small', 'A version holds only the files that backup run changed or deleted — not a full copy. Restore the latest copy first, then pick files from a version if you need an older state.'),
            h('div.version-list', versions.map((v) => {
              const d = versionDate(v);
              return opt(v, d ? absTime(d) : v, d ? 'files replaced or deleted in that run' : '');
            }))) : h('p.muted.small', 'No older versions yet.'));
      };
      brainSel.addEventListener('change', () => { otherIn.hidden = brainSel.value !== '__other'; if (!otherIn.hidden) otherIn.focus(); else loadVersions(); });
      otherIn.addEventListener('input', ctx.debounce(loadVersions, 500));
      targetIn.addEventListener('input', () => { targetIn.dataset.touched = '1'; });

      async function pickTarget() {
        const id = currentId() || 'brain';
        let pending = null;
        const fb = folderBrowser({
          pickLabel: 'Restore into a new folder here',
          newLabel: 'Create and use',
          onPick: (p, { create }) => pending.close(create ? p : `${p}/${id}-restored-${stamp()}`),
          signal: ctx.signal,
        });
        pending = openDialog({ title: 'Where should the copy go?', body: h('div', h('p.muted', 'Pick a folder; the backup is copied into a new folder inside it. Or type a name for a new folder.'), fb), wide: true, actions: [{ label: 'Cancel', value: null }] });
        const picked = await pending;
        if (picked) { targetIn.value = picked; targetIn.dataset.touched = '1'; }
      }

      const form = h('form.card.form',
        h('p', 'Restoring copies a backup into a ', h('strong', 'new, empty folder'), ' inside your home folder. It never overwrites an existing brain: look through the copy, then add it as a brain or swap folders yourself.'),
        h('div.form-grid',
          field({ label: 'Brain', control: h('div.stack', brainSel, otherIn) }),
          field({ label: 'Restore into', control: h('div.inline-add', targetIn, browse), help: 'Must be new or empty.' })),
        field({ label: 'Version', control: versionsBox, wide: true }),
        h('div.actions', go));
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        const id = currentId();
        const target = targetIn.value.trim();
        if (!id) { toast('Choose a brain to restore'); return; }
        if (!target) { toast('Choose a folder to restore into'); targetIn.focus(); return; }
        runBusy(go, async () => {
          const ok = await confirmDialog({
            title: 'Start the restore?',
            message: h('div', h('p', 'Copy ', h('strong', version === 'current' ? 'the latest full copy' : `version ${version}`), ' of ', mono(id), ' into ', mono(target), '?'), h('p.muted', 'It runs in the background. Watch progress here or on the Logs page.')),
            confirm: 'Restore',
          });
          if (!ok) return;
          const r = await post('/api/backup/restore', { brain: id, version, target });
          toast(`Restoring into ${r && r.target ? r.target : target}`, 'ok');
          await refresh();
        });
      });
      form.setBrains = (brains) => {
        syncOptions(brainSel, [['', 'Choose a brain…'], ...brains.map((b) => [b.id, brainLabel(b)]), ['__other', 'Another id…']]);
      };
      return form;
    }
  },
};
