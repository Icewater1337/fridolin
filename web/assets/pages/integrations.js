// Integrations: services that feed brains and that runs reach. GitHub
// (components/github-card.js), and the Plaud recorder: connect with Plaud's
// own sign-in, pick the brain that receives recordings, and see what was
// handed over last.

import { del, download, post, put } from '../app/api.js';
import { brainsResource, resource } from '../app/resources.js';
import { h, mount, paint } from '../lib/dom.js';
import { brainLabel } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { confirmDialog } from '../components/dialog.js';
import { field, toggle, select, nextId } from '../components/fields.js';
import { pill, callout, timeAgo, mono, pageHeader, loading } from '../components/common.js';
import { chatEl, chatName } from '../components/chat.js';
import { liveForm } from '../components/live-form.js';
import { githubCard } from '../components/github-card.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Brain, PlaudAppBuild, PlaudConnectJob, PlaudForward, PlaudGivenUp, PlaudPhone, PlaudSettings, PlaudStatus } from '../../types/api.js' */

/**
 * What the connected card's facts show, from the status.
 * @typedef {object} PlaudFacts
 * @property {string} chat
 * @property {string} chatName
 * @property {boolean} toYou
 * @property {string} chatBrain
 * @property {string} target
 * @property {PlaudForward | null} last
 * @property {PlaudGivenUp[]} givenUp
 * @property {number} handed
 * @property {string} poll
 * @property {number} pending
 * @property {boolean} waiting
 * @property {number} [seen]
 * @property {string} cliVersion
 */

/**
 * What the settings form is built from.
 * @typedef {{st: Partial<PlaudSettings>, options: [string, string][], defaultMessage: string, interval: number}} SettingsData
 */

const every = (/** @type {number} */ sec) => (sec % 60 === 0 ? (sec === 60 ? 'minute' : `${sec / 60} minutes`) : `${sec} seconds`);

// What a recording's run is up to, as the status names it.
/** @type {Record<string, string | undefined>} */
const runState = { handed: 'with the brain', done: 'answered', stopped: 'stopped', failed: 'its run failed, trying again' };

export default /** @satisfies {PageModule} */ ({
  title: 'Integrations',
  render(root, ctx) {
    const card = h('section.card', loading('Checking Plaud…'));
    mount(root,
      pageHeader('Integrations', 'Services that hand things to your brains on their own, and that their runs may use.'),
      h('div.page-stack', githubCard(ctx), card));

    // The status never waits for Plaud: who is signed in is what the
    // bridge asked last.
    const plaud = resource('/api/plaud/status');
    const refresh = () => plaud.refresh();
    /** @type {PlaudStatus | null} */
    let status = null;
    /** @type {Brain[]} */
    let brains = [];

    // While connected the card keeps these parts, so the settings keep
    // their edits while the status around them refreshes.
    const headBox = h('div');
    const alertsBox = h('div.stack');
    const factsBox = h('div');
    const actionsBox = h('div.actions');
    const loginBox = h('p.muted.small');
    // The phone app, while it uploads through the site: Plaud's developer
    // keys, the build and the paired phones, each painted on its own so that
    // keys being typed survive a refresh.
    const keysBox = h('div');
    const buildBox = h('div');
    const phonesBox = h('div');
    const appBox = h('div.stack', keysBox, buildBox, phonesBox);
    const settings = liveForm({ build: buildSettings, edits: (d) => d.st });
    ctx.dirty = () => settings.dirty();

    /** @type {(state: string, kind: string) => HTMLElement} */
    const head = (state, kind) => h('div.card-head',
      h('div.card-title', icon('inbox'), h('h2', 'Plaud recorder')),
      pill(state, kind));
    // A CLI version Fridolin does not read, and what to install instead.
    const cliWarn = (/** @type {string} */ warning) => (warning ? callout('warn', 'Plaud’s CLI version', warning) : null);
    // A sign-in under way or one that failed. The connected card shows it
    // only while the login expired, when Connect again can fail and keep
    // the old login: on a login that works, a failed attempt is old news
    // (one from before a sign-in outside the dashboard, say).
    const connectNotes = (/** @type {PlaudConnectJob | null | undefined} */ connect) => [
      connect && connect.state === 'running' ? callout('info', 'Approve access in the browser tab Plaud just opened.',
        // The bridge passes on only the link Plaud's tool printed for
        // its sign-in; the host shows where it leads.
        connect.auth_url
          ? h('div', 'No tab opened? ', h('a', { href: connect.auth_url, target: '_blank', rel: 'noopener noreferrer' }, 'Open Plaud’s sign-in'), ` (${new URL(connect.auth_url).host})`)
          : h('div', 'Use the tab Plaud’s tool opened in your browser.')) : null,
      connect && connect.state === 'error' ? callout('bad', 'Sign-in did not finish', connect.error) : null,
    ];
    // The phone app needs neither Plaud's CLI nor its login.
    const phoneAppButton = () => btn('Use the Plaud Sync phone app instead', {
      icon: 'phone',
      onClick: async () => {
        await put('/api/plaud/settings', { phone_upload: true });
        toast('Set up the phone app on this card', 'ok');
        await refresh();
      },
    });
    const startSignIn = async () => {
      await post('/api/plaud/connect');
      // The sign-in starts now; whether a browser tab opens, the card says.
      toast('Starting Plaud’s sign-in…', 'info');
      await refresh();
    };

    // A status known from an earlier visit paints at once, so everything
    // render() uses is defined above. It is asked for often only while
    // Plaud's sign-in runs, so the card shows at once when it is done;
    // otherwise only a check changes it, at most every 30 s
    // (plaud.interval_seconds).
    const signingIn = () => !!(status && status.connect && status.connect.state === 'running');
    const building = () => !!(status && status.app && status.app.build && status.app.build.state === 'running');
    ctx.follow(brainsResource, (list) => { brains = list || []; if (status) render(); }, 60000);
    ctx.follow(plaud, (s) => { status = s; render(); }, () => (signingIn() || building() ? 2000 : 20000));

    function render() {
      const s = status;
      // Recordings from the Plaud Sync phone app need no CLI and no login:
      // they come from a folder its files are synced to (plaud.phone_dir),
      // or it uploads them through the site (plaud.phone_upload).
      const folder = (s.settings || {}).phone_dir || '';
      const phone = !!(folder || (s.settings || {}).phone_upload);
      if (!s.cli.found && !phone) {
        paint(card, { install: s.cli.install_hint }, () => [
          head('Not installed', 'neutral'),
          h('p', 'New recordings from your Plaud reach a brain as finished transcripts — with Plaud’s AI summary — so it can file them, remember what matters and tell you the action items.'),
          callout('info', 'Install Plaud’s command-line tool first',
            h('div', 'In a terminal: ', mono(s.cli.install_hint, { copy: true }), ' (needs Node.js). Then check again.')),
          h('div.actions', btn('Check again', { icon: 'refresh', onClick: () => refresh() }), phoneAppButton()),
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
            ? callout('bad', 'Plaud’s login expired', 'Recordings are not arriving, and the chat gets a reminder once a day. Connect again — it takes one approval in the browser — or disconnect to stop the reminders.')
            : h('p', 'Sign in with your Plaud account. New recordings then reach the brain you choose as finished transcripts, with Plaud’s AI summary.'),
          moved
            ? callout('warn', 'Connect Plaud again', '“Let brains use the Plaud CLI” changed where the login lives, and Fridolin does not take over a login it did not make. Connect again to use the one in ', mono(loginDir), ', or disconnect.')
            : null,
          cliWarn(cliWarning),
          connectNotes(connect),
          h('p.muted.small', access
            ? 'The login goes to ~/.plaud/, where brains can use it (“Let brains use the Plaud CLI” is on). Plaud’s tool runs with its usage statistics turned off.'
            : 'Fridolin keeps the Plaud login in its own data folder, out of the brains’ reach. Plaud’s tool runs with its usage statistics turned off.'),
          h('div.actions', btn(connecting ? 'Waiting for Plaud…' : expired ? 'Connect again' : 'Connect Plaud', {
            kind: 'primary', icon: 'external', disabled: connecting, onClick: startSignIn,
          }),
          // Fridolin still has a login on record: you may rather stop here.
          expired || moved ? disconnectButton({ connected: false, expired, moved, access }) : phoneAppButton()),
        ]);
        return;
      }

      paint(card, 'connected', () => [headBox, alertsBox, factsBox, settings.el, actionsBox, appBox, loginBox]);
      const st = /** @type {Partial<PlaudSettings>} */ (s.settings || {});
      paint(headBox, { expired: !!s.expired, account: s.account || '', folder, phone }, ({ expired, account }) => [
        head(folder ? 'Phone folder' : phone ? 'Phone app' : expired ? 'Login expired' : 'Connected', expired ? 'bad' : 'ok'),
        folder ? h('p', 'Recordings come from the Plaud Sync phone app, through the folder ', mono(folder), ' its files are synced to (', mono('plaud.phone_dir'), ').')
          : phone ? h('p', 'Recordings come from the Plaud Sync phone app: it takes each one off the NotePin as it stops, has Plaud transcribe it and uploads the transcript through your site, signed with a key only that phone has.') : null,
        account ? h('p', 'Signed in as ', h('strong', account), '.') : null,
      ]);
      // A destination problem only blocks anything while Plaud is on.
      const blocked = st.enabled && s.summary_error;
      paint(alertsBox, {
        expired: !!s.expired, connect: (s.expired && s.connect) || null, blocked: blocked || '',
        lastError: s.last_error && !s.expired && s.last_error !== s.summary_error ? s.last_error : '', cliWarning: s.cli.warning || '',
      }, (a) => [
        connectNotes(a.connect),
        a.expired ? callout('bad', 'Plaud’s login expired', 'Recordings are not arriving. Connect again — it takes one approval in the browser.') : null,
        a.blocked ? callout('bad', 'Recordings cannot be delivered', a.blocked) : null,
        a.lastError ? callout('warn', 'Last check failed', a.lastError) : null,
        cliWarn(a.cliWarning),
      ]);
      paint(factsBox, {
        chat: s.summary_chat || '', chatName: chatName(s.summary_chat || ''), toYou: !!s.summary_to_you, chatBrain: s.summary_chat_brain || '', target: s.target_brain || '',
        last: s.last_forwarded || null, givenUp: (s.given_up || []).slice(-3).reverse(), handed: s.handed || 0,
        poll: s.last_poll || '', pending: s.pending || 0, waiting: !!(blocked || s.last_error), seen: s.seen, cliVersion: phone ? '' : s.cli.version || '',
      }, renderFacts);
      // The picked brain is marked when it cannot take recordings: gone,
      // or listed but not running (not approved, or it has an issue).
      const missing = (/** @type {string} */ label) => (s.brain_missing ? `${label} (missing)` : label);
      const auto = brains.find((b) => b.id === s.auto_brain);
      /** @type {[string, string][]} */
      const options = [['', `Automatic (${auto ? auto.name || auto.id : s.auto_brain || 'the DM brain'})`], ...brains.map((b) => /** @type {[string, string]} */ ([b.id, b.id === st.brain ? missing(brainLabel(b)) : brainLabel(b)]))];
      // The picked brain keeps its option while it is gone (or the list is
      // still loading), so Save does not quietly switch to Automatic.
      if (st.brain && !brains.some((b) => b.id === st.brain)) options.push([st.brain, missing(st.brain)]);
      settings.set({ st, options, defaultMessage: s.default_message || '', interval: s.interval_seconds || 60 });
      paint(actionsBox, { enabled: !!st.enabled, expired: !!s.expired, connecting: !!(s.connect && s.connect.state === 'running'), access: !!st.brain_access, phone }, renderActions);
      paint(loginBox, phone ? '' : s.login_dir || '', (dir) => (dir ? ['Login stored in ', mono(dir), '.'] : null));
      const app = s.app || null;
      appBox.hidden = !app;
      if (app) {
        paint(keysBox, { saved: !!app.credentials }, renderKeys);
        paint(buildBox, { site: app.site_url || '', saved: !!app.credentials, build: app.build || null, built: app.built || '', buildsOn: app.builds_on || '', noBuilder: app.no_builder || '' }, renderBuild);
        paint(phonesBox, app.devices || [], renderPhones);
      }
    }

    /** @param {PlaudFacts} f */
    function renderFacts(f) {
      const facts = [];
      if (f.chat) {
        facts.push(h('div', 'Summaries go to ', chatEl(f.chat), f.toYou ? ' (your DM)' : '', '.'));
        if (f.chatBrain) facts.push(h('div.muted.small', `What you write back in that chat goes to the ${f.chatBrain} brain, not to ${f.target}.`));
      }
      // "Send again" hands a recording over once more.
      /** @param {string} id */
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

    /** @param {{saved: boolean}} keys */
    function renderKeys({ saved }) {
      /** @type {(label: string, help?: string) => HTMLElement} */
      const key = (label, help) => field({ label, help, control: h('input', { type: 'password', id: nextId('pk'), autocomplete: 'off', placeholder: saved ? 'saved — type to replace' : '' }) });
      const fields = [key('Client ID'), key('Client secret'), key('API key', 'App Settings → API Keys: Plaud transcribes with it.')];
      /** @param {HTMLElement} f */
      const value = (f) => f.querySelector('input').value;
      return [
        h('h3', 'Phone app'),
        h('p.muted.small', 'The app is built with your Plaud developer app’s keys (portal.plaud.ai → App Settings). Fridolin keeps them in its data folder and never shows them again. They end up inside the app, so keep the app to yourself.'),
        h('div.settings-fields', fields),
        h('div.actions', btn(saved ? 'Replace keys' : 'Save keys', {
          icon: 'key',
          onClick: async () => {
            const [clientId, clientSecret, apiKey] = fields.map(value);
            await put('/api/plaud/app/credentials', { client_id: clientId, client_secret: clientSecret, api_key: apiKey });
            fields.forEach((f) => { f.querySelector('input').value = ''; });
            toast('Plaud developer keys saved', 'ok');
            await refresh();
          },
        })),
      ];
    }

    // Where the app is built: here, where this computer has a JDK and the
    // Android SDK, else on a node that builds Android apps (android-build).
    /** @param {{site: string, saved: boolean, build: PlaudAppBuild | null, built: string, buildsOn: string, noBuilder: string}} view */
    function renderBuild({ site, saved, build, built, buildsOn, noBuilder }) {
      const running = !!(build && build.state === 'running');
      const expires = build && build.pairing_expires ? ` Its pairing code works once, until ${new Date(build.pairing_expires).toLocaleString()}.` : '';
      const where = build && build.node ? ` on ${build.node}` : '';
      return [
        site ? null : callout('warn', 'The site has no domain yet', 'The app uploads through your site: give it a domain on the Site page first.'),
        !running && noBuilder ? callout('warn', 'Nothing here can build the app',
          h('div', 'This computer has no JDK 17+ and Android SDK, and no node that builds Android apps is online. Join a machine that has them as a node (Nodes → New join code), then approve what it offers there: android-build among it.'),
          h('div.muted.small', noBuilder)) : null,
        build && build.state === 'error' ? callout('bad', `Building the app${where} failed`, h('div', build.error), build.log ? h('pre.small', build.log) : null) : null,
        running && build.stage ? h('p', icon('refresh'), ' ', build.stage, '…') : null,
        built && !running ? h('p', 'Built ', timeAgo(built), where && build.state === 'done' ? where : '', '.', expires) : null,
        !running && buildsOn && buildsOn !== 'this computer' ? h('p.muted.small', 'This computer has no JDK and Android SDK: the node ', h('strong', buildsOn), ' builds the app and sends it back.') : null,
        h('div.actions',
          btn(running ? 'Building… (a few minutes)' : built ? 'Build again' : 'Build phone app', {
            kind: built ? undefined : 'primary', icon: 'phone', disabled: running || !site || !saved || !!noBuilder,
            onClick: async () => {
              await post('/api/plaud/app/build');
              toast('Building the phone app…', 'info');
              await refresh();
            },
          }),
          built && !running ? btn('Download app (APK)', { kind: 'primary', icon: 'download', onClick: () => download('/api/plaud/app.apk', 'plaud-sync.apk') }) : null),
        h('p.muted.small', 'Copy it to the phone and install it, remove the NotePin from Plaud’s app, then tap Pair NotePin in Plaud Sync. On its first start the app makes a key that never leaves the phone and pairs it with Fridolin; build again for another phone.'),
      ];
    }

    /** @param {PlaudPhone[]} devices */
    function renderPhones(devices) {
      if (!devices.length) return h('p.muted', 'No phone is paired yet.');
      /** @param {string} id */
      const unpair = (id) => btn('Unpair', {
        small: true, kind: 'danger', icon: 'x',
        onClick: async () => {
          const ok = await confirmDialog({ title: 'Unpair this phone?', message: 'Fridolin refuses its uploads from now on. To use it again, build the app again and install it over the old one.', confirm: 'Unpair', danger: true });
          if (!ok) return;
          await del(`/api/plaud/devices/${encodeURIComponent(id)}`);
          toast('Phone unpaired', 'ok');
          await refresh();
        },
      });
      return h('div.facts', devices.map((d) => h('div', 'Phone ', mono(d.id), ' paired ', timeAgo(d.paired),
        d.last_upload ? h('span', ' · last transcript ', timeAgo(d.last_upload)) : ' · nothing sent yet', ' ', unpair(d.id))));
    }

    /**
     * @param {SettingsData} data
     * @param {() => void} changed
     */
    function buildSettings({ st, options, defaultMessage, interval }, changed) {
      const enabled = toggle({ checked: !!st.enabled, label: 'Send new recordings to a brain' });
      const upload = toggle({ checked: !!st.phone_upload, label: 'Recordings come from the Plaud Sync phone app, through your site' });
      const brainSel = select(options, st.brain || '');
      const voice = toggle({ checked: !!st.voice, label: 'Voice mode: the phone app may ask by voice and read the answer aloud' });
      const voiceBrainSel = select(options, st.voice_brain || '');
      const voiceQuiet = toggle({ checked: !!st.voice_quiet, label: 'Answers to voice questions go to the phone only, not WhatsApp' });
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
        brain_access: access.input.checked, message: msg.value, phone_upload: upload.input.checked,
        voice: voice.input.checked, voice_brain: voiceBrainSel.value, voice_quiet: voiceQuiet.input.checked,
      });
      // The phone app's transcripts have no cleaned-up version, and no login is shared.
      const phone = !!(st.phone_dir || st.phone_upload);
      const el = h('div.settings-fields',
        field({ control: enabled, help: st.phone_upload && !st.phone_dir
          ? 'Each transcript the phone app uploads goes to the brain at once, at most five with the brain at once. Those that arrive while this is off wait, and are sent when you turn it back on.'
          : phone
          ? `Every ${every(interval)} Fridolin looks in the phone folder for new transcripts. The first check — also the first after the folder changed — only remembers what is already there. Transcripts that arrive while this is off are sent when you turn it back on, oldest first, at most five with the brain at once.`
          : `Every ${every(interval)} Fridolin looks for new recordings and waits until Plaud has transcribed them. The first check — also the first after signing in with another Plaud account — only remembers what already exists, so old recordings are not sent. Recordings made while this is off are sent when you turn it back on, oldest first, at most five with the brain at once.` }),
        field({ label: 'Brain', control: brainSel, help: 'Recordings run in this brain. Its answer is private: it goes to the brain’s reply chat when that is a direct chat, otherwise to your DM — never to a group.' }),
        phone ? null : field({ control: polished, help: 'Plaud makes the cleaned-up transcript after the raw one, and not always. When it has none an hour after the recording showed up, the raw one is sent, with a note saying so.' }),
        phone ? null : accessField,
        // A synced folder (plaud.phone_dir, in config.json) wins over uploads.
        st.phone_dir ? null : field({ control: upload, help: 'On: the app is built further down this card, and Plaud’s cloud, its CLI and your Plaud login are not used. Save, then build the app.' }),
        // Voice mode rides on the app's uploads (docs/PLAUD.md, "Voice mode").
        st.phone_upload && !st.phone_dir ? field({ control: voice, help: 'With the app’s Voice mode switch on, a NotePin recording or a question spoken into the phone goes to the brain below, and the phone reads its answer aloud. The answer is encrypted for the phone: your site only passes it on. The app needs a build from this card made after this was added.' }) : null,
        st.phone_upload && !st.phone_dir && st.voice ? field({ label: 'Brain for voice questions', control: voiceBrainSel, help: 'Default: the brain that gets your DMs. Its answer goes to its private chat too, after the question.' }) : null,
        st.phone_upload && !st.phone_dir && st.voice ? field({ control: voiceQuiet }) : null,
        field({ label: 'Instruction sent with each recording', control: msg, wide: true, help: h('span', 'It follows a first line that says what arrived. ', mono('{name}'), ', ', mono('{when}'), ' and ', mono('{duration}'), ' are filled in. How a brain files recordings belongs in its ', mono('skills/plaud.md'), '.') }));
      return { el, value };
    }

    /** @param {{enabled: boolean, expired: boolean, connecting: boolean, access: boolean, phone: boolean}} view */
    function renderActions({ enabled, expired, connecting, access, phone }) {
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
        expired ? btn(connecting ? 'Waiting for Plaud…' : 'Connect again', { icon: 'external', disabled: connecting, onClick: startSignIn }) : null,
        phone ? null : disconnectButton({ connected: true, expired, moved: false, access }),
      ];
    }

    // Disconnect, on the connected card and on one that waits for you to
    // connect again; what it signs out depends on which.
    /** @param {{connected: boolean, expired: boolean, moved: boolean, access: boolean}} on */
    function disconnectButton({ connected, expired, moved, access }) {
      return btn('Disconnect', {
        kind: 'danger', icon: 'x', small: true,
        onClick: async () => {
          let note = '';
          if (moved) note = ' The login in ~/.plaud/ stays: the plaud command in your terminal may use it.';
          else if (access && connected) note = ' The login is the one in ~/.plaud/, so this also signs out the plaud command in your terminal.';
          if (expired) note += ' The daily reminder in the chat stops.';
          const ok = await confirmDialog({ title: 'Disconnect Plaud?', message: `Fridolin signs out of Plaud and stops checking for recordings.${note} Your recordings stay in Plaud, and the transcripts already filed stay where they are.`, confirm: 'Disconnect', danger: true });
          if (!ok) return;
          // Plaud's logout may take the bridge's 30 s.
          await post('/api/plaud/disconnect', undefined, { timeout: 60000 });
          toast('Plaud disconnected', 'ok');
          await refresh();
        },
      });
    }
  },
});
