// Vault: the tiered store (a hot tier on this machine, an encrypted cold
// tier in Backblaze B2), its restic snapshots and Litestream. The page
// shows GET /api/vault/status and starts the manual jobs; a rebuild of the
// index is left to the command line.

import { post } from '../app/api.js';
import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { fmtBytes, plural } from '../lib/format.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { pill, callout, timeAgo, mono, empty, pageHeader, spinner, loading, kv } from '../components/common.js';

// What each job the status names is called on the page.
const JOBS = {
  sync: 'Upload',
  evict: 'Eviction',
  rebuild: 'Rebuild of the index',
  snapshot: 'Snapshot',
  check: 'Snapshot check',
  'restore-drill': 'Restore drill',
};
const jobName = (job) => JOBS[job] || job;

const STATES = {
  running: ['Running', 'ok'],
  starting: ['Starting', 'info'],
  error: ['Error', 'bad'],
  off: ['Off', 'neutral'],
};

/** A time with its error, or "never" when it has no time. */
function when(r) {
  return r && r.at ? timeAgo(r.at) : h('span.muted', 'never');
}

function errorLine(err) {
  return err ? h('div.error-text.small', err) : null;
}

export default {
  title: 'Vault',
  render(root, ctx) {
    const statusRes = resource('/api/vault/status');
    let status = null;
    let mode = '';
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
    const start = async (path, body, what) => {
      await post(path, body);
      toast(`${what} started`, 'ok');
      await refresh();
    };

    function layout() {
      for (const k of Object.keys(regions)) delete regions[k];
      if (mode === 'off') {
        regions.off = h('div');
        mount(main, regions.off);
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
        h('section.section', h('h2.section-title', 'Evictions'), regions.evictions));
    }

    function paintAll() {
      const s = status;
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
    function renderHead({ state, error, root: dir, job }) {
      const [label, kind] = STATES[state] || [state || 'Unknown', 'neutral'];
      const running = !!job;
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
    function snapLine(r) {
      if (!r || !r.at) return h('span.muted', 'none yet');
      return h('span',
        r.error ? pill('Failed', 'bad') : null, r.error ? ' ' : null,
        r.kind ? `${r.kind} · ` : '', r.id ? mono(r.id) : null, r.id ? ' · ' : '',
        r.error ? '' : `${plural(r.files || 0, 'file')}, ${fmtBytes(r.bytes_added)} added · `,
        timeAgo(r.at), errorLine(r.error));
    }

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
    function renderB2(u) {
      if (!u) return h('div.card', h('div.card-body', h('p.muted', 'No B2 transactions counted today.')));
      const cls = u.classes || {};
      const pct = u.percent || {};
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

    // ── eviction log ──
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
};
