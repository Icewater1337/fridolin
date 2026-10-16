// Backup: Google Drive via rclone — connect, schedule, per-brain settings,
// run now, progress, and restore into a new folder.

import { get, post, put, qs } from './api.js';
import { folderBrowser } from './folder-browser.js';
import {
  h, mount, icon, pill, badge, btn, callout, timeAgo, fmtDuration, fmtBytes, mono, plural, field,
  textInput, toggle, select, linesArea, linesOf, empty, changed, pageHeader, spinner, openDialog,
  confirmDialog, toast, copyButton, runBusy, nextId, absTime, loading,
} from './ui.js';

const label = (id, brains) => {
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
    const memo = {};
    let st = null;
    let mode = '';
    let scheduleDirty = false;
    const regions = {};
    const main = h('div.page-stack', h('div.card', loading('Checking backup status…')));
    mount(root, pageHeader('Backup', 'Daily backups of your brains to your own Google Drive, with older versions kept for a while.'), main);

    ctx.dirty = () => scheduleDirty;

    const poller = ctx.poll(async () => {
      const s = await get('/api/backup/status');
      st = s;
      const m = !s.rclone || !s.rclone.found ? 'norclone' : s.connected ? 'connected' : 'connect';
      if (m !== mode) { mode = m; Object.keys(memo).forEach((k) => delete memo[k]); layout(); }
      paint();
    }, () => (st && (st.job || (st.connect && st.connect.state === 'running')) ? 2000 : 8000));

    const refresh = () => { poller.kick(); };

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
        regions.schedule = h('div');
        regions.brains = h('div');
        regions.restore = h('div');
        mount(main,
          regions.status,
          regions.job,
          h('section.section', h('h2.section-title', 'What gets backed up'), regions.brains),
          h('section.section', h('h2.section-title', 'Schedule'), regions.schedule),
          h('section.section', h('h2.section-title', 'Restore'), regions.restore));
      }
    }

    function paint() {
      if (mode === 'norclone') paintRclone();
      else if (mode === 'connect') paintConnect();
      else {
        paintStatus();
        paintJob();
        paintSchedule();
        paintBrains();
        paintRestore();
      }
    }

    // ── rclone missing ──
    function paintRclone() {
      const rc = st.rclone || {};
      if (!changed(memo, 'rc', rc)) return;
      const hint = rc.install_hint || 'See https://rclone.org/install/';
      const isCmd = !/^see /i.test(hint);
      mount(regions.rclone, h('div.card.hero',
        h('div.hero-icon', icon('download')),
        h('div.hero-body',
          h('h2', 'Install rclone first'),
          h('p', 'Backups use rclone, a free open-source tool that copies files to Google Drive. Install it on the computer running the bridge:'),
          isCmd ? h('div.code-line', h('code.mono', hint), copyButton(hint)) : h('p', mono(hint)),
          h('div.actions', btn('Check again', { kind: 'primary', icon: 'refresh', onClick: async () => { memo.rc = ''; await poller.kick(); } })))));
    }

    // ── connecting ──
    function paintConnect() {
      const c = st.connect;
      if (!changed(memo, 'connect', c)) return;
      const privacy = h('ul.facts',
        h('li', icon('shield'), h('span', 'The bridge asks only for access to files it creates (Google’s “drive.file” permission). It cannot see anything else in your Drive.')),
        h('li', icon('lock'), h('span', 'Your WhatsApp session, tokens and .env files are never uploaded.')),
        h('li', icon('backup'), h('span', 'Each brain folder is copied to a folder in your Drive. Older versions of changed files are kept for 30 days by default.')));
      if (c && c.state === 'running') {
        mount(regions.connect, h('div.card.wizard-card.waiting',
          spinner('large'),
          h('h2', 'Approve access in the browser tab that just opened'),
          h('p.muted', 'Sign in to Google and allow access. This page notices by itself when you are done.'),
          c.auth_url ? h('p', 'If no tab opened, ', h('a', { href: c.auth_url, target: '_blank', rel: 'noopener noreferrer' }, 'open this link', icon('external')), ' on the computer running the bridge.') : h('p.muted.small', 'Starting rclone…'),
          privacy));
        return;
      }
      mount(regions.connect, h('div.card.hero',
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
              memo.connect = '';
              refresh();
            },
          })))));
    }

    // ── connected: status ──
    function paintStatus() {
      const key = { s: st.settings, next: st.next_run, rc: st.rclone, running: !!st.job, brains: (st.brains || []).map((b) => [b.id, b.enabled]), bridge: st.bridge };
      if (!changed(memo, 'status', key)) return;
      const s = st.settings || {};
      const anyEnabled = (st.brains || []).some((b) => b.enabled);
      const results = [...(st.brains || []).map((b) => b.last), st.bridge].filter(Boolean);
      const lastOK = results.filter((r) => r.ok).sort((a, b) => new Date(b.finished) - new Date(a.finished))[0];
      mount(regions.status, h('div.card.backup-status',
        h('div.card-head',
          h('div.card-title', icon('backup'), h('h2', 'Google Drive')),
          pill('Connected', 'ok')),
        h('div.card-body',
          h('p', 'Backups go to the folder ', h('strong', s.root || 'fridolin-backup'), ' in your Google Drive.'),
          h('dl.kv',
            h('dt', 'Schedule'), h('dd', s.enabled ? `Every day at ${s.daily_at || '03:30'}` : 'Off — only when you click “Back up now”'),
            st.next_run ? [h('dt', 'Next run'), h('dd', timeAgo(st.next_run))] : null,
            h('dt', 'Last good backup'), h('dd', lastOK ? timeAgo(lastOK.finished) : h('span.muted', 'none yet')),
            h('dt', 'rclone'), h('dd', h('span.muted.small', st.rclone.version || st.rclone.path || 'found'))),
          !anyEnabled && (st.brains || []).length ? callout('warn', null, 'No brain has backups turned on. Turn them on below.') : null,
          h('div.actions',
            btn('Back up now', {
              kind: 'primary', icon: 'backup', disabled: !!st.job,
              onClick: async () => { await post('/api/backup/run', {}); toast('Backup started', 'ok'); memo.status = ''; refresh(); },
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
                refresh();
              },
            })))));
    }

    // ── job progress ──
    function paintJob() {
      const j = st.job;
      if (!changed(memo, 'job', j)) return;
      if (!j) { mount(regions.job); return; }
      const done = j.done || [];
      const queue = j.queue || [];
      const total = done.length + queue.length + (j.current ? 1 : 0);
      const pct = total ? Math.round((done.length / total) * 100) : 0;
      const bar = h('div.progress', h('div.progress-bar'));
      bar.firstChild.style.width = `${Math.max(4, pct)}%`;
      const restoring = j.current && j.current.startsWith('restore:');
      mount(regions.job, h('div.card.job-card',
        h('div.card-head',
          h('div.card-title', spinner(), h('h2', restoring ? label(j.current) : 'Backing up…')),
          btn('Cancel', {
            small: true, icon: 'stop',
            onClick: async () => { await post('/api/backup/cancel'); toast('Cancelling…', 'info'); refresh(); },
          })),
        restoring ? h('p.muted', 'Copying files from Google Drive. Large folders can take a while.') : [
          bar,
          h('div.job-items',
            done.map((id) => h('span.job-item.done', icon('check'), label(id, st.brains))),
            j.current ? h('span.job-item.current', spinner(), label(j.current, st.brains)) : null,
            queue.map((id) => h('span.job-item.queued', icon('clock'), label(id, st.brains)))),
        ],
        h('p.muted.small', timeAgo(j.started, 'Started '))));
    }

    // ── schedule ──
    function paintSchedule() {
      const s = st.settings || {};
      const inc = st.include_bridge_config;
      const key = { s, inc };
      if (!changed(memo, 'schedule', key) && regions.schedule.firstChild) return;
      if (scheduleDirty) return;
      const enabled = toggle({ checked: !!s.enabled, label: 'Back up every day' });
      const time = h('input', { type: 'time', id: nextId('t'), value: s.daily_at || '03:30', step: 60, required: true });
      const rootIn = textInput({ value: s.root || '', placeholder: 'fridolin-backup', class: 'mono' });
      const keep = textInput({ value: String(s.keep_versions_days ?? 30), inputmode: 'numeric', class: 'num' });
      const incSw = toggle({ checked: inc !== false, label: 'Also back up the bridge’s settings' });
      const saveBtn = btn('Save schedule', { kind: 'primary', type: 'submit', icon: 'check' });
      const bar = h('div.actions', { hidden: true }, btn('Discard', { onClick: () => { scheduleDirty = false; memo.schedule = ''; paintSchedule(); } }), saveBtn);
      const form = h('form.card.form',
        h('div.field', enabled, h('div.help', 'Runs at the time below while the computer is awake. A run missed while it slept happens as soon as it wakes.')),
        h('div.form-grid',
          field({ label: 'Time of day', control: time, help: '24-hour time on this computer.' }),
          field({ label: 'Keep old versions for (days)', control: keep, help: 'Counted from the backup that replaced or deleted the files. 0 keeps them forever.' }),
          field({ label: 'Folder in Google Drive', control: rootIn, help: 'A plain folder name. Changing it starts a fresh backup in the new folder.' })),
        h('div.field', incSw, h('div.help', 'config.json and the approved brain settings — enough to set up the bridge again on a new computer. Never the WhatsApp session or tokens.')),
        bar);
      const dirty = () => { scheduleDirty = true; bar.hidden = false; };
      form.addEventListener('input', dirty);
      form.addEventListener('change', dirty);
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        const days = keep.value.trim();
        if (!/^\d+$/.test(days)) { toast('Days must be a whole number (0 = keep forever)'); keep.focus(); return; }
        if (!/^\d{2}:\d{2}$/.test(time.value)) { toast('Pick a time of day'); time.focus(); return; }
        if (/[:\\]|^\/|\.\./.test(rootIn.value)) { toast('Use a plain folder name for the Drive folder'); rootIn.focus(); return; }
        runBusy(saveBtn, async () => {
          await put('/api/backup/settings', {
            enabled: enabled.input.checked,
            daily_at: time.value,
            root: rootIn.value.trim(),
            keep_versions_days: Number(days),
            include_bridge_config: incSw.input.checked,
          });
          scheduleDirty = false;
          memo.schedule = '';
          memo.status = '';
          toast('Backup schedule saved', 'ok');
          refresh();
        });
      });
      mount(regions.schedule, form);
    }

    // ── per-brain ──
    function paintBrains() {
      const brains = st.brains || [];
      if (!changed(memo, 'brains', { brains, bridge: st.bridge, job: !!st.job, inc: st.include_bridge_config })) return;
      if (!brains.length) {
        mount(regions.brains, h('div.card', empty('No brains yet', 'Add a brain first; its folder can then be backed up.', btn('Add a brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' }))));
        return;
      }
      const rows = brains.map((b) => {
        const sw = toggle({
          checked: b.enabled,
          label: b.enabled ? 'On' : 'Off',
          onChange: async (v, input) => {
            input.disabled = true;
            try {
              await put('/api/backup/settings', { brain: b.id, brain_enabled: v });
              toast(`${b.name || b.id}: backups ${v ? 'on' : 'off'}`, 'ok');
              memo.brains = '';
              refresh();
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
          h('td', resultCell(b.last, b)),
          h('td.right.nowrap',
            btn(`Excludes${b.exclude && b.exclude.length ? ` (${b.exclude.length})` : ''}`, { small: true, kind: 'ghost', icon: 'edit', onClick: () => editExcludes(b) }),
            btn('Back up', {
              small: true, icon: 'backup', disabled: !!st.job, title: 'Back up only this brain now',
              onClick: async () => { await post('/api/backup/run', { brains: [b.id], include_bridge: false }); toast(`Backing up ${b.name || b.id}`, 'ok'); refresh(); },
            })));
      });
      rows.push(h('tr.muted-row',
        h('td', h('div', h('strong', 'Bridge settings')), h('div.small.muted', 'config.json and approved brain.json files')),
        h('td', st.include_bridge_config === false ? pill('Off', 'neutral') : pill('On', 'ok')),
        h('td', resultCell(st.bridge)),
        h('td')));
      mount(regions.brains, h('div.card.flush.table-wrap', h('table.table.backup-table',
        h('thead', h('tr', h('th', 'Brain'), h('th', 'Backup'), h('th', 'Last result'), h('th.right', ''))),
        h('tbody', rows))));
    }

    function resultCell(r, b) {
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
          small: true, icon: 'backup', disabled: !!st.job, title: 'Back up this brain including the deletion',
          onClick: () => mirrorDeletion(b),
        })) : null);
    }

    // A run refused as a mass deletion is only mirrored when the owner says
    // so: it removes the missing files from the latest copy in Drive.
    async function mirrorDeletion(b) {
      const name = b.name || b.id;
      const days = (st.settings || {}).keep_versions_days ?? 30;
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
      memo.brains = '';
      refresh();
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
      if (saved) { toast('Saved', 'ok'); memo.brains = ''; refresh(); }
    }

    // ── restore ──
    function paintRestore() {
      const ids = (st.brains || []).map((b) => b.id);
      if (regions.restore.firstChild && !changed(memo, 'restoreIds', ids)) return;
      memo.restoreIds = JSON.stringify(ids);
      let versions = null;
      let version = 'current';
      const brainSel = select([['', 'Choose a brain…'], ...(st.brains || []).map((b) => [b.id, b.name && b.name !== b.id ? `${b.name} (${b.id})` : b.id]), ['__other', 'Another id…']], '');
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
          const r = await get(`/api/backup/versions${qs({ brain: id })}`);
          if (currentId() !== id) return;
          versions = r.versions || [];
          paintVersions();
        } catch (e) {
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
      let t;
      otherIn.addEventListener('input', () => { clearTimeout(t); t = setTimeout(loadVersions, 500); });
      targetIn.addEventListener('input', () => { targetIn.dataset.touched = '1'; });

      async function pickTarget() {
        const id = currentId() || 'brain';
        let pending = null;
        const fb = folderBrowser({
          pickLabel: 'Restore into a new folder here',
          newLabel: 'Create and use',
          onPick: (p, { create }) => pending.close(create ? p : `${p}/${id}-restored-${stamp()}`),
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
          refresh();
        });
      });
      mount(regions.restore, form);
    }
  },
};
