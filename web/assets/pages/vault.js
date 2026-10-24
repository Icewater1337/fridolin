// Vault: the tiered store (a hot tier on this machine, an encrypted cold
// tier in Backblaze B2), its restic snapshots and Litestream. The page
// shows GET /api/vault/status and starts the manual jobs; a rebuild of the
// index is left to the command line.

import { get, post, put, del } from '../app/api.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { fmtBytes, plural } from '../lib/format.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { confirmDialog, openDialog } from '../components/dialog.js';
import { field, select, textInput, toggle } from '../components/fields.js';
import { nodeFolderPicker } from '../components/node-folder-picker.js';
import { pill, callout, timeAgo, mono, empty, pageHeader, spinner, loading, kv } from '../components/common.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Child } from '../lib/dom.js' */
/** @import { B2Usage, BrainBackup, DeletionHold, DrillRecord, DriveSnapshotsStatus, LitestreamStatus, SnapshotRecord, SnapshotsStatus, TargetRetention, VaultEvictRecord, VaultEviction, VaultFileCounts, VaultReach, VaultStatus, VaultSyncRecord, VaultTarget } from '../../types/api.js' */

/**
 * What the hot tier's card shows, from the status.
 * @typedef {object} HotView
 * @property {number} hot
 * @property {number} budget
 * @property {number} target
 * @property {number} cold
 * @property {Partial<VaultFileCounts>} files
 * @property {string} lastUpload
 * @property {VaultSyncRecord | null} sync
 * @property {VaultEvictRecord | null} evict
 * @property {VaultReach | null} reachable
 */

/**
 * The parts of the page that a layout has, each filled by paint().
 * @typedef {object} Regions
 * @property {HTMLDivElement} [targets]
 * @property {HTMLDivElement} [off]
 * @property {HTMLDivElement} [head]
 * @property {HTMLDivElement} [hot]
 * @property {HTMLDivElement} [snapshots]
 * @property {HTMLDivElement} [litestream]
 * @property {HTMLDivElement} [b2]
 * @property {HTMLDivElement} [drill]
 * @property {HTMLDivElement} [evictions]
 * @property {HTMLTableSectionElement} [evictionRows]
 */

// What each job the status names is called on the page.
/** @type {Record<string, string>} */
const JOBS = {
  sync: 'Upload',
  evict: 'Eviction',
  rebuild: 'Rebuild of the index',
  snapshot: 'Snapshot',
  check: 'Snapshot check',
  'restore-drill': 'Restore drill',
};
const jobName = (/** @type {string} */ job) => JOBS[job] || job;

/** @type {Record<string, [string, string]>} */
const STATES = {
  running: ['Running', 'ok'],
  starting: ['Starting', 'info'],
  error: ['Error', 'bad'],
  off: ['Off', 'neutral'],
};

/**
 * A time with its error, or "never" when it has no time.
 * @param {{at?: string} | null | undefined} r
 */
function when(r) {
  return r && r.at ? timeAgo(r.at) : h('span.muted', 'never');
}

/** @param {string} [err] */
function errorLine(err) {
  return err ? h('div.error-text.small', err) : null;
}

export default /** @satisfies {PageModule} */ ({
  title: 'Vault',
  render(root, ctx) {
    const statusRes = resource('/api/vault/status');
    /** @type {Partial<VaultStatus> | null} */
    let status = null;
    let mode = '';
    /** @type {Regions} */
    const regions = {};
    const main = h('div.page-stack', h('div.card', loading('Checking the vault…')));
    mount(root, pageHeader('Vault', 'Files kept on this computer and, encrypted, in Backblaze B2, with snapshots to restore from.'), main);

    // Polls faster while a job runs or the store starts.
    const busy = () => !!(status && (status.job || status.state === 'starting'));
    ctx.follow(statusRes, (s) => {
      status = s || {};
      const next = status.state === 'off' ? 'off' : 'on';
      if (next !== mode) {
        mode = next;
        layout();
      }
      paintAll();
    }, () => (busy() ? 2000 : 10000));

    const refresh = () => statusRes.refresh();

    // Starts a job: 202 says it started, a 409 (another job runs) becomes
    // a toast through btn's handler.
    /**
     * @param {string} path
     * @param {unknown} body
     * @param {string} what
     */
    const start = async (path, body, what) => {
      await post(path, body);
      toast(`${what} started`, 'ok');
      await refresh();
    };

    function layout() {
      for (const k of /** @type {(keyof Regions)[]} */ (Object.keys(regions))) delete regions[k];
      regions.targets = h('div');
      const targets = h('section.section', h('h2.section-title', 'Backup targets'), regions.targets);
      if (mode === 'off') {
        regions.off = h('div');
        mount(main, regions.off, targets);
        return;
      }
      regions.head = h('div');
      regions.hot = h('div');
      regions.snapshots = h('div');
      regions.litestream = h('div');
      regions.b2 = h('div');
      regions.drill = h('div');
      regions.evictions = h('div');
      regions.evictionRows = h('tbody');
      mount(main,
        regions.head,
        h('section.section', h('h2.section-title', 'Hot tier'), regions.hot),
        h('section.section', h('h2.section-title', 'Snapshots'), regions.snapshots),
        h('section.section', h('h2.section-title', 'Litestream'), regions.litestream),
        h('section.section', h('h2.section-title', 'B2 transactions'), regions.b2),
        h('section.section', h('h2.section-title', 'Restore drill'), regions.drill),
        h('section.section', h('h2.section-title', 'Evictions'), regions.evictions),
        targets);
    }

    function paintAll() {
      const s = status;
      paint(regions.targets, s.targets || [], renderTargets);
      if (mode === 'off') {
        paint(regions.off, null, renderOff);
        return;
      }
      const job = s.job || '';
      paint(regions.head, { state: s.state || '', error: s.error || '', root: s.root || '', job }, renderHead);
      paint(regions.hot, {
        hot: s.hot_bytes || 0, budget: s.budget_bytes || 0, target: s.target_bytes || 0, cold: s.cold_bytes || 0,
        files: s.files || {}, lastUpload: s.last_upload || '', sync: s.last_sync || null, evict: s.last_evict || null,
        reachable: s.reachable || null,
      }, renderHot);
      paint(regions.snapshots, { snap: s.snapshots || null, job }, renderSnapshots);
      paint(regions.litestream, s.litestream || null, renderLitestream);
      paint(regions.b2, s.b2 || null, renderB2);
      paint(regions.drill, { drill: s.last_drill || null, job }, renderDrill);
      paintEvictions(s.evictions || []);
    }

    // ── off ──
    function renderOff() {
      return h('div.card.hero',
        h('div.hero-icon', icon('lock')),
        h('div.hero-body',
          h('h2', 'The vault is off'),
          h('p', 'Set ', mono('vault.enabled'), ' to ', mono('true'), ' in config.json and restart the bridge to keep files in the tiered store, with an encrypted copy in Backblaze B2.')));
    }

    // ── state and the manual jobs ──
    /** @param {{state: string, error: string, root: string, job: string}} head */
    function renderHead({ state, error, root: dir, job }) {
      const [label, kind] = STATES[state] || [state || 'Unknown', 'neutral'];
      const running = !!job;
      /** @type {(text: string, iconName: string, path: string, body?: unknown, primary?: boolean) => HTMLButtonElement} */
      const action = (text, iconName, path, body, primary) => btn(text, {
        kind: primary ? 'primary' : '', icon: iconName, disabled: running,
        onClick: () => start(path, body, text.replace(/ now$/, '')),
      });
      return h('div.card.vault-head',
        h('div.card-head',
          h('div.card-title', icon('lock'), h('h2', 'Tiered store')),
          pill(label, kind, { pulse: state === 'starting' })),
        h('div.card-body',
          state === 'error' ? callout('bad', 'The vault is not running', error || 'Unknown error') : null,
          state !== 'error' && error ? callout('warn', null, error) : null,
          dir ? h('p', 'Hot tier in ', mono(dir, { copy: true }), '.') : null,
          running ? h('div.loading-row.vault-job', spinner(), h('span', `${jobName(job)} running…`)) : null,
          h('div.actions',
            action('Upload now', 'up', '/api/vault/sync', undefined, true),
            action('Evict now', 'trash', '/api/vault/evict'),
            action('Snapshot now', 'backup', '/api/vault/snapshot', { kind: 'nightly' }),
            action('Check snapshots', 'shield', '/api/vault/check', {}))));
    }

    // ── hot tier ──
    /** @param {HotView} view */
    function renderHot({ hot, budget, target, cold, files, lastUpload, sync, evict, reachable }) {
      const pct = budget ? Math.round((hot / budget) * 100) : 0;
      let usage;
      if (budget) {
        const meter = h('meter.vault-meter', {
          min: 0, max: budget, value: Math.min(hot, budget), low: target || budget, high: target || budget, optimum: 0,
          'aria-label': 'Hot tier usage', 'aria-valuetext': `${fmtBytes(hot)} of ${fmtBytes(budget)}`,
        });
        const mark = target ? h('span.vault-target', { title: `Eviction target: ${fmtBytes(target)}`, 'aria-hidden': 'true' }) : null;
        if (mark) mark.style.left = `${Math.min(100, (target / budget) * 100)}%`;
        usage = h('div.vault-usage',
          h('div.vault-usage-head', h('strong', `${fmtBytes(hot)} of ${fmtBytes(budget)}`), h('span.muted', ` · ${pct}%`)),
          h('div.vault-meter-wrap', meter, mark),
          target ? h('p.muted.small', 'The mark is the eviction target, ', h('strong', fmtBytes(target)), ': the budget less headroom. Eviction brings the hot tier down to it.') : null,
          hot > budget ? callout('warn', null, 'The hot tier is over its budget.') : null);
      } else {
        usage = h('p', h('strong', fmtBytes(hot)), ' on this computer; no budget set.');
      }
      const reach = !reachable || !reachable.at ? h('span.muted', 'not checked yet')
        : h('span', reachable.ok ? pill('Reachable', 'ok') : pill('Unreachable', 'bad'), ' ', timeAgo(reachable.at), errorLine(reachable.error));
      return h('div.card',
        h('div.card-body',
          usage,
          kv([
            ['Files', `${files.hot || 0} only here · ${files.both || 0} in both tiers · ${files.cold || 0} only in B2`],
            ['Cold tier (B2)', fmtBytes(cold)],
            ['Last upload', timeAgo(lastUpload)],
            ['Last upload run', sync && sync.at ? h('span', when(sync), ` · ${plural(sync.uploaded || 0, 'file')}, ${fmtBytes(sync.uploaded_bytes)}`,
              sync.failed ? h('span.error-text', ` · ${sync.failed} failed`) : null, errorLine(sync.error)) : h('span.muted', 'never')],
            ['Last eviction', evict && evict.at ? h('span', when(evict), ` · ${plural(evict.evicted || 0, 'file')}, ${fmtBytes(evict.freed_bytes)} freed`,
              evict.over_budget ? h('span.error-text', ' · still over budget') : null, errorLine(evict.error)) : h('span.muted', 'never')],
            ['B2', reach],
          ])));
    }

    // ── snapshots ──
    /** @param {SnapshotRecord | null | undefined} r */
    function snapLine(r) {
      if (!r || !r.at) return h('span.muted', 'none yet');
      return h('span',
        r.error ? pill('Failed', 'bad') : null, r.error ? ' ' : null,
        r.kind ? `${r.kind} · ` : '', r.id ? mono(r.id) : null, r.id ? ' · ' : '',
        r.error ? '' : `${plural(r.files || 0, 'file')}, ${fmtBytes(r.bytes_added)} added · `,
        timeAgo(r.at), errorLine(r.error));
    }

    /** @param {{snap: SnapshotsStatus | null, job: string}} shown */
    function renderSnapshots({ snap, job }) {
      if (!snap || !snap.enabled) {
        return h('div.card', h('div.card-body', h('p.muted', 'Snapshots are off.'), snap && snap.error ? callout('bad', null, snap.error) : null));
      }
      const check = snap.last_check;
      const prune = snap.last_prune;
      const drive = snap.drive;
      const hold = snap.deletion_hold;
      return h('div.card',
        h('div.card-body',
          snap.error ? callout('bad', 'Snapshots fail', snap.error) : null,
          hold ? renderHold(hold, !!job) : null,
          kv([
            ['Repository', mono(snap.repo || '')],
            ['Last snapshot', snapLine(snap.last)],
            ['Last hourly', snapLine(snap.last_hourly)],
            ['Last nightly', snapLine(snap.last_nightly)],
            ['Last check', check && check.at ? h('span', check.ok ? pill('OK', 'ok') : pill('Failed', 'bad'), ' ', timeAgo(check.at),
              check.read_data_subset ? ` · read ${check.read_data_subset} of the data` : '', errorLine(check.error)) : h('span.muted', 'never')],
            ['Last prune', prune && prune.at ? h('span', prune.error ? pill('Failed', 'bad') : null, prune.error ? ' ' : null, timeAgo(prune.at), errorLine(prune.error)) : h('span.muted', 'never')],
            ['Copy to Google Drive', renderDrive(drive)],
          ])));
    }

    /** @param {DriveSnapshotsStatus | null | undefined} d */
    function renderDrive(d) {
      if (!d || !d.enabled) return h('span.muted', 'Off');
      const copy = d.last_copy;
      return h('span',
        d.repo ? h('div', mono(d.repo)) : null,
        h('div', d.completed_once ? pill('Copied', 'ok') : pill('First copy not done yet', 'warn'), ' ',
          copy && copy.at ? h('span', 'last copy ', timeAgo(copy.at)) : h('span.muted', 'no copy yet')),
        errorLine(copy && copy.error));
    }

    // Pruning stops when a snapshot holds far fewer files than the one
    // before, so a mass deletion cannot prune away the snapshots that
    // still have the files; only the owner lets it go on.
    /**
     * @param {DeletionHold} hold
     * @param {boolean} running
     */
    function renderHold(hold, running) {
      return callout('warn', 'Pruning is on hold',
        h('p', 'Snapshot ', mono(hold.snapshot || ''), ` holds ${plural(hold.files || 0, 'file')}, down from ${hold.prev_files || 0} in the one before`,
          hold.since ? [' (held ', timeAgo(hold.since), ')'] : null,
          '. Older snapshots are kept until you allow pruning, so nothing deleted by mistake is lost.'),
        h('div.actions', btn('Allow pruning…', {
          icon: 'trash', disabled: running,
          onClick: async () => {
            const ok = await confirmDialog({
              title: 'Allow pruning?',
              message: h('div',
                h('p', 'Check first that the files missing from snapshot ', mono(hold.snapshot || ''), ' were meant to go: a disk that is not mounted or a moved folder can empty the vault by accident.'),
                h('p', 'Pruning then removes older snapshots as the retention says, and with them the only copies of those files.')),
              confirm: 'Allow pruning', danger: true,
            });
            if (!ok) return;
            await post('/api/vault/prune/allow', {});
            toast('Pruning allowed', 'ok');
            await refresh();
          },
        })));
    }

    // ── Litestream ──
    /** @param {LitestreamStatus | null} l */
    function renderLitestream(l) {
      if (!l || !l.enabled) return h('div.card', h('div.card-body', h('p.muted', 'Litestream is off.')));
      return h('div.card',
        h('div.card-body',
          kv([
            ['State', l.running
              ? h('span', pill('Running', 'ok'), l.pid ? [' · pid ', mono(String(l.pid))] : null, l.since ? [' · since ', timeAgo(l.since)] : null)
              : pill('Not running', 'bad')],
            ['Restarts', String(l.restarts || 0)],
            ['Databases', (l.dbs || []).length ? h('span.vault-dbs', (l.dbs || []).map((d) => mono(d))) : h('span.muted', 'none')],
            ['Last error', l.last_error ? h('span.error-text', l.last_error) : h('span.muted', 'none')],
          ])));
    }

    // ── B2 transactions ──
    // What the bridge spent today (UTC) of Backblaze's capped classes: the
    // free tier refuses every call of a class past 2,500 a day.
    /** @param {B2Usage | null} u */
    function renderB2(u) {
      if (!u) return h('div.card', h('div.card-body', h('p.muted', 'No B2 transactions counted today.')));
      const cls = u.classes || {};
      const pct = u.percent || {};
      /** @param {string} c */
      const level = (c) => {
        const p = pct[c] || 0;
        const kind = p >= 100 ? 'bad' : p >= 70 ? 'warn' : 'ok';
        return h('span', pill(`${p}%`, kind), ` ${cls[c] || 0} of ${u.budget || 0}`);
      };
      const y = u.yesterday;
      return h('div.card',
        h('div.card-body',
          (u.warnings || []).map((w) => callout(u.cap_exceeded ? 'bad' : 'warn', null, w)),
          kv([
            ['Day (UTC)', u.day || ''],
            ['Class B (downloads, HEAD)', level('B')],
            ['Class C (log-ins, listings)', level('C')],
            ['Class A (uploads, free)', String(cls.A || 0)],
            ['The day before', y ? `class B ${y.B || 0}, class C ${y.C || 0}` : h('span.muted', 'not seen')],
          ]),
          h('p.muted.small', 'Estimated from what each call costs; vault.b2_daily_budget sets the budget, and doctor warns at 70%.')));
    }

    // ── restore drill ──
    /** @param {{drill: DrillRecord | null, job: string}} shown */
    function renderDrill({ drill: d, job }) {
      const run = btn('Run restore drill', {
        icon: 'play', disabled: !!job,
        onClick: () => start('/api/vault/restore-drill', undefined, 'Restore drill'),
      });
      if (!d || !d.at) {
        return h('div.card', h('div.card-body',
          h('p.muted', 'No restore drill yet. A drill restores the latest snapshot into a scratch folder and checks that the brains are in it.'),
          h('div.actions', run)));
      }
      return h('div.card',
        h('div.card-body',
          h('div', d.ok ? pill('Passed', 'ok') : pill('Failed', 'bad'), ' ', timeAgo(d.at), d.snapshot ? [' · snapshot ', mono(d.snapshot)] : null),
          d.detail ? h('p', { class: d.ok ? 'muted' : 'error-text' }, d.detail) : null,
          (d.checks || []).length ? h('ul.vault-checks', d.checks.map((c) => h(`li.${c.ok ? 'ok' : 'bad'}`,
            icon(c.ok ? 'check' : 'x'), h('span', h('strong', c.name), c.detail ? h('span.muted', ` — ${c.detail}`) : null)))) : null,
          h('div.actions', run)));
    }

    // ── node targets ──
    // restic repositories in folders on nodes, besides the cloud: each
    // brain.json says which get its folder (backup.targets), and which of
    // its paths go only there (backup.node_only).
    /** @param {VaultTarget[]} list */
    function renderTargets(list) {
      const add = btn('Add target…', { icon: 'plus', kind: list.length ? '' : 'primary', onClick: addTarget });
      if (!list.length) {
        return h('div.card', h('div.card-body',
          h('p.muted', 'No backup targets on your nodes yet. A target is a folder on a drive of another machine of yours, where the core keeps an encrypted restic repository: the data goes there over the node connection, never through the cloud.'),
          h('div.actions', add)));
      }
      return h('div.page-stack', list.map(targetCard), h('div.actions', add));
    }

    /** @param {VaultTarget} t */
    function targetCard(t) {
      const running = !!t.job;
      /** @type {(text: string, iconName: string, path: string, body: unknown) => HTMLButtonElement} */
      const job = (text, iconName, path, body) => btn(text, {
        icon: iconName, small: true, disabled: running || !t.ready,
        onClick: () => start(`/api/vault/targets/${encodeURIComponent(t.name)}/${path}`, body, `${text} (${t.name})`),
      });
      const [label, kind] = t.ready ? (t.online ? ['Ready', 'ok'] : ['Offline', 'warn']) : [t.online ? 'Not ready' : 'Offline', 'warn'];
      const keep = /** @type {Partial<TargetRetention>} */ (t.retention || {});
      const drill = t.last_drill;
      const check = t.last_check;
      return h('div.card',
        h('div.card-head',
          h('div.card-title', icon('backup'), h('h2', t.name)),
          pill(label, kind)),
        h('div.card-body',
          t.problem ? callout('warn', null, t.problem) : null,
          running ? h('div.loading-row.vault-job', spinner(), h('span', `${jobName(t.job)} running…`)) : null,
          t.deletion_hold ? callout('warn', 'Pruning is on hold',
            h('p', 'Snapshot ', mono(t.deletion_hold.snapshot || ''), ` holds ${plural(t.deletion_hold.files || 0, 'file')}, down from ${t.deletion_hold.prev_files || 0}.`),
            h('div.actions', btn('Allow pruning', { small: true, icon: 'trash', onClick: async () => { await post(`/api/vault/targets/${encodeURIComponent(t.name)}/prune/allow`, {}); await refresh(); } }))) : null,
          kv(/** @type {[Child, Child][]} */ ([
            ['Node', h('span', t.node_name || t.node, ' ', mono(t.node))],
            ['Folder', mono(t.folder, { copy: true })],
            ['Brains', (t.brains || []).length ? (t.brains || []).join(', ') : h('span.muted', 'none yet — choose this target in a brain\'s backup settings')],
            ['Schedule', `nightly at ${t.nightly_at}${t.hourly ? ', and hourly' : ''}${t.include_data_dir ? '; with the data dir' : ''}`],
            ['Keeps', `${keep.hourly || 0} hourly, ${keep.daily || 0} daily, ${keep.weekly || 0} weekly, ${keep.monthly || 0} monthly`],
            ['Last nightly', snapLine(t.last_nightly)],
            t.hourly ? ['Last hourly', snapLine(t.last_hourly)] : null,
            ['Size', t.size_at ? `${fmtBytes(t.size)} in ${plural(t.files || 0, 'file')} · ${fmtBytes(t.free)} free on its disk` : h('span.muted', 'not measured yet')],
            ['Last check', check && check.at ? h('span', check.ok ? pill('OK', 'ok') : pill('Failed', 'bad'), ' ', timeAgo(check.at), errorLine(check.error)) : h('span.muted', 'never')],
            ['Restore drill', drill && drill.at ? h('span', drill.ok ? pill('Passed', 'ok') : pill('Failed', 'bad'), ' ', timeAgo(drill.at), drill.detail ? h('div.muted.small', drill.detail) : null) : h('span.muted', 'never')],
          ]).filter(Boolean)),
          h('div.actions',
            job('Back up now', 'backup', 'snapshot', { kind: 'nightly' }),
            job('Check', 'shield', 'check', {}),
            job('Restore drill', 'play', 'restore-drill', {}),
            btn('Brains…', { small: true, icon: 'edit', onClick: () => chooseBrains(t) }),
            btn('Remove…', { small: true, kind: 'danger', icon: 'x', disabled: running, onClick: () => removeTarget(t) }))));
    }

    // Which brains go to the target, and which of their paths go only to
    // node targets (brain.json backup.targets and backup.node_only).
    /** @param {VaultTarget} t */
    async function chooseBrains(t) {
      const brains = await get('/api/brains');
      const key = `node:${t.name}`;
      const rows = (brains || []).map((b) => {
        const bk = /** @type {Partial<BrainBackup>} */ ((b.manifest && b.manifest.backup) || {});
        const chosen = !!(bk.targets || {})[key];
        const nodeOnly = (bk.node_only || []).join(', ');
        return {
          id: b.id, targets: { ...(bk.targets || {}) }, chosen, nodeOnly,
          was: { chosen, nodeOnly },
          input: textInput({ value: nodeOnly, placeholder: '/work/, /clients/', 'aria-label': `Paths of ${b.id} only for node targets` }),
        };
      });
      const body = h('div.stack',
        h('p.muted', 'A brain\'s folder goes here once you tick it. Paths listed beside it never go to the cloud, only to the node targets the brain goes to; its exclude list still keeps the rest out of both.'),
        rows.map((r) => h('div.stack',
          toggle({ label: r.id, checked: r.chosen, onChange: (v) => { r.chosen = v; } }),
          field({ label: 'Only to node targets', control: r.input }))));
      await openDialog({
        title: `Brains backed up to ${t.name}`,
        wide: true,
        body,
        actions: [
          { label: 'Cancel', value: null },
          {
            label: 'Save', kind: 'primary',
            onClick: async () => {
              for (const r of rows) {
                const nodeOnly = r.input.value.split(',').map((x) => x.trim()).filter(Boolean);
                if (r.chosen === r.was.chosen && nodeOnly.join(', ') === r.was.nodeOnly) continue;
                await put(`/api/brains/${encodeURIComponent(r.id)}/backup`, { targets: { ...r.targets, [key]: r.chosen }, node_only: nodeOnly });
              }
              toast('Saved', 'ok');
              await refresh();
              return true;
            },
          },
        ],
      });
    }

    /** @param {VaultTarget} t */
    async function removeTarget(t) {
      const ok = await confirmDialog({
        title: `Remove the target ${t.name}?`,
        message: h('div',
          h('p', 'No more snapshots go to ', mono(t.folder), ' on ', t.node_name || t.node, '. The repository stays there, with its snapshots, and its password stays in the host\'s secrets.'),
          h('p', 'Brains that chose this target keep the setting, which then means nothing.')),
        confirm: 'Remove', danger: true,
      });
      if (!ok) return;
      await del(`/api/vault/targets/${encodeURIComponent(t.name)}`);
      toast(`Removed the target ${t.name}`, 'ok');
      await refresh();
    }

    // The wizard: a node that offers backups, a folder on one of its
    // drives, a name and a schedule.
    async function addTarget() {
      const resp = await get('/api/nodes');
      const ready = (resp.nodes || []).filter((n) => n.device === 'approved' && n.effective && n.effective.backup);
      if (!ready.length) {
        await openDialog({
          title: 'No node offers backups yet',
          body: h('div',
            h('p', 'On the machine with the backup disk, turn backups on for its node:'),
            h('p', mono('fridolin node backup on', { copy: true }), ' (or ', mono('--root D:\\'), ' to lend one drive only), then restart its agent.'),
            h('p', 'Then approve what it offers on the Nodes page, and come back here.')),
        });
        return;
      }
      const state = { node: ready[0].id, folder: '' };
      const name = textInput({ value: 'nas', 'aria-label': 'Target name', pattern: '[a-z0-9][a-z0-9-]{0,31}' });
      const nightly = textInput({ value: '03:00', 'aria-label': 'Nightly at', pattern: '[0-2][0-9]:[0-5][0-9]' });
      let hourly = false;
      let dataDir = false;
      const chosen = h('code.mono', 'none yet');
      const pickerWrap = h('div');
      const showPicker = () => mount(pickerWrap, nodeFolderPicker({ node: state.node, onPick: (/** @type {string} */ p) => { state.folder = p; chosen.textContent = p; } }));
      const nodeSel = select(ready.map((n) => [n.id, `${n.name} (${n.id}${n.online ? '' : ', offline'})`]), state.node, { 'aria-label': 'Node' });
      nodeSel.addEventListener('change', () => { state.node = nodeSel.value; state.folder = ''; chosen.textContent = 'none yet'; showPicker(); });
      showPicker();
      await openDialog({
        title: 'Add a backup target',
        wide: true,
        body: h('div.stack',
          field({ label: 'Node', control: nodeSel }),
          field({ label: 'Folder on the node', help: 'Pick a drive, then a folder, or name a new one. The repository goes in it.', control: h('div', pickerWrap, h('p.small', 'Chosen: ', chosen)) }),
          field({ label: 'Name', help: 'Lower-case letters, digits and -. Brains choose it as node:<name>.', control: name }),
          field({ label: 'Nightly snapshot at', control: nightly }),
          toggle({ label: 'Also a snapshot every hour', onChange: (v) => { hourly = v; } }),
          toggle({ label: 'Include the data dir in the nightly snapshot', onChange: (v) => { dataDir = v; } })),
        actions: [
          { label: 'Cancel', value: null },
          {
            label: 'Add target', kind: 'primary',
            onClick: async () => {
              if (!state.folder) { toast('Pick a folder on the node first', 'warn'); return false; }
              await post('/api/vault/targets', {
                name: name.value.trim(), kind: 'node', node: state.node, folder: state.folder,
                nightly_at: nightly.value.trim(), hourly, include_data_dir: dataDir,
              });
              toast('Target added', 'ok');
              await refresh();
              return true;
            },
          },
        ],
      });
    }

    // ── eviction log ──
    /** @param {VaultEviction[]} list */
    function paintEvictions(list) {
      if (!list.length) {
        paint(regions.evictions, 'none', () => h('div.card', empty('No evictions yet', 'Files moved off this computer to make room show here.')));
        return;
      }
      paint(regions.evictions, 'table', () => h('div.card.flush.table-wrap', h('table.table.vault-table',
        h('thead', h('tr', h('th', 'File'), h('th.right', 'Size'), h('th', 'Reason'), h('th', 'When'))),
        regions.evictionRows)));
      reconcile(regions.evictionRows, list, (e) => `${e.at}|${e.path}`, (e) => h('tr',
        h('td', mono(e.path)),
        h('td.right.nowrap', fmtBytes(e.size)),
        h('td', e.reason || ''),
        h('td.nowrap', timeAgo(e.at))));
    }
  },
});
