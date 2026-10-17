// Google Calendar: the one-time OAuth client setup (Settings) and the
// per-brain "Connect Google Calendar" card (brain page).

import { get, post, put, del } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { icon } from './icons.js';
import { toast } from './toast.js';
import { btn } from './buttons.js';
import { confirmDialog } from './dialog.js';
import { nextId } from './fields.js';
import { pill, callout, mono, loading } from './common.js';

const CONSOLE = 'https://console.cloud.google.com/apis/credentials';
const ENABLE_API = 'https://console.cloud.google.com/apis/library/calendar-json.googleapis.com';

/** Steps and a paste box for the Google OAuth client. onSaved() runs (and is awaited) after a successful save. */
export function googleClientSetup({ redirectUri, onSaved, compact = false } = {}) {
  const area = h('textarea.mono', {
    id: nextId('gclient'), rows: compact ? 4 : 6, spellcheck: 'false', autocomplete: 'off',
    placeholder: '{"installed":{"client_id":"….apps.googleusercontent.com","client_secret":"…", …}}',
  });
  const file = h('input', { type: 'file', accept: '.json,application/json' });
  file.addEventListener('change', async () => {
    const f = file.files && file.files[0];
    if (f) area.value = await f.text();
  });
  const saveBtn = btn('Save client', {
    kind: 'primary', icon: 'check',
    onClick: async () => {
      const text = area.value.trim();
      if (!text) { toast('Paste the JSON file first'); return; }
      const r = await put('/api/google/client', { json: text });
      area.value = '';
      toast('Google client saved', 'ok');
      if (r && r.note) toast(r.note, 'warn', { timeout: 15000 });
      if (onSaved) await onSaved(r);
    },
  });
  return h('div.stack',
    h('ol.steps-list',
      h('li', 'In ', h('a', { href: ENABLE_API, target: '_blank', rel: 'noopener noreferrer' }, 'Google Cloud Console'), ', pick or create a project and ', h('strong', 'enable the Google Calendar API'), '.'),
      h('li', 'Under ', h('a', { href: CONSOLE, target: '_blank', rel: 'noopener noreferrer' }, 'APIs & Services → Credentials'), ' choose ', h('strong', 'Create credentials → OAuth client ID → Desktop app'), '. (If asked, set up the consent screen first: External, add your own address as a test user, or publish it so sign-ins do not expire after 7 days.)'),
      h('li', 'Download the JSON and paste it here, or pick the file.')),
    h('div.field',
      h('label.field-label', { for: area.id }, 'OAuth client JSON'),
      area,
      h('div.help', 'Stored in the bridge’s data folder, readable only by the bridge — never by brains, never shown again.')),
    h('div.actions', file, saveBtn),
    redirectUri ? h('p.muted.small', 'Using a “Web application” client instead? Add ', mono(redirectUri, { copy: true }), ' to its authorised redirect URIs.') : null);
}

// A load that failed because the page went away needs no message.
const failed = (signal, show) => (e) => { if (!(signal && signal.aborted)) show(e); };

/** Settings page card: client status, replace or remove. signal is the page's. */
export function googleClientCard({ signal } = {}) {
  const el = h('section.card.settings-section', { id: 'sec-google' },
    h('h2', 'Google sign-in'),
    h('p.muted.section-intro', 'Brains can connect a Google account for their calendar. Google needs an OAuth client from a Google Cloud project for that — you set it up once here, then every brain signs in with one click on its own page.'));
  const body = h('div', loading('Loading…'));
  el.appendChild(body);
  async function refresh() {
    const c = await get('/api/google/client', { signal });
    if (!c.configured) {
      mount(body, googleClientSetup({ redirectUri: c.redirect_uri, onSaved: refresh }));
      return;
    }
    const removeBtn = btn('Remove client', {
      kind: 'danger', icon: 'trash', small: true,
      onClick: async () => {
        const ok = await confirmDialog({
          title: 'Remove the Google client?',
          message: 'Brains that are already connected keep working (their tokens carry the client). New sign-ins need a client again.',
          confirm: 'Remove', danger: true,
        });
        if (!ok) return;
        await del('/api/google/client');
        await refresh();
      },
    });
    let replacing = false;
    const replaceBox = h('div');
    const replaceBtn = btn('Replace…', {
      small: true, icon: 'edit',
      onClick: () => {
        replacing = !replacing;
        mount(replaceBox, replacing ? googleClientSetup({ redirectUri: c.redirect_uri, onSaved: refresh, compact: true }) : null);
      },
    });
    mount(body,
      h('div.facts',
        h('div', pill('Configured', 'ok'), ' ', c.type === 'installed' ? 'Desktop app client' : 'Web application client', ' ', mono(c.client_id_hint || ''))),
      c.type === 'web' ? callout('warn', 'Web application client', 'Make sure ', mono(c.redirect_uri, { copy: true }), ' is one of its authorised redirect URIs, or sign-in fails with “redirect_uri_mismatch”.') : null,
      h('div.actions', replaceBtn, removeBtn),
      replaceBox);
  }
  refresh().catch(failed(signal, (e) => mount(body, callout('bad', 'Could not load', e.message))));
  return el;
}

/** Brain page card: connect, choose calendars, disconnect. signal is the page's. */
export function calendarCard(brainId, { signal } = {}) {
  const el = h('div.card', loading('Checking calendar…'));
  let selection = null; // Set of calendar ids being edited
  let dflt = '';

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
        // Same tab: Google sends the browser back to this page afterwards.
        window.location.assign(r.auth_url);
      },
    });
    return b;
  }

  function render(s) {
    const errBox = s.last_error ? callout('bad', 'Sign-in did not finish', s.last_error) : null;
    if (!s.client_configured && !s.connected) {
      mount(el,
        h('div.card-head', h('div.card-title', icon('clock'), h('h3', 'Google Calendar')), pill('Not set up', 'neutral')),
        h('p.muted', 'Before a brain can sign in, the bridge needs a Google OAuth client — a one-time setup for all brains.'),
        googleClientSetup({ onSaved: () => refresh() }));
      return;
    }
    if (!s.connected) {
      mount(el,
        h('div.card-head', h('div.card-title', icon('clock'), h('h3', 'Google Calendar')), pill('Not connected', 'neutral')),
        errBox,
        h('p.muted', 'Sign in with the Google account this brain should use. Afterwards you choose which of its calendars the brain may read and write.'),
        h('div.actions', connectButton()));
      return;
    }

    const cals = s.calendars || [];
    if (!selection) {
      selection = new Set((s.selected && s.selected.length) ? s.selected : cals.filter((c) => c.primary).map((c) => c.id));
      dflt = s.default || [...selection][0] || '';
    }
    const rows = cals.map((c) => {
      const cb = h('input', { type: 'checkbox', checked: selection.has(c.id), id: nextId('cal') });
      const radio = h('input', { type: 'radio', name: `dflt-${brainId}`, checked: c.id === dflt, disabled: !selection.has(c.id), title: 'New events go here' });
      cb.addEventListener('change', () => {
        if (cb.checked) selection.add(c.id); else selection.delete(c.id);
        if (!selection.has(dflt)) dflt = [...selection][0] || '';
        render(s);
      });
      radio.addEventListener('change', () => { dflt = c.id; });
      const swatch = h('span.dot');
      if (c.color) swatch.style.background = c.color;
      return h('tr',
        h('td', cb),
        h('td', h('label', { for: cb.id }, swatch, ' ', c.summary || c.id, c.primary ? h('span.muted', ' (primary)') : null)),
        h('td.muted.small', c.access_role === 'reader' || c.access_role === 'freeBusyReader' ? 'read only' : ''),
        h('td', h('label.small', radio, ' default')));
    });
    const saveBtn = btn('Save calendars', {
      kind: 'primary', icon: 'check',
      onClick: async () => {
        const list = cals.map((c) => c.id).filter((id) => selection.has(id));
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
          message: 'The bridge revokes this brain’s access at Google and deletes its token. The calendars themselves are not touched.',
          confirm: 'Disconnect', danger: true,
        });
        if (!ok) return;
        await del(`/api/brains/${encodeURIComponent(brainId)}/calendar`);
        selection = null;
        await refresh();
      },
    });
    mount(el,
      h('div.card-head', h('div.card-title', icon('clock'), h('h3', 'Google Calendar')), pill('Connected', 'ok')),
      errBox,
      h('p', 'Signed in as ', h('strong', s.account || 'unknown account'), '.'),
      s.list_error ? callout('bad', 'Could not load the calendars', s.list_error, h('div.actions', connectButton('Sign in again'))) : null,
      cals.length ? h('div.table-wrap', h('table.table',
        h('thead', h('tr', h('th', ''), h('th', 'Calendar'), h('th', ''), h('th', 'New events'))),
        h('tbody', rows))) : null,
      h('p.muted.small', 'Google grants access to the whole account; the bridge’s calendar tool keeps this brain to the calendars you tick. Token: ', mono(s.token_path)),
      h('div.actions', cals.length ? saveBtn : null, connectButton('Switch account'), disconnectBtn));
  }

  refresh().catch(failed(signal, (e) => mount(el, callout('bad', 'Could not check the calendar', e.message))));
  el.refresh = refresh;
  return el;
}

