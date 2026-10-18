// Integrations: services that feed brains. Plaud recorder for now: connect
// with Plaud's own sign-in, pick the brain that receives recordings, and see
// what was handed over last.

import { post, put } from '../app/api.js';
import { brainsResource, resource } from '../app/resources.js';
import { h, mount, paint } from '../lib/dom.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { field, toggle, select, nextId } from '../components/fields.js';
import { pill, callout, timeAgo, mono, pageHeader, loading } from '../components/common.js';
import { chatEl, chatName } from '../components/chat.js';
import { liveForm } from '../components/live-form.js';

const every = (sec) => (sec % 60 === 0 ? (sec === 60 ? 'minute' : `${sec / 60} minutes`) : `${sec} seconds`);

export default {
  title: 'Integrations',
  render(root, ctx) {
    const card = h('section.card', loading('Checking Plaud…'));
    mount(root,
      pageHeader('Integrations', 'Services that hand things to your brains on their own.'),
      h('div.page-stack', card));

    // It asks Plaud for the account, which may take 30 s.
    const plaud = resource('/api/plaud/status', { timeout: 60000 });
    const refresh = () => plaud.refresh();
    let status = null;
    let brains = [];

    // While connected the card keeps these parts, so the settings keep
    // their edits while the status around them refreshes.
    const headBox = h('div');
    const alertsBox = h('div.stack');
    const factsBox = h('div');
    const actionsBox = h('div.actions');
    const loginBox = h('p.muted.small');
    const settings = liveForm({ build: buildSettings, edits: (d) => d.st });
    ctx.dirty = () => settings.dirty();

    const head = (state, kind) => h('div.card-head',
      h('div.card-title', icon('inbox'), h('h2', 'Plaud recorder')),
      pill(state, kind));

    // A status known from an earlier visit paints at once, so everything
    // render() uses is defined above.
    ctx.follow(brainsResource, (list) => { brains = list || []; if (status) render(); }, 60000);
    ctx.follow(plaud, (s) => { status = s; render(); }, 4000);

    function render() {
      const s = status;
      if (!s.cli.found) {
        paint(card, { install: s.cli.install_hint }, () => [
          head('Not installed', 'neutral'),
          h('p', 'New recordings from your Plaud reach a brain as finished transcripts — with Plaud’s AI summary — so it can file them, remember what matters and tell you the action items.'),
          callout('info', 'Install Plaud’s command-line tool first',
            h('div', 'In a terminal: ', mono(s.cli.install_hint, { copy: true }), ' (needs Node.js). Then check again.')),
          h('div.actions', btn('Check again', { icon: 'refresh', onClick: () => refresh() })),
        ]);
        return;
      }
      if (!s.connected) {
        if (settings.dirty()) settings.discard(); // nothing to edit until it is connected again
        const connecting = s.connect && s.connect.state === 'running';
        paint(card, { connect: s.connect || null }, ({ connect }) => [
          head(connecting ? 'Signing in…' : 'Not connected', connecting ? 'warn' : 'neutral'),
          h('p', 'Sign in with your Plaud account. New recordings then reach the brain you choose as finished transcripts, with Plaud’s AI summary.'),
          connecting ? callout('info', 'Approve access in the browser tab Plaud just opened.',
            connect.auth_url ? h('div', 'No tab opened? ', h('a', { href: connect.auth_url, target: '_blank', rel: 'noopener noreferrer' }, 'Open Plaud’s sign-in')) : null) : null,
          connect && connect.state === 'error' ? callout('bad', 'Sign-in did not finish', connect.error) : null,
          h('p.muted.small', 'Fridolin keeps the Plaud login in its own data folder, out of the brains’ reach. Plaud’s tool runs with its usage statistics turned off.'),
          h('div.actions', btn(connecting ? 'Waiting for Plaud…' : 'Connect Plaud', {
            kind: 'primary', icon: 'external', disabled: connecting,
            onClick: async () => { await post('/api/plaud/connect'); toast('Plaud’s sign-in opened in your browser', 'ok'); await refresh(); },
          })),
        ]);
        return;
      }

      paint(card, 'connected', () => [headBox, alertsBox, factsBox, settings.el, actionsBox, loginBox]);
      const st = s.settings || {};
      paint(headBox, { expired: !!s.expired, account: s.account || '' }, ({ expired, account }) => [
        head(expired ? 'Login expired' : 'Connected', expired ? 'bad' : 'ok'),
        account ? h('p', 'Signed in as ', h('strong', account), '.') : null,
      ]);
      // A destination problem only blocks anything while Plaud is on.
      const blocked = st.enabled && s.summary_error;
      paint(alertsBox, { expired: !!s.expired, blocked: blocked || '', lastError: s.last_error && !s.expired && s.last_error !== s.summary_error ? s.last_error : '' }, (a) => [
        a.expired ? callout('bad', 'Plaud’s login expired', 'Recordings are not arriving. Connect again — it takes one approval in the browser.') : null,
        a.blocked ? callout('bad', 'Recordings cannot be delivered', a.blocked) : null,
        a.lastError ? callout('warn', 'Last check failed', a.lastError) : null,
      ]);
      paint(factsBox, {
        chat: s.summary_chat || '', chatName: chatName(s.summary_chat || ''), toYou: !!s.summary_to_you, chatBrain: s.summary_chat_brain || '', target: s.target_brain || '',
        last: s.last_forwarded || null, poll: s.last_poll || '', pending: s.pending || 0, waiting: !!(blocked || s.last_error), seen: s.seen,
      }, renderFacts);
      // The picked brain is marked when it cannot take recordings: gone,
      // or listed but not running (not approved, or it has an issue).
      const missing = (label) => (s.brain_missing ? `${label} (missing)` : label);
      const options = [['', `Automatic (${s.target_brain || 'the DM brain'})`], ...brains.map((b) => [b.id, b.id === st.brain ? missing(b.name || b.id) : b.name || b.id])];
      // The picked brain keeps its option while it is gone (or the list is
      // still loading), so Save does not quietly switch to Automatic.
      if (st.brain && !brains.some((b) => b.id === st.brain)) options.push([st.brain, missing(st.brain)]);
      settings.set({ st, options });
      paint(actionsBox, { enabled: !!st.enabled, expired: !!s.expired }, renderActions);
      paint(loginBox, s.login_dir || '', (dir) => ['Login stored in ', mono(dir), '.']);
    }

    function renderFacts(f) {
      const facts = [];
      if (f.chat) {
        facts.push(h('div', 'Summaries go to ', chatEl(f.chat), f.toYou ? ' (your DM)' : '', '.'));
        if (f.chatBrain) facts.push(h('div.muted.small', `What you write back in that chat goes to the ${f.chatBrain} brain, not to ${f.target}.`));
      }
      if (f.last) facts.push(h('div', 'Last recording: ', h('strong', f.last.name), ' → ', f.last.brain, ' ', timeAgo(f.last.at)));
      if (f.poll) facts.push(h('div.muted', 'Checked ', timeAgo(f.poll), f.pending ? ` · ${f.pending} ${f.waiting ? 'waiting to be delivered' : 'waiting for Plaud’s transcript'}` : ''));
      if (typeof f.seen === 'number') facts.push(h('div.muted.small', f.seen === 1 ? '1 recording known' : `${f.seen} recordings known`));
      return facts.length ? h('div.facts', facts) : null;
    }

    function buildSettings({ st, options }) {
      const enabled = toggle({ checked: !!st.enabled, label: 'Send new recordings to a brain' });
      const brainSel = select(options, st.brain || '');
      const polished = toggle({ checked: !!st.polished, label: 'Use Plaud’s AI-cleaned transcript' });
      const access = toggle({ checked: !!st.brain_access, label: 'Let brains use the Plaud CLI themselves' });
      const msg = h('textarea', { id: nextId('pmsg'), rows: 4, placeholder: 'Built-in instruction: file it under transcripts/, note what matters, reply with a summary and action items. {name}, {when} and {duration} are filled in.' });
      msg.value = st.message || '';
      const value = () => ({
        enabled: enabled.input.checked, brain: brainSel.value, polished: polished.input.checked,
        brain_access: access.input.checked, message: msg.value,
      });
      const el = h('div.settings-fields',
        field({ control: enabled, help: `Every ${every(st.interval_seconds || 60)} Fridolin looks for new recordings and waits until Plaud has transcribed them. The first check — also the first after signing in with another Plaud account — only remembers what already exists, so old recordings are not sent. Recordings made while this is off are sent when you turn it back on (up to the latest 20).` }),
        field({ label: 'Brain', control: brainSel, help: 'Recordings run in this brain. Its answer is private: it goes to the brain’s reply chat when that is a direct chat, otherwise to your DM — never to a group.' }),
        field({ control: polished }),
        field({ control: access, warn: access.input.checked ? 'Brains can read the Plaud login while this is on.' : null, help: h('span', 'On: the login lives in ', mono('~/.plaud/'), ' and brains may run ', mono('plaud search'), ', ', mono('plaud transcript'), ' … themselves. Off: only Fridolin can use it.') }),
        field({ label: 'Instruction sent with each recording', control: msg, wide: true }));
      return { el, value };
    }

    function renderActions({ enabled, expired }) {
      return [
        btn('Save', {
          kind: 'primary', icon: 'check',
          onClick: async () => {
            await put('/api/plaud/settings', settings.form().value());
            settings.saved();
            toast('Plaud settings saved', 'ok');
            await refresh();
          },
        }),
        // A check hands new recordings over, so it is offered only while that is on.
        enabled ? btn('Check now', {
          icon: 'refresh',
          onClick: async () => {
            const r = await post('/api/plaud/sync');
            toast(r && r.status === 'already checking' ? 'Already checking Plaud…' : 'Checking Plaud…', 'ok');
            ctx.timeout(() => refresh(), 3000);
          },
        }) : null,
        expired ? btn('Connect again', { icon: 'external', onClick: async () => { await post('/api/plaud/connect'); await refresh(); } }) : null,
        btn('Disconnect', {
          kind: 'danger', icon: 'x', small: true,
          onClick: async () => {
            const ok = await confirmDialog({ title: 'Disconnect Plaud?', message: 'Fridolin signs out of Plaud and stops checking for recordings. Your recordings stay in Plaud.', confirm: 'Disconnect', danger: true });
            if (!ok) return;
            // Plaud's logout may take the bridge's 30 s.
            await post('/api/plaud/disconnect', undefined, { timeout: 60000 });
            await refresh();
          },
        }),
      ];
    }
  },
};
