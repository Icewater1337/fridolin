// Overview: health, WhatsApp pairing, Claude CLI, sandbox, brains, backup,
// warnings and the latest messages.

import { get, post, qs } from '../app/api.js';
import { store } from '../app/store.js';
import { brainsResource, resource, schedulesResource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { fmtDuration, fmtBytes, plural, parseTs } from '../lib/format.js';
import { phone } from '../lib/jid.js';
import { waKind, waLabel } from '../lib/whatsapp.js';
import { routingSummary, hasDMBrain, issueLabel, backupOwner } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { pill, badge, callout, timeAgo, mono, empty, spinner } from '../components/common.js';
import { runtimeSummary, firstBrainHero } from '../components/brain.js';
import { showActivity } from '../components/activity-item.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { ActivityEntry, Brain, HousekeepingCounts, HousekeepingFinding, HousekeepingStatus, SandboxStatus, Status, WhatsAppStatus } from '../../types/api.js' */

export default /** @satisfies {PageModule} */ ({
  title: 'Overview',
  render(root, ctx) {
    const header = h('div.health');
    const cta = h('div');
    const alerts = h('div.alerts');
    const agentsNote = h('div.alerts');
    const waCard = h('div.card.wa-card');
    const claudeCard = h('div.card');
    const sandboxCard = h('div.card');
    const coreBox = h('div');
    const backupCard = h('div.card');
    const housekeepingCard = h('div.card');
    const brainsBox = h('div');
    const brainList = h('div.brain-list');
    const activityBox = h('div.activity-list.compact');

    mount(root,
      h('header.page-header', h('div.page-heading', h('h1', 'Overview'), header)),
      cta,
      alerts,
      agentsNote,
      waCard,
      coreBox,
      h('div.grid.three', claudeCard, sandboxCard, backupCard),
      housekeepingCard,
      h('section.section',
        h('div.section-head', h('h2.section-title', 'Brains'), btn('Manage', { href: '#/brains', small: true, kind: 'ghost', icon: 'right' })),
        brainsBox),
      h('section.section',
        h('div.section-head', h('h2.section-title', 'Recent activity'), btn('All activity', { href: '#/activity', small: true, kind: 'ghost', icon: 'right' })),
        activityBox));

    // ── WhatsApp + QR ──
    const qr = { img: null, url: '', pairing: false };
    const loadQR = async (signal) => {
      try {
        const r = await get('/api/whatsapp/qr', { signal });
        if (r && r.data_url && r.data_url !== qr.url) {
          qr.url = r.data_url;
          if (qr.img) qr.img.src = r.data_url;
        }
        if (qr.age) qr.age.replaceChildren(timeAgo(r.created_at, 'Code from '));
      } catch (e) {
        if (e.status !== 404) throw e;
      }
    };

    /** @param {Status} s */
    const renderWA = (s) => {
      const wa = /** @type {Partial<WhatsAppStatus>} */ (s.whatsapp || {});
      const shown = { mode: wa.mode, own_number: wa.own_number, detail: wa.detail, qr_available: wa.qr_available, since: wa.since, dev: s.dev_mode };
      paint(waCard, shown, buildWA);
      waCard.classList.toggle('pairing', !!wa.qr_available);
    };
    /** @param {Partial<WhatsAppStatus> & {dev: boolean}} wa */
    const buildWA = (wa) => {
      const kind = waKind(wa.mode);
      const notPaired = !wa.own_number;
      const canPair = !wa.dev && (wa.mode === 'qr-expired' || (notPaired && !['pairing', 'connecting', 'connected', 'disabled'].includes(wa.mode)));
      const pairBtn = canPair ? btn('Start pairing', {
        kind: 'primary', icon: 'phone',
        onClick: async () => {
          await post('/api/whatsapp/pair');
          toast('Pairing started — a QR code appears in a moment', 'info');
        },
      }) : null;
      const head = h('div.card-head',
        h('div.card-title', icon('phone'), h('h2', 'WhatsApp')),
        pill(waLabel(wa.mode), kind, { pulse: wa.mode === 'connecting' || wa.mode === 'pairing' }));
      const facts = h('div.card-body',
        wa.own_number ? h('p', 'Linked to ', mono(phone(wa.own_number))) : null,
        // In dev mode the detail only repeats the note below.
        wa.detail && !wa.dev ? h('p.muted', wa.detail) : null,
        parseTs(wa.since) ? h('p.muted.small', timeAgo(wa.since, 'Since ')) : null,
        wa.dev ? h('p.muted', 'Dev mode: nothing is sent to WhatsApp. Use the ', h('a', { href: '#/dev' }, 'Dev page'), ' to simulate messages.') : null,
        pairBtn ? h('div.actions', pairBtn) : null);
      if (wa.qr_available) {
        qr.img = h('img.qr-img', { alt: 'WhatsApp pairing QR code', width: 280, height: 280 });
        if (qr.url) qr.img.src = qr.url;
        qr.age = h('div.muted.small');
        loadQR(ctx.signal).catch(() => {});
        return [
          head,
          h('div.qr-layout',
            h('div.qr-frame', qr.img, qr.age),
            h('div.qr-steps',
              h('h3', 'Link this bridge to WhatsApp'),
              h('ol',
                h('li', 'Open WhatsApp on the phone that holds the bridge’s account.'),
                h('li', 'Go to ', h('strong', 'Settings → Linked Devices → Link a Device'), '.'),
                h('li', 'Point the camera at this code.')),
              h('p.muted.small', 'The code changes every few seconds — this page keeps it fresh. Use a second number if you can: the bridge answers as whichever account it is linked to.'))),
        ];
      }
      qr.img = null;
      qr.url = '';
      return [head, facts];
    };

    // ── health header ──
    // The uptime in whole minutes, so the header changes once a minute.
    const uptime = (sec) => (sec < 60 ? '< 1 min' : fmtDuration(Math.floor(sec / 60) * 60));
    /** @param {Status} s */
    const renderHealth = (s) => paint(header, {
      version: s.version, dev: s.dev_mode, started: s.started_at, up: uptime(s.uptime_s || 0), debouncing: s.debouncing,
    }, (x) => [
      pill('Running', 'ok'),
      h('span.health-item', { title: x.started ? `Started ${new Date(x.started).toLocaleString()}` : '' }, icon('clock'), `up ${x.up}`),
      x.version ? h('span.health-item.mono', x.version) : null,
      x.dev ? badge('Dev mode', 'warn', 'Started with --no-whatsapp') : null,
      x.debouncing ? h('span.health-item', plural(x.debouncing, 'message'), ' gathering') : null,
    ]);

    // ── first run ──
    /** @param {Status} s */
    const renderCTA = (s) => paint(cta, (s.brains || []).length === 0 && (s.issues || []).length === 0, (show) => (show ? firstBrainHero() : null));

    // ── warnings and brain issues ──
    /** @param {Status} s */
    const renderAlerts = (s) => paint(alerts, { warnings: s.warnings || [], issues: s.issues || [] }, ({ warnings, issues }) => [
      warnings.map((w) => callout('warn', null, w)),
      issues.map((is) => {
        const label = issueLabel(is.kind);
        const link = is.id ? h('a', { href: `#/brains/${encodeURIComponent(is.id)}` }, is.kind === 'drift' ? 'Review and approve' : 'Open brain') : null;
        return callout(is.kind === 'drift' ? 'warn' : 'bad', `${label}${is.id ? `: ${is.id}` : ''}`,
          h('div', is.detail), h('div.muted.small', mono(is.path)), link ? h('div', link) : null);
      }),
    ]);

    // ── cards ──
    /** @param {Status} s */
    const renderClaude = (s) => paint(claudeCard, { ...(s.agent_provider === 'codex' ? s.codex : s.claude), provider: s.agent_provider || 'claude' }, (c) => [
      h('div.card-head', h('div.card-title', icon('terminal'), h('h2', c.provider === 'codex' ? 'Codex CLI' : 'Claude CLI')), c.found ? pill('Found', 'ok') : pill('Not found', 'bad')),
      h('div.card-body',
        c.path ? h('p', mono(c.path)) : null,
        c.version ? h('p.muted', c.version) : null,
        !c.found ? h('p.muted', 'Install the CLI and log in with ', h('code.mono', c.provider), ', or point ', h('a', { href: '#/settings/claude' }, 'Settings → AI mode'), ' at the right binary.') : null),
    ]);

    /** @param {Status} s */
    const renderSandbox = (s) => paint(sandboxCard, /** @type {Partial<SandboxStatus>} */ (s.sandbox || {}), (sb) => {
      let p;
      let text;
      if (!sb.supported) { p = pill('Not available', 'neutral'); text = 'The sandbox uses macOS sandbox-exec, or bubblewrap in the fridolin-core container. Here brains can read everything the bridge’s user can, the dashboard token included. A separate user for the bridge keeps them out of your own files, not out of the bridge’s.'; } else if (sb.enabled) { p = pill('On', 'ok'); text = 'Brains write only their own folders, and cannot read other brains’ folders, your keys, keychains, mail or documents.'; } else { p = pill('Off', 'warn'); text = 'Brains can read everything your user account can. Turn the sandbox on in Settings.'; }
      return [
        h('div.card-head', h('div.card-title', icon('shield'), h('h2', 'Sandbox')), p),
        h('div.card-body', h('p.muted', text), sb.supported ? h('a.small', { href: '#/settings/sandbox' }, 'Sandbox settings') : null),
      ];
    });

    // The containerized core: its tailnet name and its lease, when either
    // is on. A lost lease means this copy no longer sends anything.
    /** @param {Status} s */
    const renderCore = (s) => paint(coreBox, s.core || null, (c) => {
      if (!c) return [];
      const tn = c.tailnet;
      const ls = c.lease;
      let p = pill('Core', 'neutral');
      if (ls && ls.state === 'lost') p = pill('Lease lost', 'bad');
      else if (ls && ls.state === 'unverified') p = pill('Lease unverified', 'warn');
      else if (tn && tn.state === 'error') p = pill('Tailnet failed', 'bad');
      else if (ls && ls.state === 'held') p = pill('Holds the lease', 'ok');
      else if (tn && tn.state === 'up') p = pill('On the tailnet', 'ok');
      return h('div.card',
        h('div.card-head', h('div.card-title', icon('lock'), h('h2', 'Core')), p),
        h('div.card-body',
          tn ? h('p', 'Tailnet: ', tn.state === 'up' && tn.url ? mono(tn.url, { copy: true }) : h('span.muted', tn.state === 'starting' ? `joining as ${tn.hostname}…` : tn.state)) : null,
          tn && tn.error ? h('p.error-text.small', tn.error) : null,
          ls && ls.state === 'lost'
            ? h('p.error-text.small', `The core lease was lost${ls.holder ? ` to ${ls.holder}` : ''}: WhatsApp and outbound sending are stopped on this machine.`)
            : null,
          ls && ls.state === 'unverified'
            ? h('p.error-text.small', `The lease store does not answer${ls.error ? ` (${ls.error})` : ''}. WhatsApp keeps running: this device held the lease last, and it stops only if another device holds it.`)
            : null,
          ls && ls.state !== 'lost' ? h('p.muted', 'Lease: device ', mono(ls.device_id || ''), ls.renewed_at ? [', renewed ', timeAgo(ls.renewed_at)] : null) : null));
    });

    // Whose backup failed: the bridge's settings, or a brain by its name.
    /** @param {Status} s */
    const failedOwner = (s) => {
      const id = (s.backup || {}).last_error_brain;
      return id ? backupOwner(id, s.brains) : '';
    };
    /** @param {Status} s */
    const renderBackup = (s) => paint(backupCard, { ...(s.backup || {}), owner: failedOwner(s) }, (b) => {
      let p;
      // Daily backups that lost their Drive link would otherwise just stop.
      const stranded = !b.connected && b.enabled;
      if (stranded) p = pill('Not connected', 'bad');
      else if (!b.connected) p = pill('Not set up', 'neutral');
      else if (b.running) p = pill(b.restoring ? 'Restoring…' : 'Backing up…', 'info', { pulse: true });
      else if (b.last_error && !b.last_ok) p = pill('Failing', 'bad');
      else if (b.enabled) p = pill('Daily', 'ok');
      else p = pill('Manual only', 'warn');
      return [
        h('div.card-head', h('div.card-title', icon('backup'), h('h2', 'Backup')), p),
        h('div.card-body',
          stranded ? h('p.error-text.small', 'Daily backups are on, but Google Drive is not connected, so none run. Connect it again on the Backup page.') : null,
          !b.connected && !stranded ? h('p.muted', 'Back up your brains to Google Drive every day.') : null,
          b.connected ? h('p', 'Last good backup: ', b.last_ok ? timeAgo(b.last_ok) : h('span.muted', 'none yet')) : null,
          b.connected && b.next_run ? h('p.muted', 'Next: ', timeAgo(b.next_run)) : null,
          b.last_error ? h('p.error-text.small', b.owner ? `${b.owner}: ${b.last_error}` : b.last_error) : null,
          h('a.small', { href: '#/backup' }, b.connected || stranded ? 'Backup settings' : 'Set up backups')),
      ];
    });

    // The housekeeping job: its last report, what it would stop or did,
    // and a dry run on demand.
    const hkResource = resource('/api/housekeeping');
    let hkBusy = false;
    /** @param {HousekeepingFinding} f */
    const findingLine = (f) => h('li.small',
      h('strong', f.kind), ' ',
      f.pid ? mono(`PID ${f.pid}`) : (f.target ? mono(f.target) : null), ' ',
      f.name ? h('span', f.name, ' ') : null,
      f.members && f.members.length > 1 ? h('span.muted', `+${f.members.length - 1} `) : null,
      (f.cpu_percent || f.rss) ? h('span.muted', `${Math.round(f.cpu_percent || 0)}% CPU, ${fmtBytes(f.rss || 0)} `) : null,
      f.age_seconds ? h('span.muted', `up ${fmtDuration(f.age_seconds)} `) : null,
      f.owner ? h('span.muted', `(${f.owner}) `) : null,
      h('span', '— ', f.reason),
      f.outcome ? [' ', badge(f.outcome, f.problem ? 'bad' : (f.outcome === 'stopped' || f.outcome === 'removed' ? 'ok' : 'neutral'))] : null);
    /** @param {HousekeepingStatus | undefined} v */
    const renderHousekeeping = (v) => paint(housekeepingCard, { ...(v || {}), busy: hkBusy }, (x) => {
      const last = x.last;
      const c = /** @type {Partial<HousekeepingCounts>} */ (x.counts || {});
      let p;
      if (x.busy || x.running) p = pill('Running…', 'info', { pulse: true });
      else if (last && c.problems) p = pill(plural(c.problems, 'problem'), 'bad');
      else if (!x.enabled) p = pill('Off', 'neutral');
      else if (x.dry_run) p = pill('Dry run', 'warn', { title: 'housekeeping.dry_run: it reports, and stops nothing' });
      else p = pill(`Every ${x.every_hours} h`, 'ok');
      const findings = last ? last.findings || [] : [];
      const runNow = btn('Check now', {
        small: true, kind: 'ghost', icon: 'refresh', disabled: x.busy || x.running,
        onClick: async () => {
          hkBusy = true;
          renderHousekeeping(hkResource.data());
          try {
            await post('/api/housekeeping/run', { dry_run: true }, { timeout: 300000 });
            toast('Housekeeping checked (dry run)', 'info');
          } finally {
            hkBusy = false;
            await hkResource.refresh();
            renderHousekeeping(hkResource.data());
          }
        },
      });
      return [
        h('div.card-head', h('div.card-title', icon('shield'), h('h2', 'Housekeeping')), p),
        h('div.card-body',
          last
            ? h('p', 'Last run ', timeAgo(last.started), ` (${last.trigger}${last.dry_run ? ', dry run' : ''}): `, x.summary || '')
            : h('p.muted', 'No run yet. It looks for processes the bridge and its runs left behind, and for stale locks.'),
          last && last.note ? h('p.muted.small', last.note) : null,
          findings.length ? h('ul.plain-list', findings.slice(0, 8).map(findingLine)) : null,
          findings.length > 8 ? h('p.muted.small', `and ${findings.length - 8} more: fridolin housekeeping status lists them all`) : null,
          last && (last.errors || []).length ? last.errors.map((e) => h('p.error-text.small', e)) : null,
          (x.asked || []).length ? h('p.small', plural(x.asked.length, 'process'), ' waiting for your answer on the ', h('a', { href: '#/approvals' }, 'Approvals'), ' page') : null,
          x.enabled && x.next_run ? h('p.muted.small', 'Next run ', timeAgo(x.next_run)) : null,
          x.dry_run ? h('p.muted.small', 'Dry run: set housekeeping.dry_run to false in config.json once the reports look right.') : null,
          h('div.actions', runNow)),
      ];
    });
    ctx.follow(hkResource, renderHousekeeping, 60000);

    // Which brain is the fallback, and whether a brain handles DMs, is in
    // /api/brains rather than the status, and seldom changes.
    /** @type {Brain[] | null} */
    let configured = null;
    /** @param {Status} s */
    const renderBrains = (s) => {
      const list = s.brains || [];
      // The first-brain card above says what to do then.
      if (!list.length) {
        paint(brainsBox, 'none', () => empty('No brains yet', 'Add a folder as a brain to start answering messages.'));
        return;
      }
      paint(brainsBox, 'list', () => brainList);
      const noDMBrain = !!configured && !hasDMBrain(configured);
      const rows = list.map((b) => ({ b, fallback: !!configured && configured.some((x) => x.fallback && x.id === b.id), noDMBrain }));
      reconcile(brainList, rows, ({ b }) => b.id, ({ b, ...where }) => h('a.brain-row.card.link-card', { href: `#/brains/${encodeURIComponent(b.id)}` },
        h('div.brain-row-main',
          h('div.brain-row-title', h('strong', b.name || b.id), h('code.mono.muted', b.id)),
          h('div.muted.small', routingSummary(b.routing, where))),
        h('div.brain-row-state', runtimeSummary(b)),
        icon('right', 'row-chevron')));
    };
    // ── scheduled agents waiting for approval ──
    ctx.follow(schedulesResource, (d) => {
      const pending = ((d && d.agents) || []).filter((a) => a.review && a.review.status !== 'invalid');
      paint(agentsNote, pending.map((a) => `${a.brain}/${a.name}`).join(','), () => (pending.length
        ? callout('warn', `${plural(pending.length, 'scheduled agent')} waiting for your approval`,
          h('div', pending.map((a) => a.name).join(', '), ': written or changed outside the dashboard, they run only once you approve them.'),
          h('div', h('a', { href: '#/schedules' }, 'Review & approve on the Schedules page')))
        : null));
    }, 30000);

    ctx.follow(brainsResource, (l) => {
      configured = Array.isArray(l) ? l : [];
      const s = store.get().status;
      if (s) renderBrains(s);
    }, 60000);

    /** @param {Status} s */
    const renderAll = (s) => {
      renderHealth(s);
      renderCTA(s);
      renderAlerts(s);
      renderWA(s);
      renderClaude(s);
      renderSandbox(s);
      renderCore(s);
      renderBackup(s);
      renderBrains(s);
    };

    // Status comes from the app-wide poller; this page only subscribes.
    if (!store.get().status) mount(header, spinner());
    ctx.watch(store, (s) => s.status, (s) => { if (s) renderAll(s); });

    // QR refresh while pairing (codes rotate roughly every 20 s).
    ctx.poll(async (signal) => {
      if (qr.img) await loadQR(signal);
    }, 3000);

    /** @type {ActivityEntry[] | null} */
    let items = null;
    const paintActivity = () => {
      if (!items) return;
      if (!items.length) {
        paint(activityBox, 'none', () => empty('No messages yet', 'Messages your brains receive and send show up here.'));
        return;
      }
      paint(activityBox, 'list', () => null);
      showActivity(activityBox, items, { compact: true });
    };
    ctx.follow(resource(`/api/activity${qs({ limit: 10 })}`), (data) => {
      items = data || [];
      paintActivity();
    }, 3000);
    // Chat labels show group names once the groups have loaded.
    ctx.watch(store, (s) => s.names, paintActivity);
  },
});
