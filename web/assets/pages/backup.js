// Backup: Google Drive via rclone — connect, schedule, per-brain settings,
// run now, progress, and restore into a new folder.

import { get, post, put, del } from '../app/api.js';
import { resource } from '../app/resources.js';
import { folderBrowser } from '../components/folder-browser.js';
import { liveForm } from '../components/live-form.js';
import { h, mount, paint, reconcile, syncOptions } from '../lib/dom.js';
import { fmtDuration, fmtBytes, plural, absTime, stamp } from '../lib/format.js';
import { brainLabel, backupOwner } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn, copyButton, runBusy } from '../components/buttons.js';
import { openDialog, confirmDialog } from '../components/dialog.js';
import { field, textInput, toggle, select, linesArea, linesOf, nextId } from '../components/fields.js';
import { pill, callout, timeAgo, mono, empty, pageHeader, spinner, loading } from '../components/common.js';
import { googleClientSetup } from '../components/calendar.js';
import { errorMessage } from '../lib/errors.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { BackupBrainStatus, BackupClientStatus, BackupConnectJob, BackupJob, BackupResult, BackupSettings, BackupStatus, RcloneStatus, RestoreResult } from '../../types/api.js' */

/**
 * What the status card shows, from the status.
 * @typedef {object} StatusView
 * @property {Partial<BackupSettings>} settings
 * @property {string} next
 * @property {RcloneStatus} rclone
 * @property {boolean} running
 * @property {boolean} shared
 * @property {string} folder
 * @property {BackupConnectJob | null} connect
 * @property {BackupClientStatus | null} client
 * @property {string} redirect
 * @property {{enabled: boolean, last: BackupResult | null}[]} brains
 * @property {BackupResult | null} bridge
 */

// A version folder is named after its run's time: in UTC with a Z, or in
// the bridge's local time for folders made before.
/** @param {string} v */
function versionDate(v) {
  const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})(Z?)$/.exec(v || '');
  if (!m) return null;
  /** @type {[number, number, number, number, number, number]} */
  const parts = [+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]];
  return m[7] ? new Date(Date.UTC(...parts)) : new Date(...parts);
}

export default /** @satisfies {PageModule} */ ({
  title: 'Backup',
  render(root, ctx) {
    const statusRes = resource('/api/backup/status');
    /** @type {BackupStatus | null} */
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
        regions.lastRestore = h('div');
        mount(main,
          regions.status,
          regions.job,
          h('section.section', h('h2.section-title', 'What gets backed up'), regions.brains),
          h('section.section', h('h2.section-title', 'Schedule'), schedule.el),
          h('section.section', h('h2.section-title', 'Restore'), regions.lastRestore, regions.restore));
      }
    }

    function paintMode() {
      const st = backupStatus;
      if (mode === 'norclone') paint(regions.rclone, st.rclone || {}, renderRclone);
      else if (mode === 'connect') paint(regions.connect, { connect: st.connect || null, client: st.client || null, redirect: st.redirect_uri || '' }, renderConnect);
      else {
        paint(regions.status, {
          settings: st.settings || {}, next: st.next_run || '', rclone: st.rclone, running: !!st.job,
          shared: !!st.shared_client, folder: st.drive_folder || '', connect: st.connect || null, client: st.client || null, redirect: st.redirect_uri || '',
          brains: (st.brains || []).map((b) => ({ enabled: b.enabled, last: b.last || null })), bridge: st.bridge || null,
        }, renderStatus);
        paint(regions.job, st.job ? { job: st.job, names: (st.brains || []).map((b) => [b.id, b.name]) } : null, renderJob);
        schedule.set({ settings: st.settings || {}, inc: st.include_bridge_config });
        paintBrains();
        paint(regions.lastRestore, st.job && st.job.kind === 'restore' ? null : (st.last_restore || null), renderLastRestore);
        regions.restore.setBrains(st.brains || []);
      }
    }

    // ── rclone missing ──
    /** @param {Partial<RcloneStatus>} rc */
    function renderRclone(rc) {
      if (rc.error) {
        // backup.rclone_path names an rclone that cannot run; the bridge
        // uses no other one instead.
        return h('div.card.hero',
          h('div.hero-icon', icon('download')),
          h('div.hero-body',
            h('h2', 'The configured rclone does not work'),
            callout('bad', null, rc.error),
            h('p', 'Fix ', mono('backup.rclone_path'), ' in config.json, or remove it to use the rclone in the helper folders of this computer’s PATH (see Helper programs in docs/CONFIGURATION.md).'),
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
    // The bridge signs in to Google itself, with your own OAuth client, and
    // Google sends the browser back to this page.
    /** @param {boolean} reconnect */
    async function startConnect(reconnect) {
      const r = await post('/api/backup/connect', reconnect ? { reconnect: true } : undefined);
      if (r && r.auth_url) window.location.assign(r.auth_url);
      else await refresh();
    }

    // Which Google client the sign-in uses, or the steps to add one.
    /**
     * @param {BackupClientStatus | null} client
     * @param {string} redirect
     */
    function clientBlock(client, redirect) {
      if (!client) {
        return h('div.stack',
          callout('info', 'Your own Google client first', 'Google Drive signs in with an OAuth client from your own Google Cloud project. Add it under ',
            h('a', { href: '#/settings/google' }, 'Settings → Google sign-in'), ' (brains use it for their calendars too), or paste one here for Drive alone. Enable the Google Drive API in its project.'),
          googleClientSetup({ redirectUri: redirect, onSaved: () => refresh(), compact: true, drive: true }));
      }
      const what = client.source === 'drive' ? 'the client added here for Drive' : 'your client under Settings → Google sign-in';
      const box = h('div');
      let pasting = false;
      return h('div.stack',
        h('p.small', 'Signs in with ', what, ' ', mono(client.client_id_hint || ''), client.type === 'web' ? ' (Web application)' : ' (Desktop app)', '. Its project needs the Google Drive API enabled.'),
        client.type === 'web' && redirect ? callout('warn', null, 'Make sure ', mono(redirect, { copy: true }), ' is one of its authorised redirect URIs, or sign-in fails with “redirect_uri_mismatch”.') : null,
        h('div.actions',
          client.source === 'drive'
            ? btn('Use the Settings client instead', { small: true, icon: 'x', onClick: async () => { await del('/api/backup/drive-client'); await refresh(); } })
            : btn('Use a separate client for Drive…', { small: true, icon: 'edit', onClick: () => { pasting = !pasting; mount(box, pasting ? googleClientSetup({ redirectUri: redirect, onSaved: () => refresh(), compact: true, drive: true }) : null); } })),
        box);
    }

    // A sign-in on its way: waiting for Google, or setting up the folder.
    /** @param {BackupConnectJob} c */
    function renderSignIn(c) {
      if (c.step === 'folder') {
        return h('div.stack', h('p', spinner(), ' Setting up the folder ', h('strong', 'Fridolin'), ' in your Google Drive…'));
      }
      return h('div.stack',
        h('p', spinner(), ' Waiting for you to approve access at Google. This page notices by itself when you are done.'),
        c.auth_url ? h('p', h('a', { href: c.auth_url }, 'Open Google’s sign-in', icon('external'))) : null);
    }

    /** @param {{connect: BackupConnectJob | null, client: BackupClientStatus | null, redirect: string}} shown */
    function renderConnect({ connect: c, client, redirect }) {
      const privacy = h('ul.facts',
        h('li', icon('shield'), h('span', 'The bridge asks only for access to files it creates (Google’s “drive.file” permission), with your own Google client. It cannot see anything else in your Drive.')),
        h('li', icon('lock'), h('span', 'Your WhatsApp session and the bridge’s tokens are never uploaded, nor .env files, private keys and tokens in brain folders.')),
        h('li', icon('backup'), h('span', 'Each brain folder is copied to a folder in your Drive, inside Fridolin/. Older versions of changed files are kept for 30 days by default.')));
      if (c && c.state === 'running') {
        return h('div.card.wizard-card.waiting',
          spinner('large'),
          h('h2', c.step === 'folder' ? 'Setting up Google Drive' : 'Approve access at Google'),
          renderSignIn(c),
          privacy);
      }
      return h('div.card.hero',
        h('div.hero-icon', icon('backup')),
        h('div.hero-body',
          h('h2', 'Connect Google Drive'),
          h('p', 'Back up every brain folder — prompts, memories, history and files — once a day.'),
          c && c.state === 'error' ? callout('bad', 'Connecting failed', c.error || 'Unknown error') : null,
          clientBlock(client, redirect),
          privacy,
          h('div.actions', btn(c && c.state === 'error' ? 'Try again' : 'Connect Google Drive', {
            kind: 'primary', icon: 'external', disabled: !client,
            onClick: () => startConnect(false),
          }))));
    }

    // ── connected: status ──
    // The link through rclone's shared client, which Google retires during
    // 2026: the switch to your own client, and how it goes.
    /** @param {Pick<StatusView, 'shared' | 'connect' | 'client' | 'redirect' | 'running'>} view */
    function renderSwitch({ shared, connect: c, client, redirect, running }) {
      const switching = c && c.reconnect;
      if (switching && c.state === 'running') return callout('info', 'Switching to your own Google client', renderSignIn(c));
      if (switching && c.state === 'done') return callout('ok', 'Switched to your own Google client', 'Backups and the vault’s weekly copy now go to the folder Fridolin in your Drive. The folders the old link made stay at the top of your Drive until you delete them there.');
      const failed = switching && c.state === 'error' ? callout('bad', 'Switching failed — the old link still works', c.error || 'Unknown error') : null;
      if (!shared) return failed;
      return h('div.stack',
        failed,
        callout('warn', 'This link uses rclone’s shared Google client',
          'Google retires it during 2026, and backups to Drive stop then. Switch to your own Google client: this link keeps backing up until the new one is in place. ',
          'The new link cannot see what the old one made, so backups and the vault’s weekly copy start afresh in a new folder, Fridolin, in your Drive; the old folders stay until you delete them there.'),
        clientBlock(client, redirect),
        h('div.actions', btn('Switch to your own Google client', {
          kind: 'primary', icon: 'external', disabled: !client || running,
          onClick: () => startConnect(true),
        })));
    }

    /** @param {StatusView} view */
    function renderStatus({ settings: s, next, rclone, running, brains, bridge, shared, folder, connect, client, redirect }) {
      const anyEnabled = brains.some((b) => b.enabled);
      const results = [...brains.map((b) => b.last), bridge].filter(Boolean);
      const lastOK = results.filter((r) => r.ok).sort((a, b) => new Date(b.finished).getTime() - new Date(a.finished).getTime())[0];
      return h('div.card.backup-status',
        h('div.card-head',
          h('div.card-title', icon('backup'), h('h2', 'Google Drive')),
          pill('Connected', 'ok')),
        h('div.card-body',
          h('p', 'Backups go to the folder ', h('strong', (folder ? `${folder}/` : '') + (s.root || 'fridolin-backup')), ' in your Google Drive.'),
          renderSwitch({ shared, connect, client, redirect, running }),
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
                  message: 'Backups stop until you connect again. Everything already in your Drive stays there. The bridge removes only its own link: your Google client keeps its access to your Google account until you remove it at myaccount.google.com/permissions.',
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
    /** @param {{job: BackupJob, names: string[][]} | null} shown */
    function renderJob(shown) {
      if (!shown) return null;
      const { job: j } = shown;
      const brains = shown.names.map(([id, name]) => ({ id, name }));
      const done = j.done || [];
      const queue = j.queue || [];
      const total = done.length + queue.length + (j.current ? 1 : 0);
      const pct = total ? Math.round((done.length / total) * 100) : 0;
      const bar = h('progress.progress', { max: 100, value: pct, 'aria-label': 'Backup progress', 'aria-valuetext': `${done.length} of ${total} done` });
      const restoring = j.kind === 'restore';
      return h('div.card.job-card',
        h('div.card-head',
          h('div.card-title', spinner(), h('h2', restoring ? `Restoring ${backupOwner(j.brain, brains)}` : 'Backing up…')),
          btn('Cancel', {
            small: true, icon: 'stop',
            onClick: async () => { await post('/api/backup/cancel'); toast('Cancelling…', 'info'); await refresh(); },
          })),
        restoring ? h('p.muted', 'Copying files from Google Drive into ', mono(j.target), '. Large folders can take a while.') : [
          bar,
          h('div.job-items',
            done.map((id) => h('span.job-item.done', icon('check'), backupOwner(id, brains))),
            j.current ? h('span.job-item.current', spinner(), backupOwner(j.current, brains)) : null,
            queue.map((id) => h('span.job-item.queued', icon('clock'), backupOwner(id, brains)))),
        ],
        h('p.muted.small', timeAgo(j.started, 'Started ')));
    }

    // ── schedule ──
    /** @param {{settings: Partial<BackupSettings>, inc: boolean}} data */
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
      /** @type {({brain: BackupBrainStatus, running: boolean} | {bridge: BackupResult | null, inc: boolean})[]} */
      const rows = [
        ...brains.map((b) => ({ brain: b, running })),
        { bridge: st.bridge || null, inc: st.include_bridge_config !== false },
      ];
      reconcile(regions.brainRows, rows, (r) => ('brain' in r ? `brain:${r.brain.id}` : 'bridge'), (r) => ('brain' in r ? brainRow(r) : bridgeRow(r)));
    }

    /** @param {{brain: BackupBrainStatus, running: boolean}} row */
    function brainRow({ brain: b, running }) {
      const sw = toggle({
        checked: b.enabled,
        label: b.enabled ? 'On' : 'Off',
        onChange: async (v, input) => {
          input.disabled = true;
          try {
            await put(`/api/brains/${encodeURIComponent(b.id)}/backup`, { enabled: v });
            toast(`${b.name || b.id}: backups ${v ? 'on' : 'off'}`, 'ok');
            await refresh();
          } catch (e) {
            input.checked = !v;
            toast(errorMessage(e));
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

    /** @param {{bridge: BackupResult | null, inc: boolean}} row */
    function bridgeRow({ bridge, inc }) {
      return h('tr.muted-row',
        h('td', h('div', h('strong', 'Bridge settings')), h('div.small.muted', 'config.json and approved brain.json files')),
        h('td', inc ? pill('On', 'ok') : pill('Off', 'neutral')),
        h('td', resultCell(bridge)),
        h('td'));
    }

    /**
     * @param {BackupResult | null} r
     * @param {BackupBrainStatus} [b]
     * @param {boolean} [running]
     */
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
    /** @param {BackupBrainStatus} b */
    async function mirrorDeletion(b) {
      const name = b.name || b.id;
      const days = (backupStatus.settings || {}).keep_versions_days ?? 30;
      const ok = await confirmDialog({
        title: `Mirror the deletion in ${name}?`,
        message: h('div',
          h('p', 'Many of the files backed up from ', mono(b.dir), ', or most of one of its folders, are gone from it. Check it first: a brain, a disk that is not mounted or a moved folder can empty it by accident.'),
          h('p', 'Mirroring removes the missing files from the latest copy in Google Drive. ',
            days > 0 ? `Their previous copies stay in the older versions for ${plural(days, 'day')}.` : 'Their previous copies stay in the older versions.')),
        confirm: 'Mirror the deletion', danger: true,
      });
      if (!ok) return;
      await post('/api/backup/run', { brains: [b.id], include_bridge: false, force: true });
      toast(`Backing up ${name}, deletions included`, 'ok');
      await refresh();
    }

    /** @param {BackupBrainStatus} b */
    async function editExcludes(b) {
      const ta = linesArea(b.exclude || [], { rows: 8, placeholder: 'node_modules/\n*.log' });
      // Left out of every brain folder by the bridge itself (backupSecretExcludes).
      const secretExcludes = (backupStatus && backupStatus.secret_excludes) || [];
      const includeSecrets = !!(backupStatus && backupStatus.settings && backupStatus.settings.include_secret_files);
      const saved = await openDialog({
        title: `Leave out of ${b.name || b.id}’s backup`,
        body: h('div',
          h('p.muted', 'One pattern per line, relative to the brain folder. A trailing / means a folder; * matches anything in a name.'),
          field({ label: 'Patterns', control: ta }),
          h('p.muted.small', 'Examples: ', mono('node_modules/'), ' ', mono('*.log'), ' ', mono('/big-exports/')),
          secretExcludes.length && !includeSecrets
            ? h('p.muted.small', 'Always left out, whatever you list here: ', ...secretExcludes.flatMap((p, i) => [i ? ' ' : '', mono(p)]))
            : null),
        actions: [
          { label: 'Cancel', value: null },
          { label: 'Save', kind: 'primary', onClick: async () => { await put(`/api/brains/${encodeURIComponent(b.id)}/backup`, { exclude: linesOf(ta) }); return true; } },
        ],
      });
      if (saved) { toast('Saved', 'ok'); await refresh(); }
    }

    // ── restore ──
    // How the last restore went; a failed one would otherwise look just
    // like a finished one once its progress card is gone.
    /** @param {RestoreResult | null} r */
    function renderLastRestore(r) {
      if (!r) return null;
      const what = h('span', r.version === 'current' ? 'the latest copy' : `version ${r.version}`, ' of ', mono(r.brain), ' into ', mono(r.target));
      if (r.ok) {
        return callout('ok', 'Last restore finished', h('span', 'Copied ', what, ` ${plural(r.files, 'file')}, ${fmtBytes(r.bytes)}, `, timeAgo(r.finished), '. Look through it, then add it as a brain.'));
      }
      return callout(r.error === 'cancelled' ? 'warn' : 'bad', r.error === 'cancelled' ? 'Last restore was cancelled' : 'Last restore failed',
        h('span', 'Restoring ', what, ' ', timeAgo(r.finished), r.error === 'cancelled' ? '' : `: ${r.error}`, '. What it copied is still in that folder.'));
    }

    // Built once per connected layout, so a brain chosen and a folder typed
    // stay while the status refreshes; setBrains() only updates the choice.
    function restoreForm() {
      /** @type {string[] | null} */
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
          const r = await get(`/api/brains/${encodeURIComponent(id)}/backup/versions`, { signal: ctx.signal, timeout: 90000 });
          if (currentId() !== id) return;
          versions = r.versions || [];
          paintVersions();
        } catch (e) {
          if (ctx.dead) return;
          mount(versionsBox, callout('bad', 'Could not list the versions', errorMessage(e)));
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
        h('p.muted.small', 'Until the copy is a brain itself, every brain can read it: the sandbox keeps brains out of each other’s folders, and a restored copy is not one yet.'),
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
            message: h('div', h('p', 'Copy ', h('strong', version === 'current' ? 'the latest full copy' : `version ${version}`), ' of ', mono(id), ' into ', mono(target), '?'), h('p.muted', 'It runs in the background; this page shows how it went.')),
            confirm: 'Restore',
          });
          if (!ok) return;
          const r = await post('/api/backup/restore', { brain: id, version, target });
          toast(`Restoring into ${r && r.target ? r.target : target}`, 'ok');
          await refresh();
        });
      });
      form.setBrains = (/** @type {BackupBrainStatus[]} */ brains) => {
        syncOptions(brainSel, [['', 'Choose a brain…'], ...brains.map((b) => /** @type {[string, string]} */ ([b.id, brainLabel(b)])), ['__other', 'Another id…']]);
      };
      return form;
    }
  },
});
