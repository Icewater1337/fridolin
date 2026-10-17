// Integrations: services that feed brains. Plaud recorder for now: connect
// with Plaud's own sign-in, pick the brain that receives recordings, and see
// what was handed over last.

import { get, post, put } from './api.js';
import {
  h, mount, icon, pill, btn, callout, timeAgo, mono, field, toggle, select, pageHeader,
  confirmDialog, toast, loading, nextId, chatEl,
} from './ui.js';

const every = (sec) => (sec % 60 === 0 ? (sec === 60 ? 'minute' : `${sec / 60} minutes`) : `${sec} seconds`);

export default {
  title: 'Integrations',
  render(root, ctx) {
    const card = h('section.card', loading('Checking Plaud…'));
    mount(root,
      pageHeader('Integrations', 'Services that hand things to your brains on their own.'),
      h('div.page-stack', card));

    let brains = [];
    let formDirty = false;
    ctx.dirty = () => formDirty;
    get('/api/brains').then((b) => { brains = b; }).catch(() => {});

    const poller = ctx.poll(async () => {
      if (formDirty) return;
      const s = await get('/api/plaud/status');
      render(s);
    }, 4000);

    function render(s) {
      const head = (state, kind) => h('div.card-head',
        h('div.card-title', icon('inbox'), h('h2', 'Plaud recorder')),
        pill(state, kind));

      if (!s.cli.found) {
        mount(card, head('Not installed', 'neutral'),
          h('p', 'New recordings from your Plaud reach a brain as finished transcripts — with Plaud’s AI summary — so it can file them, remember what matters and tell you the action items.'),
          callout('info', 'Install Plaud’s command-line tool first',
            h('div', 'In a terminal: ', mono(s.cli.install_hint, { copy: true }), ' (needs Node.js). Then check again.')),
          h('div.actions', btn('Check again', { icon: 'refresh', onClick: () => poller.kick() })));
        return;
      }

      const connecting = s.connect && s.connect.state === 'running';
      if (!s.connected) {
        mount(card, head(connecting ? 'Signing in…' : 'Not connected', connecting ? 'warn' : 'neutral'),
          h('p', 'Sign in with your Plaud account. New recordings then reach the brain you choose as finished transcripts, with Plaud’s AI summary.'),
          connecting ? callout('info', 'Approve access in the browser tab Plaud just opened.',
            s.connect.auth_url ? h('div', 'No tab opened? ', h('a', { href: s.connect.auth_url, target: '_blank', rel: 'noopener noreferrer' }, 'Open Plaud’s sign-in')) : null) : null,
          s.connect && s.connect.state === 'error' ? callout('bad', 'Sign-in did not finish', s.connect.error) : null,
          h('p.muted.small', 'Fridolin keeps the Plaud login in its own data folder, out of the brains’ reach. Plaud’s tool runs with its usage statistics turned off.'),
          h('div.actions', btn(connecting ? 'Waiting for Plaud…' : 'Connect Plaud', {
            kind: 'primary', icon: 'external', disabled: connecting,
            onClick: async () => { await post('/api/plaud/connect'); toast('Plaud’s sign-in opened in your browser', 'ok'); poller.kick(); },
          })));
        return;
      }

      const st = s.settings || {};
      // The picked brain is marked when it cannot take recordings: gone,
      // or listed but not running (not approved, or it has an issue).
      const missing = (label) => (s.brain_missing ? `${label} (missing)` : label);
      const brainOpts = [['', `Automatic (${s.target_brain || 'the DM brain'})`], ...brains.map((b) => [b.id, b.id === st.brain ? missing(b.name || b.id) : b.name || b.id])];
      // The picked brain keeps its option while it is gone (or the list is
      // still loading), so Save does not quietly switch to Automatic.
      if (st.brain && !brains.some((b) => b.id === st.brain)) brainOpts.push([st.brain, missing(st.brain)]);
      const enabled = toggle({ checked: !!st.enabled, label: 'Send new recordings to a brain', onChange: () => { formDirty = true; } });
      const brainSel = select(brainOpts, st.brain || '');
      brainSel.addEventListener('change', () => { formDirty = true; });
      const polished = toggle({ checked: !!st.polished, label: 'Use Plaud’s AI-cleaned transcript', onChange: () => { formDirty = true; } });
      const access = toggle({ checked: !!st.brain_access, label: 'Let brains use the Plaud CLI themselves', onChange: () => { formDirty = true; } });
      const msgId = nextId('pmsg');
      const msg = h('textarea', { id: msgId, rows: 4, placeholder: 'Built-in instruction: file it under transcripts/, note what matters, reply with a summary and action items. {name}, {when} and {duration} are filled in.' });
      msg.value = st.message || '';
      msg.addEventListener('input', () => { formDirty = true; });

      const save = btn('Save', {
        kind: 'primary', icon: 'check',
        onClick: async () => {
          await put('/api/plaud/settings', {
            enabled: enabled.input.checked, brain: brainSel.value, polished: polished.input.checked,
            brain_access: access.input.checked, message: msg.value,
          });
          formDirty = false;
          toast('Plaud settings saved', 'ok');
          poller.kick();
        },
      });

      // A destination problem only blocks anything while Plaud is on.
      const blocked = st.enabled && s.summary_error;
      const facts = [];
      if (s.summary_chat) {
        facts.push(h('div', 'Summaries go to ', chatEl(s.summary_chat), s.summary_to_you ? ' (your DM)' : '', '.'));
        if (s.summary_chat_brain) facts.push(h('div.muted.small', `What you write back in that chat goes to the ${s.summary_chat_brain} brain, not to ${s.target_brain}.`));
      }
      if (s.last_forwarded) facts.push(h('div', 'Last recording: ', h('strong', s.last_forwarded.name), ' → ', s.last_forwarded.brain, ' ', timeAgo(s.last_forwarded.at)));
      if (s.last_poll) facts.push(h('div.muted', 'Checked ', timeAgo(s.last_poll), s.pending ? ` · ${s.pending} ${blocked || s.last_error ? 'waiting to be delivered' : 'waiting for Plaud’s transcript'}` : ''));
      if (typeof s.seen === 'number') facts.push(h('div.muted.small', s.seen === 1 ? '1 recording known' : `${s.seen} recordings known`));

      mount(card,
        head(s.expired ? 'Login expired' : 'Connected', s.expired ? 'bad' : 'ok'),
        s.account ? h('p', 'Signed in as ', h('strong', s.account), '.') : null,
        s.expired ? callout('bad', 'Plaud’s login expired', 'Recordings are not arriving. Connect again — it takes one approval in the browser.') : null,
        blocked ? callout('bad', 'Recordings cannot be delivered', s.summary_error) : null,
        s.last_error && !s.expired && s.last_error !== s.summary_error ? callout('warn', 'Last check failed', s.last_error) : null,
        facts.length ? h('div.facts', facts) : null,
        h('div.settings-fields',
          field({ control: enabled, help: `Every ${every(st.interval_seconds || 60)} Fridolin looks for new recordings and waits until Plaud has transcribed them. The first check — also the first after signing in with another Plaud account — only remembers what already exists, so old recordings are not sent. Recordings made while this is off are sent when you turn it back on (up to the latest 20).` }),
          field({ label: 'Brain', control: brainSel, help: 'Recordings run in this brain. Its answer is private: it goes to the brain’s reply chat when that is a direct chat, otherwise to your DM — never to a group.' }),
          field({ control: polished }),
          field({ control: access, warn: access.input.checked ? 'Brains can read the Plaud login while this is on.' : null, help: h('span', 'On: the login lives in ', mono('~/.plaud/'), ' and brains may run ', mono('plaud search'), ', ', mono('plaud transcript'), ' … themselves. Off: only Fridolin can use it.') }),
          field({ label: 'Instruction sent with each recording', control: msg, wide: true })),
        h('div.actions',
          save,
          // A check hands new recordings over, so it is offered only while that is on.
          st.enabled ? btn('Check now', {
            icon: 'refresh',
            onClick: async () => {
              const r = await post('/api/plaud/sync');
              toast(r && r.status === 'already checking' ? 'Already checking Plaud…' : 'Checking Plaud…', 'ok');
              setTimeout(() => poller.kick(), 3000);
            },
          }) : null,
          s.expired ? btn('Connect again', { icon: 'external', onClick: async () => { await post('/api/plaud/connect'); poller.kick(); } }) : null,
          btn('Disconnect', {
            kind: 'danger', icon: 'x', small: true,
            onClick: async () => {
              const ok = await confirmDialog({ title: 'Disconnect Plaud?', message: 'Fridolin signs out of Plaud and stops checking for recordings. Your recordings stay in Plaud.', confirm: 'Disconnect', danger: true });
              if (!ok) return;
              await post('/api/plaud/disconnect');
              poller.kick();
            },
          })),
        h('p.muted.small', 'Login stored in ', mono(s.login_dir), '.'));
    }
  },
};

