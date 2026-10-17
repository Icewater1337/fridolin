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

// What a recording's run is up to, as the status names it.
const runState = { handed: 'with the brain', done: 'answered', stopped: 'stopped', failed: 'its run failed, trying again' };

export default {
  title: 'Integrations',
  render(root, ctx) {
    const card = h('section.card', loading('Checking Plaud…'));
    mount(root,
      pageHeader('Integrations', 'Services that hand things to your brains on their own.'),
      h('div.page-stack', card));

    // The status never waits for Plaud: who is signed in is what the
    // bridge asked last.
    const plaud = resource('/api/plaud/status');
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
    // A CLI version Fridolin does not read, and what to install instead.
    const cliWarn = (warning) => (warning ? callout('warn', 'Plaud’s CLI version', warning) : null);

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
        // Plaud's tool deletes a login that expired; Fridolin remembers it
        // had one.
        const signIn = {
          connect: s.connect || null, expired: !!s.expired && !connecting, moved: !!s.login_moved && !connecting,
          loginDir: s.login_dir || '', access: !!(s.settings || {}).brain_access, cliWarning: s.cli.warning || '',
        };
        paint(card, signIn, ({ connect, expired, moved, loginDir, access, cliWarning }) => [
          head(connecting ? 'Signing in…' : expired ? 'Login expired' : 'Not connected', connecting ? 'warn' : expired ? 'bad' : 'neutral'),
          expired
            ? callout('bad', 'Plaud’s login expired', 'Recordings are not arriving, and the chat gets a reminder once a day. Connect again — it takes one approval in the browser.')
            : h('p', 'Sign in with your Plaud account. New recordings then reach the brain you choose as finished transcripts, with Plaud’s AI summary.'),
          moved
            ? callout('warn', 'Connect Plaud again', '“Let brains use the Plaud CLI” changed where the login lives, and Fridolin does not take over a login it did not make. Connect again to use the one in ', mono(loginDir), '.')
            : null,
          cliWarn(cliWarning),
          connecting ? callout('info', 'Approve access in the browser tab Plaud just opened.',
            // The bridge passes on only the link Plaud's tool printed for
            // its sign-in; the host shows where it leads.
            connect.auth_url
              ? h('div', 'No tab opened? ', h('a', { href: connect.auth_url, target: '_blank', rel: 'noopener noreferrer' }, 'Open Plaud’s sign-in'), ` (${new URL(connect.auth_url).host})`)
              : h('div', 'Use the tab Plaud’s tool opened in your browser.')) : null,
          connect && connect.state === 'error' ? callout('bad', 'Sign-in did not finish', connect.error) : null,
          h('p.muted.small', access
            ? 'The login goes to ~/.plaud/, where brains can use it (“Let brains use the Plaud CLI” is on). Plaud’s tool runs with its usage statistics turned off.'
            : 'Fridolin keeps the Plaud login in its own data folder, out of the brains’ reach. Plaud’s tool runs with its usage statistics turned off.'),
          h('div.actions', btn(connecting ? 'Waiting for Plaud…' : expired ? 'Connect again' : 'Connect Plaud', {
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
      paint(alertsBox, {
        expired: !!s.expired, blocked: blocked || '', lastError: s.last_error && !s.expired && s.last_error !== s.summary_error ? s.last_error : '',
        cliWarning: s.cli.warning || '',
      }, (a) => [
        a.expired ? callout('bad', 'Plaud’s login expired', 'Recordings are not arriving. Connect again — it takes one approval in the browser.') : null,
        a.blocked ? callout('bad', 'Recordings cannot be delivered', a.blocked) : null,
        a.lastError ? callout('warn', 'Last check failed', a.lastError) : null,
        cliWarn(a.cliWarning),
      ]);
      paint(factsBox, {
        chat: s.summary_chat || '', chatName: chatName(s.summary_chat || ''), toYou: !!s.summary_to_you, chatBrain: s.summary_chat_brain || '', target: s.target_brain || '',
        last: s.last_forwarded || null, givenUp: (s.given_up || []).slice(-3).reverse(), handed: s.handed || 0,
        poll: s.last_poll || '', pending: s.pending || 0, waiting: !!(blocked || s.last_error), seen: s.seen, cliVersion: s.cli.version || '',
      }, renderFacts);
      // The picked brain is marked when it cannot take recordings: gone,
      // or listed but not running (not approved, or it has an issue).
      const missing = (label) => (s.brain_missing ? `${label} (missing)` : label);
      const options = [['', `Automatic (${s.target_brain || 'the DM brain'})`], ...brains.map((b) => [b.id, b.id === st.brain ? missing(b.name || b.id) : b.name || b.id])];
      // The picked brain keeps its option while it is gone (or the list is
      // still loading), so Save does not quietly switch to Automatic.
      if (st.brain && !brains.some((b) => b.id === st.brain)) options.push([st.brain, missing(st.brain)]);
      settings.set({ st, options, defaultMessage: s.default_message || '', interval: s.interval_seconds || 60 });
      paint(actionsBox, { enabled: !!st.enabled, expired: !!s.expired, access: !!st.brain_access }, renderActions);
      paint(loginBox, s.login_dir || '', (dir) => ['Login stored in ', mono(dir), '.']);
    }

    function renderFacts(f) {
      const facts = [];
      if (f.chat) {
        facts.push(h('div', 'Summaries go to ', chatEl(f.chat), f.toYou ? ' (your DM)' : '', '.'));
        if (f.chatBrain) facts.push(h('div.muted.small', `What you write back in that chat goes to the ${f.chatBrain} brain, not to ${f.target}.`));
      }
      // "Send again" hands a recording over once more.
      const again = (id) => btn('Send again', {
        small: true, icon: 'refresh',
        onClick: async () => { await post('/api/plaud/resend', { id }); toast('It goes to the brain with the next check', 'ok'); await refresh(); },
      });
      if (f.last) {
        facts.push(h('div', 'Last recording: ', h('strong', f.last.name), ' → ', f.last.brain, ' ', timeAgo(f.last.at),
          runState[f.last.status] ? ` · ${runState[f.last.status]}` : '', f.last.status === 'stopped' ? again(f.last.id) : null));
      }
      // Recordings Fridolin stopped trying, newest first.
      for (const g of f.givenUp) facts.push(h('div.muted', 'Not sent: ', h('strong', g.name || g.id), ` — ${g.reason} `, timeAgo(g.at), again(g.id)));
      if (f.handed) facts.push(h('div.muted', f.handed === 1 ? '1 recording with the brain' : `${f.handed} recordings with the brain`));
      if (f.poll) facts.push(h('div.muted', 'Checked ', timeAgo(f.poll), f.pending ? ` · ${f.pending} ${f.waiting ? 'waiting to be delivered' : 'waiting for Plaud’s transcript'}` : ''));
      if (typeof f.seen === 'number') facts.push(h('div.muted.small', f.seen === 1 ? '1 recording known' : `${f.seen} recordings known`));
      if (f.cliVersion) facts.push(h('div.muted.small', `Plaud CLI ${f.cliVersion}`));
      return facts.length ? h('div.facts', facts) : null;
    }

    function buildSettings({ st, options, defaultMessage, interval }, changed) {
      const enabled = toggle({ checked: !!st.enabled, label: 'Send new recordings to a brain' });
      const brainSel = select(options, st.brain || '');
      const polished = toggle({ checked: !!st.polished, label: 'Use Plaud’s AI-cleaned transcript' });
      const accessWarn = h('div.help.warn-text', 'Brains can read the Plaud login while this is on.');
      const showAccessWarn = () => { accessWarn.hidden = !access.input.checked; };
      // The setting moves the login, so saving it means connecting again.
      const access = toggle({
        checked: !!st.brain_access, label: 'Let brains use the Plaud CLI themselves',
        onChange: async (on, input) => {
          const ok = await confirmDialog({
            title: on ? 'Let brains use the Plaud CLI?' : 'Keep the Plaud login from brains?',
            message: on
              ? 'Plaud’s login then lives in ~/.plaud/, where brains can read it. The login Fridolin has now does not move: after saving, connect Plaud again. If your own terminal is signed in to Plaud, that login is used once you connect.'
              : 'Plaud’s login then lives in Fridolin’s data folder, out of the brains’ reach. The one in ~/.plaud/ stays for your terminal: after saving, connect Plaud again.',
            confirm: on ? 'Turn on' : 'Turn off',
          });
          // Cancelled, the switch is back as it was, and so is the form.
          if (!ok) {
            input.checked = !on;
            changed();
          }
          showAccessWarn();
        },
      });
      showAccessWarn();
      const accessField = field({ control: access, help: h('span', 'On: the login lives in ', mono('~/.plaud/'), ' and brains may run ', mono('plaud search'), ', ', mono('plaud transcript'), ' … themselves. Off: only Fridolin can use it. Either way the login does not move: connect again after changing this.') });
      accessField.append(accessWarn);
      // The built-in instruction comes from the bridge, the one place it is
      // written down.
      const msg = h('textarea', { id: nextId('pmsg'), rows: 4, placeholder: defaultMessage ? `Built in: ${defaultMessage}` : '' });
      msg.value = st.message || '';
      const value = () => ({
        enabled: enabled.input.checked, brain: brainSel.value, polished: polished.input.checked,
        brain_access: access.input.checked, message: msg.value,
      });
      const el = h('div.settings-fields',
        field({ control: enabled, help: `Every ${every(interval)} Fridolin looks for new recordings and waits until Plaud has transcribed them. The first check — also the first after signing in with another Plaud account — only remembers what already exists, so old recordings are not sent. Recordings made while this is off are sent when you turn it back on, five per check, oldest first.` }),
        field({ label: 'Brain', control: brainSel, help: 'Recordings run in this brain. Its answer is private: it goes to the brain’s reply chat when that is a direct chat, otherwise to your DM — never to a group.' }),
        field({ control: polished, help: 'Plaud makes the cleaned-up transcript after the raw one, and not always. When it has none an hour after the recording showed up, the raw one is sent, with a note saying so.' }),
        accessField,
        field({ label: 'Instruction sent with each recording', control: msg, wide: true, help: h('span', 'It follows a first line that says what arrived. ', mono('{name}'), ', ', mono('{when}'), ' and ', mono('{duration}'), ' are filled in. How a brain files recordings belongs in its ', mono('skills/plaud.md'), '.') }));
      return { el, value };
    }

    function renderActions({ enabled, expired, access }) {
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
            const shared = access
              ? ' The login is the one in ~/.plaud/, so this also signs out the plaud command in your terminal.'
              : '';
            const ok = await confirmDialog({ title: 'Disconnect Plaud?', message: `Fridolin signs out of Plaud and stops checking for recordings.${shared} Your recordings stay in Plaud, and the transcripts already filed stay where they are.`, confirm: 'Disconnect', danger: true });
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
