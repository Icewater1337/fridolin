// Overview: health, WhatsApp pairing, Claude CLI, sandbox, brains, backup,
// warnings and the latest messages.

import { get, post, qs } from '../app/api.js';
import { store } from '../app/store.js';
import { h, mount } from '../lib/dom.js';
import { fmtDuration, plural, parseTs } from '../lib/format.js';
import { phone } from '../lib/jid.js';
import { waKind, waLabel } from '../lib/whatsapp.js';
import { routingSummary } from '../lib/brain.js';
import { changed } from '../lib/memo.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { pill, badge, callout, timeAgo, mono, empty, spinner } from '../components/common.js';
import { outcomePill } from '../components/brain.js';
import { activityItem } from '../components/activity-item.js';

export default {
  title: 'Overview',
  render(root, ctx) {
    const header = h('div.health');
    const cta = h('div');
    const alerts = h('div.alerts');
    const waCard = h('div.card.wa-card');
    const claudeCard = h('div.card');
    const sandboxCard = h('div.card');
    const backupCard = h('div.card');
    const brainsBox = h('div');
    const activityBox = h('div.activity-list.compact');
    const memo = {};

    mount(root,
      h('header.page-header', h('div.page-heading', h('h1', 'Overview'), header)),
      cta,
      alerts,
      waCard,
      h('div.grid.three', claudeCard, sandboxCard, backupCard),
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

    const renderWA = (s) => {
      const wa = s.whatsapp || {};
      const key = { mode: wa.mode, own: wa.own_number, detail: wa.detail, qr: wa.qr_available, since: wa.since, dev: s.dev_mode };
      if (!changed(memo, 'wa', key)) return;
      const kind = waKind(wa.mode);
      const notPaired = !wa.own_number;
      const canPair = !s.dev_mode && (wa.mode === 'qr-expired' || (notPaired && !['pairing', 'connecting', 'connected', 'disabled'].includes(wa.mode)));
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
        wa.detail && !s.dev_mode ? h('p.muted', wa.detail) : null,
        parseTs(wa.since) ? h('p.muted.small', timeAgo(wa.since, 'Since ')) : null,
        s.dev_mode ? h('p.muted', 'Dev mode: nothing is sent to WhatsApp. Use the ', h('a', { href: '#/dev' }, 'Dev page'), ' to simulate messages.') : null,
        pairBtn ? h('div.actions', pairBtn) : null);
      if (wa.qr_available) {
        qr.img = h('img.qr-img', { alt: 'WhatsApp pairing QR code', width: 280, height: 280 });
        if (qr.url) qr.img.src = qr.url;
        qr.age = h('div.muted.small');
        mount(waCard,
          head,
          h('div.qr-layout',
            h('div.qr-frame', qr.img, qr.age),
            h('div.qr-steps',
              h('h3', 'Link this bridge to WhatsApp'),
              h('ol',
                h('li', 'Open WhatsApp on the phone that holds the bridge’s account.'),
                h('li', 'Go to ', h('strong', 'Settings → Linked Devices → Link a Device'), '.'),
                h('li', 'Point the camera at this code.')),
              h('p.muted.small', 'The code changes every few seconds — this page keeps it fresh. Use a second number if you can: the bridge answers as whichever account it is linked to.'))));
        loadQR(ctx.signal).catch(() => {});
      } else {
        qr.img = null;
        qr.url = '';
        mount(waCard, head, facts);
      }
      waCard.classList.toggle('pairing', !!wa.qr_available);
    };

    // ── health header ──
    const renderHealth = (s) => {
      const key = { v: s.version, dev: s.dev_mode, up: Math.floor((s.uptime_s || 0) / 60), plat: s.platform, deb: s.debouncing, ing: s.ingest };
      if (!changed(memo, 'health', key)) return;
      mount(header,
        pill('Running', 'ok'),
        h('span.health-item', { title: s.started_at ? `Started ${new Date(s.started_at).toLocaleString()}` : '' }, icon('clock'), `up ${fmtDuration(s.uptime_s || 0)}`),
        s.version ? h('span.health-item.mono', s.version) : null,
        s.dev_mode ? badge('Dev mode', 'warn', 'Started with --no-whatsapp') : null,
        s.debouncing ? h('span.health-item', plural(s.debouncing, 'message'), ' gathering') : null);
    };

    // ── first run ──
    const renderCTA = (s) => {
      const show = (s.brains || []).length === 0 && (s.issues || []).length === 0;
      if (!changed(memo, 'cta', { show })) return;
      if (!show) { mount(cta); return; }
      mount(cta, h('div.card.hero',
        h('div.hero-icon', icon('sparkle')),
        h('div.hero-body',
          h('h2', 'Set up your first brain'),
          h('p', 'A brain is a folder on this computer with its own instructions, memory and conversation history. Pick or create a folder, choose which chats it answers, and you are ready to go.'),
          h('div.actions', btn('Add a brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' })))));
    };

    // ── warnings and brain issues ──
    const renderAlerts = (s) => {
      const warnings = s.warnings || [];
      const issues = s.issues || [];
      if (!changed(memo, 'alerts', { warnings, issues })) return;
      mount(alerts,
        warnings.map((w) => callout('warn', null, w)),
        issues.map((is) => {
          const label = { drift: 'brain.json changed on disk', missing: 'Brain folder missing', invalid: 'Brain configuration invalid', unapproved: 'Brain not approved' }[is.kind] || is.kind;
          const link = is.id ? h('a', { href: `#/brains/${encodeURIComponent(is.id)}` }, is.kind === 'drift' ? 'Review and approve' : 'Open brain') : null;
          return callout(is.kind === 'drift' ? 'warn' : 'bad', `${label}${is.id ? `: ${is.id}` : ''}`,
            h('div', is.detail), h('div.muted.small', mono(is.path)), link ? h('div', link) : null);
        }));
    };

    // ── cards ──
    const renderClaude = (s) => {
      const c = s.claude || {};
      if (!changed(memo, 'claude', c)) return;
      mount(claudeCard,
        h('div.card-head', h('div.card-title', icon('terminal'), h('h2', 'Claude CLI')), c.found ? pill('Found', 'ok') : pill('Not found', 'bad')),
        h('div.card-body',
          c.path ? h('p', mono(c.path)) : null,
          c.version ? h('p.muted', c.version) : null,
          !c.found ? h('p.muted', 'Install Claude Code and log in once with ', h('code.mono', 'claude'), ', or point ', h('a', { href: '#/settings/claude' }, 'Settings → Claude'), ' at the right binary.') : null));
    };

    const renderSandbox = (s) => {
      const sb = s.sandbox || {};
      if (!changed(memo, 'sandbox', sb)) return;
      let p;
      let text;
      if (!sb.supported) { p = pill('Not available', 'neutral'); text = 'The sandbox uses macOS sandbox-exec. On this system, run the bridge as a separate user instead.'; } else if (sb.enabled) { p = pill('On', 'ok'); text = 'Brains cannot read other brains’ folders, your keys, mail or documents.'; } else { p = pill('Off', 'warn'); text = 'Brains can read everything your user account can. Turn the sandbox on in Settings.'; }
      mount(sandboxCard,
        h('div.card-head', h('div.card-title', icon('shield'), h('h2', 'Sandbox')), p),
        h('div.card-body', h('p.muted', text), sb.supported ? h('a.small', { href: '#/settings/sandbox' }, 'Sandbox settings') : null));
    };

    const renderBackup = (s) => {
      const b = s.backup || {};
      if (!changed(memo, 'backup', b)) return;
      let p;
      if (!b.connected) p = pill('Not set up', 'neutral');
      else if (b.running) p = pill('Backing up…', 'info', { pulse: true });
      else if (b.last_error && !b.last_ok) p = pill('Failing', 'bad');
      else if (b.enabled) p = pill('Daily', 'ok');
      else p = pill('Manual only', 'warn');
      mount(backupCard,
        h('div.card-head', h('div.card-title', icon('backup'), h('h2', 'Backup')), p),
        h('div.card-body',
          !b.connected ? h('p.muted', 'Back up your brains to Google Drive every day.') : null,
          b.connected ? h('p', 'Last good backup: ', b.last_ok ? timeAgo(b.last_ok) : h('span.muted', 'none yet')) : null,
          b.connected && b.next_run ? h('p.muted', 'Next: ', timeAgo(b.next_run)) : null,
          b.last_error ? h('p.error-text.small', b.last_error) : null,
          h('a.small', { href: '#/backup' }, b.connected ? 'Backup settings' : 'Set up backups')));
    };

    const renderBrains = (s) => {
      const list = s.brains || [];
      if (!changed(memo, 'brains', list)) return;
      if (!list.length) {
        mount(brainsBox, empty('No brains yet', 'Add a folder as a brain to start answering messages.', btn('Add a brain', { href: '#/add-brain', kind: 'primary', icon: 'plus' })));
        return;
      }
      mount(brainsBox, h('div.brain-list', list.map((b) => {
        const running = b.running;
        const q = (b.queue || []).length;
        return h('a.brain-row.card.link-card', { href: `#/brains/${encodeURIComponent(b.id)}` },
          h('div.brain-row-main',
            h('div.brain-row-title', h('strong', b.name || b.id), h('code.mono.muted', b.id)),
            h('div.muted.small', routingSummary(b.routing))),
          h('div.brain-row-state',
            running ? pill('Working', 'info', { pulse: true, title: running.preview || '' }) : pill('Idle', 'neutral'),
            q ? badge(`${q} queued`, 'warn') : null,
            h('span.brain-row-last', outcomePill(b.last), b.last ? h('span.muted.small', timeAgo(b.last.finished)) : null)),
          icon('right', 'row-chevron'));
      })));
    };

    const renderAll = (s) => {
      renderHealth(s);
      renderCTA(s);
      renderAlerts(s);
      renderWA(s);
      renderClaude(s);
      renderSandbox(s);
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

    let items = null;
    const paintActivity = () => {
      if (!items) return;
      if (!items.length) {
        mount(activityBox, empty('No messages yet', 'Messages your brains receive and send show up here.'));
        return;
      }
      mount(activityBox, items.map((e) => activityItem(e, { compact: true })));
    };
    ctx.poll(async (signal) => {
      const data = await get(`/api/activity${qs({ limit: 10 })}`, { signal });
      if (!changed(memo, 'activity', data)) return;
      items = data;
      paintActivity();
    }, 3000);
    // Chat labels show group names once the groups have loaded.
    ctx.watch(store, (s) => s.names, paintActivity);
  },
};
