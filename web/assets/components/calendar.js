// Google Calendar: the one-time OAuth client setup, a step-by-step guide
// through Google Cloud Console (Settings), and the per-brain "Connect
// Google Calendar" card (brain page).

import { get, post, put, del, errorStatus } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { icon } from './icons.js';
import { toast } from './toast.js';
import { btn } from './buttons.js';
import { confirmDialog } from './dialog.js';
import { nextId } from './fields.js';
import { tabs } from './tabs.js';
import { pill, callout, mono, loading, timeAgo, kv } from './common.js';
import { errorMessage } from '../lib/errors.js';
/** @import { GoogleClientCheck, GrantHealth, CalendarStatus, CalendarEntry } from '../../types/api.js' */

const CONSOLE = 'https://console.cloud.google.com';
const CALENDAR_API = 'calendar-json.googleapis.com';
const DRIVE_API = 'drive.googleapis.com';
// What a sign-in asks for, for the consent screen's Data access page: the
// bridge's GET /api/google/client sends the same list.
const SCOPES = [
  'https://www.googleapis.com/auth/calendar.events',
  'https://www.googleapis.com/auth/calendar.events.freebusy',
  'https://www.googleapis.com/auth/calendar.calendarlist.readonly',
  'https://www.googleapis.com/auth/drive.file',
];

/**
 * A Google Cloud Console page, in project when one was named.
 * @param {string} path
 * @param {string} project
 * @param {Record<string, string>} [params]
 */
export function consoleLink(path, project, params = {}) {
  const q = new URLSearchParams(params);
  if (project) q.set('project', project);
  const qs = q.toString();
  // URLSearchParams escapes the commas of a list of APIs, which the
  // console takes either way; kept readable.
  return `${CONSOLE}${path}${qs ? `?${qs.replace(/%2C/g, ',')}` : ''}`;
}

/** The address bar of a dashboard served on this machine says 127.0.0.1. */
export function onThisMachine() {
  const host = (globalThis.location && globalThis.location.hostname) || '127.0.0.1';
  return host === '127.0.0.1' || host === 'localhost' || host === '[::1]' || host === '::1';
}

/**
 * client id and secret as the JSON Google Cloud Console offers for a
 * Desktop app client, or an error to show.
 * @param {string} id
 * @param {string} secret
 * @returns {{json?: string, error?: string}}
 */
export function clientJSONFrom(id, secret) {
  id = id.trim();
  secret = secret.trim();
  if (!id || !secret) return { error: 'Paste both the client ID and the client secret' };
  if (!/^[^\s]+\.apps\.googleusercontent\.com$/.test(id)) return { error: 'The client ID ends in .apps.googleusercontent.com: copy it again from Google Cloud Console → Clients' };
  if (/\s/.test(secret)) return { error: 'The client secret has no spaces: copy it again' };
  return { json: JSON.stringify({ installed: { client_id: id, client_secret: secret } }) };
}

/**
 * An external link that opens in a new tab.
 * @param {string} href
 * @param {string} text
 */
const ext = (href, text) => h('a', { href, target: '_blank', rel: 'noopener noreferrer' }, text, ' ', icon('external'));

/**
 * What Google says during a sign-in with your own, unverified app, and
 * why that is fine for one person's bridge.
 */
function unverifiedAppHelp() {
  return h('details.details',
    h('summary', 'About “Google hasn’t verified this app” and the 100-user cap'),
    h('div.stack.small',
      h('p', 'Your app is in production but not verified by Google, so each sign-in first shows ', h('strong', '“Google hasn’t verified this app”'), '. That is expected: Google verifies apps that are offered to the public, and this one is yours alone. Click ', h('strong', 'Advanced'), ', then ', h('strong', 'Go to Fridolin (unsafe)'), ' (it shows your app’s name), and allow access.'),
      h('p', 'An unverified app may be used by at most ', h('strong', '100 Google accounts'), ' in its lifetime: each account that signs in counts once, and signing in again does not count again. Your own accounts and your family’s are far from that, so there is nothing to apply for.'),
      h('p', 'Leaving the app in ', h('strong', 'Testing'), ' instead avoids the warning only for the test users you list, and Google then ends every sign-in after 7 days. Publish it, as step 3 says.')));
}

/**
 * The result of POST /api/google/client/check.
 * @param {GoogleClientCheck} check
 */
export function clientCheckResult(check) {
  const kind = check.status === 'ok' ? 'ok' : check.status === 'warn' ? 'warn' : 'bad';
  const title = check.status === 'ok' ? 'The client works' : check.status === 'warn' ? 'The client works, with a note' : 'The client does not work yet';
  return callout(kind, title,
    h('ul.check-items', (check.items || []).map((it) => h('li', pill(it.name, it.status === 'ok' ? 'ok' : it.status === 'warn' ? 'warn' : 'bad'), ' ', it.detail))));
}

/**
 * The step-by-step setup of a Google OAuth client: links into Google Cloud
 * Console for each step, in your project once you name it, the values to
 * type with copy buttons, a paste box (the JSON file, or the client ID and
 * secret), and a check of the client after it is saved. onSaved(saved,
 * check) runs (and is awaited) once the client is saved and the check did
 * not find it broken, or the owner keeps it anyway.
 * drive: the client is for the Drive backup alone (saved to
 * /api/backup/drive-client, not checked).
 * @param {{redirectUri?: string, onSaved?: (saved: {note?: string} | null, check?: GoogleClientCheck | null) => unknown, compact?: boolean, drive?: boolean}} [options]
 */
export function googleClientSetup({ redirectUri = 'http://127.0.0.1:8765/', onSaved, compact = false, drive = false } = {}) {
  const endpoint = drive ? '/api/backup/drive-client' : '/api/google/client';
  let project = '';
  /** @type {{el: HTMLAnchorElement, path: string, params?: Record<string, string>}[]} */
  const links = [];
  /**
   * @param {string} path
   * @param {string} text
   * @param {Record<string, string>} [params]
   */
  const step = (path, text, params) => {
    const el = /** @type {HTMLAnchorElement} */ (ext(consoleLink(path, project, params), text));
    links.push({ el, path, params });
    return el;
  };
  const projectIn = h('input.mono', { id: nextId('gproject'), type: 'text', spellcheck: 'false', autocomplete: 'off', placeholder: 'fridolin-123456' });
  projectIn.addEventListener('input', () => {
    project = projectIn.value.trim();
    for (const l of links) l.el.setAttribute('href', consoleLink(l.path, project, l.params));
  });

  // The paste box: the JSON file Google offers, or the two values.
  const area = h('textarea.mono', {
    id: nextId('gclient'), rows: compact ? 4 : 6, spellcheck: 'false', autocomplete: 'off',
    placeholder: '{"installed":{"client_id":"….apps.googleusercontent.com","client_secret":"…", …}}',
  });
  const file = h('input', { type: 'file', accept: '.json,application/json', 'aria-label': 'OAuth client JSON file' });
  file.addEventListener('change', async () => {
    const f = file.files && file.files[0];
    if (f) area.value = await f.text();
  });
  const idIn = h('input.mono', { id: nextId('gid'), type: 'text', spellcheck: 'false', autocomplete: 'off', placeholder: '….apps.googleusercontent.com' });
  const secretIn = h('input.mono', { id: nextId('gsecret'), type: 'password', spellcheck: 'false', autocomplete: 'off', placeholder: 'GOCSPX-…' });
  let mode = 'json';
  const pasteTabs = tabs({
    label: 'How to add the client',
    items: [['json', 'JSON file'], ['id', 'Client ID and secret']],
    onSelect: (key, panel) => {
      mode = key;
      mount(panel, key === 'json'
        ? h('div.field',
          h('label.field-label', { for: area.id }, 'OAuth client JSON'),
          area,
          h('div.help', 'Download it from the client’s page (Download JSON), then paste it or pick the file.'),
          h('div.actions', file))
        : h('div.stack',
          h('div.field', h('label.field-label', { for: idIn.id }, 'Client ID'), idIn),
          h('div.field', h('label.field-label', { for: secretIn.id }, 'Client secret'), secretIn,
            h('div.help', 'Both are on the client’s page in Google Cloud Console. Google shows a new client’s secret only once, when it is created: copy it then, or add a new secret there.'))));
    },
  });
  const result = h('div');
  const saveBtn = btn('Save and check', {
    kind: 'primary', icon: 'check',
    onClick: async () => {
      let text = area.value.trim();
      if (mode === 'id') {
        const built = clientJSONFrom(idIn.value, secretIn.value);
        if (built.error || !built.json) { toast(built.error || 'Paste the client first'); return; }
        text = built.json;
      } else if (!text) { toast('Paste the JSON file first'); return; }
      const r = await put(endpoint, { json: text });
      area.value = '';
      secretIn.value = '';
      if (r && r.note) toast(r.note, 'warn', { timeout: 15000 });
      if (drive) {
        toast('Google client saved', 'ok');
        if (onSaved) await onSaved(r, null);
        return;
      }
      /** @type {GoogleClientCheck | null} */
      let check = null;
      try {
        check = await post('/api/google/client/check');
      } catch (e) {
        toast(`Saved; the check could not run: ${errorMessage(e)}`, 'warn');
      }
      if (check && check.status === 'bad') {
        mount(result, clientCheckResult(check),
          h('p.small', 'The client is saved, but sign-ins with it will fail. Fix it in Google Cloud Console and paste it again, or keep it anyway.'),
          h('div.actions', btn('Keep it anyway', { small: true, onClick: async () => { if (onSaved) await onSaved(r, check); } })));
        return;
      }
      toast('Google client saved', 'ok');
      if (onSaved) await onSaved(r, check);
    },
  });

  const apis = drive ? DRIVE_API : `${CALENDAR_API},${DRIVE_API}`;
  return h('div.stack.google-setup',
    h('p.muted', 'About ten minutes in Google Cloud Console, once. Each link opens the right page in a new tab; come back here after each step.'),
    h('ol.steps-list',
      h('li',
        h('strong', 'Create a project. '), step('/projectcreate', 'New project'), ' — name it ', mono('Fridolin', { copy: true }), ', or use a project you have.',
        h('div.field',
          h('label.field-label', { for: projectIn.id }, 'Its project ID (optional)'),
          projectIn,
          h('div.help', 'Shown under the name as you create it. Typed here, the links below open that project directly.'))),
      h('li',
        h('strong', 'Enable the APIs. '), step('/flows/enableapi', drive ? 'Enable the Google Drive API' : 'Enable the Google Calendar and Drive APIs', { apiid: apis }),
        ' — one page, one Next/Enable. ', drive ? null : 'Drive is for backups; leave it out if you will not back up to Google Drive.'),
      h('li',
        h('strong', 'Set up the consent screen. '), step('/auth/overview', 'Google Auth Platform'), ' (older consoles: APIs & Services → OAuth consent screen) → Get started:',
        h('ul.small',
          h('li', 'App name: ', mono('Fridolin', { copy: true }), '; User support email: your own address.'),
          h('li', 'Audience: ', h('strong', 'External'), '. Contact information: your address again. Agree and create.'),
          h('li', step('/auth/branding', 'Branding'), ': leave ', h('strong', 'Authorised domains'), ' empty. A Desktop app client returns to this machine (', mono(redirectUri), '), which needs no domain, and Fridolin needs no homepage or privacy policy link.'),
          h('li', step('/auth/scopes', 'Data access'), ' → Add or remove scopes → paste these into “Manually add scopes”, Add to table, Update, Save:',
            h('div.stack', SCOPES.filter((sc) => !drive || sc.endsWith('drive.file')).map((sc) => mono(sc, { copy: true })))),
          h('li', step('/auth/audience', 'Audience'), ' → ', h('strong', 'Publish app'), ' → Confirm, so the status reads ', h('strong', 'In production'), '. In Testing, Google ends every sign-in after 7 days.'))),
      h('li',
        h('strong', 'Create the client. '), step('/auth/clients/create', 'Create client'), ' → Application type ', h('strong', 'Desktop app'), ', name ', mono('Fridolin', { copy: true }), ' → Create.',
        h('p.small', 'A Desktop app client needs no redirect URI: Google sends the browser back to ', mono(redirectUri, { copy: true }), ' on its own. Using a ', h('strong', 'Web application'), ' client instead? Add exactly that address under ', h('strong', 'Authorised redirect URIs'), '.')),
      h('li',
        h('strong', 'Add it here. '), 'Download the JSON on the dialog that shows the new client, or copy its ID and secret.',
        pasteTabs.list, pasteTabs.panel,
        h('div.help', 'Stored in the bridge’s data folder, readable only by the bridge — never by brains, never shown again.'),
        h('div.actions', saveBtn),
        result)),
    unverifiedAppHelp());
}

/**
 * A box for the address Google sent the browser back to, for a dashboard
 * opened from another machine: the sign-in returns to the bridge's
 * loopback address on the machine with the browser, where nothing
 * answers, so the owner copies that address here. onDone() runs once the
 * bridge took it.
 * @param {{redirectUri?: string, onDone?: () => unknown}} [options]
 */
export function pasteReturnBox({ redirectUri = 'http://127.0.0.1:8765/', onDone } = {}) {
  const input = h('input.mono', { id: nextId('greturn'), type: 'text', spellcheck: 'false', autocomplete: 'off', placeholder: `${redirectUri}?state=…&code=…` });
  const go = btn('Finish the sign-in', {
    kind: 'primary', icon: 'check', small: true,
    onClick: async () => {
      if (!input.value.trim()) { toast('Paste the address first'); return; }
      await post('/api/google/callback', { url: input.value });
      input.value = '';
      toast('Signed in', 'ok');
      if (onDone) await onDone();
    },
  });
  return callout('info', 'Opened the dashboard from another machine?',
    h('p', 'After you allow access, Google sends its tab to ', mono(redirectUri), ' on the machine with the browser, where the bridge is not: the page does not load. That is expected. Copy the whole address from that tab’s address bar and paste it here.'),
    h('div.field', h('label.field-label', { for: input.id }, 'The address Google sent you to'), input),
    h('div.actions', go));
}

// What a calendar's access role lets the account do, in plain words.
/** @type {Record<string, string>} */
const ACCESS = { owner: 'owner', writer: 'can edit', reader: 'read only', freeBusyReader: 'free/busy only' };

// The grant's state, as the card's pill says it.
/** @type {Record<string, [string, string]>} */
const GRANT_PILLS = {
  valid: ['Working', 'ok'],
  expiring: ['Ends soon', 'warn'],
  revoked: ['Revoked by Google', 'bad'],
  needs_reconsent: ['Needs reconnecting', 'bad'],
  unchecked: ['Connected', 'neutral'],
};

/** @param {GrantHealth} health */
function grantPill(health) {
  const [text, kind] = GRANT_PILLS[health.state] || GRANT_PILLS.unchecked;
  return pill(text, kind, { title: health.detail });
}

/**
 * The overview of a connected brain's calendar: the account, the grant's
 * state and when it last worked, the calendars ticked and the default.
 * @param {CalendarStatus} s
 * @param {CalendarEntry[]} cals
 */
function grantFacts(s, cals) {
  const health = s.health || { state: 'unchecked' };
  const name = (/** @type {string | undefined} */ id) => {
    const c = cals.find((x) => x.id === id);
    return (c && c.summary) || id || '—';
  };
  const ticked = s.selected || [];
  return kv([
    ['Account', h('strong', s.account || 'unknown account')],
    ['Sign-in', h('span', grantPill(health), health.since ? h('span.muted.small', ' ', timeAgo(health.since, 'since ')) : null)],
    ['Last worked', health.last_ok ? timeAgo(health.last_ok) : h('span.muted', 'not used yet')],
    ['Google ends it', health.expires ? timeAgo(health.expires) : null],
    ['Calendars', ticked.length ? ticked.map(name).join(', ') : h('span.muted', 'the primary one')],
    ['Default', name(s.default || ticked[0])],
  ]);
}

// A load that failed because the page went away needs no message.
/**
 * @param {AbortSignal | undefined} signal
 * @param {(e: any) => void} show
 * @returns {(e: any) => void}
 */
const failed = (signal, show) => (e) => { if (!(signal && signal.aborted)) show(e); };

/**
 * Settings page card: which client sign-ins use (your own, or the build's
 * built-in one), a check of it, replace or remove, or the setup steps.
 * signal is the page's.
 * @param {{signal?: AbortSignal}} [options]
 */
export function googleClientCard({ signal } = {}) {
  const el = h('section.card.settings-section', { id: 'sec-google' },
    h('div.section-heading', h('h2', 'Google sign-in'), h('p.muted.section-intro', 'Brains can connect a Google account for their calendar, and backups go to Google Drive. Google needs an OAuth client from a Google Cloud project for that — you set it up once here, then every brain signs in with one click on its own page.')));
  const body = h('div', loading('Loading…'));
  el.appendChild(body);
  /** @type {GoogleClientCheck | null} */
  let lastCheck = null;
  async function refresh() {
    const c = await get('/api/google/client', { signal });
    const setup = (compact = false) => googleClientSetup({
      redirectUri: c.redirect_uri, compact,
      onSaved: async (_saved, check) => { lastCheck = check || null; await refresh(); },
    });
    if (!c.configured) {
      mount(body, setup());
      return;
    }
    const checkBox = h('div', lastCheck ? clientCheckResult(lastCheck) : null);
    lastCheck = null;
    const checkBtn = btn('Check the client', {
      small: true, icon: 'check',
      onClick: async () => { mount(checkBox, clientCheckResult(await post('/api/google/client/check'))); },
    });
    let replacing = false;
    const replaceBox = h('div');
    const builtin = c.source === 'builtin';
    const replaceBtn = btn(builtin ? 'Use your own client instead…' : 'Replace…', {
      small: true, icon: 'edit',
      onClick: () => {
        replacing = !replacing;
        mount(replaceBox, replacing ? setup(true) : null);
      },
    });
    const removeBtn = builtin ? null : btn('Remove client', {
      kind: 'danger', icon: 'trash', small: true,
      onClick: async () => {
        const ok = await confirmDialog({
          title: 'Remove the Google client?',
          message: c.builtin
            ? 'Sign-ins then use the client built into this build. Brains connected with yours keep working until they connect again.'
            : 'Brains that are already connected keep working (their tokens carry the client). New sign-ins need a client again.',
          confirm: 'Remove', danger: true,
        });
        if (!ok) return;
        await del('/api/google/client');
        await refresh();
      },
    });
    mount(body,
      h('div.facts',
        builtin
          ? h('div', pill('Built-in client', 'ok'), ' ', 'Desktop app client of this build’s publisher', ' ', mono(c.client_id_hint || ''))
          : h('div', pill('Your own client', 'ok'), ' ', c.type === 'installed' ? 'Desktop app client' : 'Web application client', ' ', mono(c.client_id_hint || ''))),
      builtin ? h('p.small', 'This build comes with a Google client, so brains connect with one click and nothing to set up. Google shows its publisher’s app name at sign-in. A client of your own, if you add one, takes its place.') : null,
      !builtin && c.builtin ? h('p.muted.small', 'This build also has a built-in client, which sign-ins use if you remove yours.') : null,
      c.type === 'web' ? callout('warn', 'Web application client', 'Make sure ', mono(c.redirect_uri, { copy: true }), ' is one of its authorised redirect URIs, or sign-in fails with “redirect_uri_mismatch”.') : null,
      checkBox,
      h('div.actions', checkBtn, replaceBtn, removeBtn),
      replaceBox,
      builtin ? null : unverifiedAppHelp());
  }
  refresh().catch(failed(signal, (e) => mount(body, callout('bad', 'Could not load', e.message))));
  return el;
}

/**
 * Brain page card: connect, choose calendars, disconnect. signal is the
 * page's. el.refresh() checks the calendar again.
 * @param {string} brainId
 * @param {{signal?: AbortSignal}} [options]
 */
export function calendarCard(brainId, { signal } = {}) {
  const el = h('div.card', loading('Checking calendar…'));
  /** @type {Set<string> | null} */
  let selection = null; // Set of calendar ids being edited
  let dflt = '';
  // The paste box for a sign-in from another machine; it stays while the
  // card repaints.
  const returnBox = h('div');
  /** @param {...any} children */
  const show = (...children) => mount(el, ...children, returnBox);

  // Buttons await it, so they stay busy until the card shows the result.
  async function refresh() {
    const s = await get(`/api/brains/${encodeURIComponent(brainId)}/calendar`, { signal });
    render(s);
  }

  function connectButton(label = 'Connect Google Calendar') {
    const b = btn(label, {
      kind: 'primary', icon: 'external',
      onClick: async () => {
        const r = await post(`/api/brains/${encodeURIComponent(brainId)}/calendar/connect`);
        if (onThisMachine()) {
          // Same tab: Google sends the browser back to this page afterwards.
          window.location.assign(r.auth_url);
          return;
        }
        // From another machine Google's return address does not load there:
        // a new tab for Google, and this one waits for its address.
        window.open(r.auth_url, '_blank', 'noopener');
        mount(returnBox, pasteReturnBox({ onDone: async () => { mount(returnBox); await refresh(); } }));
      },
    });
    return b;
  }

  // Files named like the copies of the OAuth client that older versions
  // wrote next to the tokens, each holding a client secret. The bridge
  // goes by name and secret only, so it cannot tell such a copy from a
  // file of the owner's: it names them, but deletes none itself.
  /** @param {CalendarStatus} s */
  function copiesBox(s) {
    const names = s.leftover_client_copies || [];
    if (!names.length) return null;
    return callout('warn', 'Client secrets in the credentials folder',
      'Files named like the copies of the OAuth client that older versions wrote for the calendar tool, each holding a client secret: ',
      names.flatMap((n, i) => (i ? [', ', mono(n)] : [mono(n)])),
      '. The bridge no longer reads them, and a brain the sandbox does not hold (on Linux, or with the sandbox off) can: delete them by hand, unless something else of yours uses them.');
  }

  /** @param {CalendarStatus} s */
  function render(s) {
    const errBox = s.last_error ? callout('bad', 'Sign-in did not finish', s.last_error) : null;
    if (!s.client_configured && !s.connected) {
      show(
        h('div.card-head', h('div.card-title', icon('clock'), h('h3', 'Google Calendar')), pill('Not set up', 'neutral')),
        copiesBox(s),
        h('p.muted', 'Before a brain can sign in, the bridge needs a Google OAuth client — a one-time setup for all brains.'),
        googleClientSetup({ onSaved: () => refresh() }));
      return;
    }
    if (!s.connected) {
      show(
        h('div.card-head', h('div.card-title', icon('clock'), h('h3', 'Google Calendar')), pill('Not connected', 'neutral')),
        errBox,
        copiesBox(s),
        h('p.muted', 'Sign in with the Google account this brain should use. Afterwards you choose which of its calendars the brain may read and write.'),
        h('div.actions', connectButton()));
      return;
    }

    const cals = s.calendars || [];
    const health = s.health || { state: 'unchecked' };
    const broken = health.state === 'revoked' || health.state === 'needs_reconsent';
    if (!selection) {
      selection = new Set((s.selected && s.selected.length) ? s.selected : cals.filter((c) => c.primary).map((c) => c.id));
      dflt = s.default || [...selection][0] || '';
    }
    const chosen = selection; // the ticks, kept across renders
    const rows = cals.map((c) => {
      const cb = h('input', { type: 'checkbox', checked: chosen.has(c.id), id: nextId('cal') });
      const radio = h('input', { type: 'radio', name: `dflt-${brainId}`, checked: c.id === dflt, disabled: !chosen.has(c.id), title: 'New events go here' });
      cb.addEventListener('change', () => {
        if (cb.checked) chosen.add(c.id); else chosen.delete(c.id);
        if (!chosen.has(dflt)) dflt = [...chosen][0] || '';
        render(s);
      });
      radio.addEventListener('change', () => { dflt = c.id; });
      const swatch = h('span.dot');
      if (c.color) swatch.style.background = c.color;
      return h('tr',
        h('td', cb),
        h('td', h('label', { for: cb.id }, swatch, ' ', c.summary || c.id, c.primary ? h('span.muted', ' (primary)') : null)),
        h('td.muted.small', ACCESS[c.access_role] || c.access_role || ''),
        h('td', h('label.small', radio, ' default')));
    });
    const saveBtn = btn('Save calendars', {
      kind: 'primary', icon: 'check',
      onClick: async () => {
        const list = cals.map((c) => c.id).filter((id) => chosen.has(id));
        if (!list.length) { toast('Tick at least one calendar, or disconnect'); return; }
        await put(`/api/brains/${encodeURIComponent(brainId)}/calendar`, { account: s.account, calendars: list, default: dflt });
        toast('Calendars saved', 'ok');
        selection = null;
        await refresh();
      },
    });
    const disconnectBtn = btn('Disconnect', {
      kind: 'danger', icon: 'x', small: true,
      onClick: async () => {
        const ok = await confirmDialog({
          title: 'Disconnect Google Calendar?',
          message: 'The bridge revokes this brain’s access at Google and deletes its token, unless another brain is signed in to the same Google account: that brain would lose its access too, so the access then stays at Google. The calendars themselves are not touched.',
          confirm: 'Disconnect', danger: true,
        });
        if (!ok) return;
        const url = `/api/brains/${encodeURIComponent(brainId)}/calendar`;
        let r;
        try {
          r = await del(url);
        } catch (e) {
          // The revocation was not confirmed by Google, so the token was kept.
          if (errorStatus(e) !== 502) throw e;
          const anyway = await confirmDialog({
            title: 'Google did not revoke the access',
            message: h('div', h('p', errorMessage(e)), h('p', 'Delete the token here anyway? The access then stays at Google until you remove it at myaccount.google.com/permissions.')),
            confirm: 'Delete the token', danger: true,
          });
          if (!anyway) return;
          r = await del(`${url}?local_only=1`);
        }
        if (r && r.note) toast(`Disconnected; ${r.note}`, 'info');
        selection = null;
        await refresh();
      },
    });
    show(
      h('div.card-head', h('div.card-title', icon('clock'), h('h3', 'Google Calendar')), grantPill(health)),
      errBox,
      copiesBox(s),
      grantFacts(s, cals),
      broken || health.state === 'expiring'
        ? callout(broken ? 'bad' : 'warn', broken ? (health.state === 'revoked' ? 'Google revoked this sign-in' : 'This sign-in has to be renewed') : 'This sign-in ends soon',
          health.detail || '', h('div', 'Reconnect signs in again, with the same or another account; the calendars you ticked stay ticked for the same account.'),
          h('div.actions', connectButton('Reconnect')))
        : null,
      s.broad_access ? callout('warn', 'More access than the calendar needs',
        'This brain signed in with full access to the account’s calendars, which older versions asked for and which includes deleting calendars and changing who they are shared with. Disconnect, then connect again, to grant only what the calendar uses: its events, free and busy times, and the list of calendars. Disconnecting revokes the old access at Google, every copy of it included, unless another brain is signed in to the same Google account: disconnect that one too, or the old access stays.') : null,
      s.list_error && !broken ? callout('bad', s.reconnect_needed ? 'Google no longer accepts this sign-in' : 'Could not load the calendars', s.list_error, h('div.actions', connectButton('Reconnect'))) : null,
      cals.length ? h('div.table-wrap', h('table.table',
        h('thead', h('tr', h('th', 'Use'), h('th', 'Calendar'), h('th', 'Access'), h('th', 'New events'))),
        h('tbody', rows))) : null,
      h('p.muted.small', 'Google grants access to the whole account; the bridge makes this brain’s calendar calls itself, only on the calendars you tick, and with the sandbox on the brain cannot read the token. Token: ', mono(s.token_path)),
      h('div.actions', cals.length ? saveBtn : null, connectButton('Switch account'), disconnectBtn));
  }

  refresh().catch(failed(signal, (e) => mount(el, callout('bad', 'Could not check the calendar', e.message))));
  return Object.assign(el, { refresh });
}

