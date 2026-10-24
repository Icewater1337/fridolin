import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { calendarCard, googleClientSetup, googleClientCard, consoleLink, clientJSONFrom, clientCheckResult } from '../assets/components/calendar.js';

// setImmediate, because the tests mock setTimeout (the toasts' and the
// requests' timers) so nothing waits for them.
const tick = () => new Promise((r) => setImmediate(r));
const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const connected = { connected: true, client_configured: true, account: 'you@example.com', calendars: [{ id: 'primary', summary: 'Calendar', primary: true }], token_path: '/Users/you/token.json' };

test('Disconnect stays busy until the card shows the result', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = calendarCard('work');
  document.body.append(card);
  net.answer(0, connected);
  await tick();

  const disconnect = button(card, 'Disconnect');
  disconnect.click();
  await tick();
  button(document.querySelector('dialog'), 'Disconnect').click(); // confirm
  await tick();
  assert.equal(net.calls[1].init.method, 'DELETE');
  net.answer(1, {});
  await tick();
  assert.equal(net.calls[2].url, '/api/brains/work/calendar', 'the card reloads');
  assert.equal(disconnect.disabled, true, 'the button waits for the reload');
  net.answer(2, { connected: false, client_configured: true });
  await tick();
  assert.equal(disconnect.disabled, false);
  assert.ok(button(card, 'Connect Google Calendar'), 'the card shows the result');
});

test('a revocation Google does not confirm offers to delete the token here only', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = calendarCard('work');
  document.body.append(card);
  net.answer(0, connected);
  await tick();

  button(card, 'Disconnect').click();
  await tick();
  button(document.querySelector('dialog'), 'Disconnect').click();
  await tick();
  net.answer(1, { error: 'the revocation was not confirmed by Google: 503 Service Unavailable' }, 502);
  await tick();
  await tick();
  const dialog = document.querySelectorAll('dialog').at(-1);
  assert.ok(dialog.textContent.includes('not confirmed by Google'), dialog.textContent);
  button(dialog, 'Delete the token').click();
  await tick();
  assert.equal(net.calls[2].init.method, 'DELETE');
  assert.equal(net.calls[2].url, '/api/brains/work/calendar?local_only=1');
});

test('a sign-in Google no longer accepts says so', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = calendarCard('work');
  document.body.append(card);
  net.answer(0, { ...connected, calendars: [], list_error: 'the sign-in no longer works at Google — connect this brain again', reconnect_needed: true });
  await tick();
  assert.ok(card.textContent.includes('Google no longer accepts this sign-in'), card.textContent);
  assert.ok(button(card, 'Sign in again'));
});

test('the broad-access warning says when Disconnect leaves the old access', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = calendarCard('work');
  document.body.append(card);
  net.answer(0, { ...connected, broad_access: true });
  await tick();
  assert.ok(card.textContent.includes('More access than the calendar needs'), card.textContent);
  // Google keeps a grant another brain shares, so one Disconnect is not enough.
  assert.ok(card.textContent.includes('unless another brain is signed in to the same Google account'), card.textContent);
});

test('files like old copies of the OAuth client are named, connected or not', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const copies = ['google_calendar_credentials_a.json', 'google_calendar_credentials_old.json'];
  for (const [i, status] of [connected, { connected: false, client_configured: true }, { connected: false, client_configured: false }].entries()) {
    const card = calendarCard('work');
    document.body.append(card);
    net.answer(i, { ...status, leftover_client_copies: copies });
    await tick();
    const text = card.textContent;
    assert.ok(text.includes('Client secrets in the credentials folder'), text);
    assert.ok(copies.every((n) => text.includes(n)), text);
    // The bridge knows a name and a secret, not that a file is such a copy.
    assert.ok(text.includes('Files named like the copies of the OAuth client'), text);
    assert.ok(!text.includes('Nothing reads them'), text);
    // The bridge deletes none itself: one may be yours, in use elsewhere.
    assert.ok(text.includes('delete them by hand, unless something else of yours uses them'), text);
  }
  const card = calendarCard('work');
  document.body.append(card);
  net.answer(3, { ...connected, leftover_client_copies: [] });
  await tick();
  assert.ok(!card.textContent.includes('Client secrets in the credentials folder'), 'no copies, no warning');
});

test('a reload error reaches the button instead of an unhandled rejection', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = calendarCard('work');
  document.body.append(card);
  net.answer(0, connected);
  await tick();
  button(card, 'Save calendars').click();
  await tick();
  net.answer(1, {});
  await tick();
  net.answer(2, { error: 'Google did not answer' }, 502);
  await tick();
  await tick();
  assert.ok(document.querySelectorAll('.toast').some((el) => el.textContent.includes('Google did not answer')), 'shown as a toast by the button');
});

test('the client file picker has a name', () => {
  installDom();
  const setup = googleClientSetup();
  const file = setup.querySelector('input[type=file]');
  assert.equal(file.getAttribute('aria-label'), 'OAuth client JSON file', 'a screen reader says what the file is for');
});

test('console links open the project named, and keep a list of APIs readable', () => {
  assert.equal(consoleLink('/auth/overview', ''), 'https://console.cloud.google.com/auth/overview');
  assert.equal(consoleLink('/auth/overview', 'fridolin-1'), 'https://console.cloud.google.com/auth/overview?project=fridolin-1');
  assert.equal(consoleLink('/flows/enableapi', 'p', { apiid: 'calendar-json.googleapis.com,drive.googleapis.com' }),
    'https://console.cloud.google.com/flows/enableapi?apiid=calendar-json.googleapis.com,drive.googleapis.com&project=p');
});

test('a client ID and secret become the JSON of a Desktop app client', () => {
  assert.deepEqual(JSON.parse(clientJSONFrom(' 12-ab.apps.googleusercontent.com ', 'GOCSPX-x\n').json),
    { installed: { client_id: '12-ab.apps.googleusercontent.com', client_secret: 'GOCSPX-x' } });
  assert.match(clientJSONFrom('my-project', 'GOCSPX-x').error, /apps\.googleusercontent\.com/);
  assert.match(clientJSONFrom('12-ab.apps.googleusercontent.com', '').error, /both/);
});

test('the setup walks through every Google Cloud step and follows the project typed', () => {
  installDom();
  const setup = googleClientSetup({ redirectUri: 'http://127.0.0.1:8765/' });
  const text = setup.textContent;
  for (const want of ['Create a project', 'Enable the APIs', 'Set up the consent screen', 'External', 'Authorised domains', 'Publish app', 'In production',
    'Desktop app', 'http://127.0.0.1:8765/', 'calendar.events.freebusy', 'drive.file', 'Google hasn’t verified this app', 'Advanced', '100 Google accounts']) {
    assert.ok(text.includes(want), `the setup says ${want}`);
  }
  const hrefs = () => setup.querySelectorAll('a').map((a) => a.getAttribute('href')).filter((x) => x.startsWith('https://console.cloud.google.com'));
  assert.ok(hrefs().some((x) => x.includes('/flows/enableapi?apiid=calendar-json.googleapis.com,drive.googleapis.com')), hrefs().join(' '));
  assert.ok(hrefs().some((x) => x.includes('/auth/clients/create')));
  const project = setup.querySelectorAll('input').find((i) => i.getAttribute('placeholder') === 'fridolin-123456');
  project.value = 'my-proj';
  project.dispatchEvent(new Event('input'));
  assert.ok(hrefs().filter((x) => !x.includes('/projectcreate')).every((x) => x.includes('project=my-proj')), hrefs().join(' '));
});

test('a pasted client is saved and checked, and a broken one is not taken quietly', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const saved = [];
  const setup = googleClientSetup({ onSaved: (r, check) => { saved.push(check); } });
  document.body.append(setup);
  button(setup, 'Client ID and secret').click();
  const [id, secret] = setup.querySelectorAll('input').filter((i) => ['….apps.googleusercontent.com', 'GOCSPX-…'].includes(i.getAttribute('placeholder')));
  id.value = '12-ab.apps.googleusercontent.com';
  secret.value = 'GOCSPX-x';
  button(setup, 'Save and check').click();
  await tick();
  assert.equal(net.calls[0].url, '/api/google/client');
  assert.deepEqual(JSON.parse(JSON.parse(net.calls[0].init.body).json), { installed: { client_id: '12-ab.apps.googleusercontent.com', client_secret: 'GOCSPX-x' } });
  net.answer(0, { status: 'saved', type: 'installed', note: '' });
  await tick();
  assert.equal(secret.value, '', 'the secret does not stay in the form');
  assert.equal(net.calls[1].url, '/api/google/client/check');
  net.answer(1, { status: 'bad', source: 'own', type: 'installed', items: [{ name: 'Known to Google', status: 'bad', detail: 'Google does not accept this client ID and secret' }] });
  await tick();
  await tick();
  assert.deepEqual(saved, [], 'a broken client waits for the owner');
  assert.ok(setup.textContent.includes('Google does not accept this client ID and secret'), setup.textContent);
  button(setup, 'Keep it anyway').click();
  await tick();
  assert.equal(saved.length, 1);
});

test('a build with a built-in client says so and needs no setup', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = googleClientCard();
  document.body.append(card);
  net.answer(0, { configured: true, builtin: true, source: 'builtin', type: 'installed', client_id_hint: '999-…', redirect_uri: 'http://127.0.0.1:8765/', scopes: [] });
  await tick();
  assert.ok(card.textContent.includes('Built-in client'), card.textContent);
  assert.ok(!card.textContent.includes('Create a project'), 'no setup steps');
  assert.ok(!button(card, 'Remove client'), 'nothing of yours to remove');
  button(card, 'Use your own client instead…').click();
  assert.ok(card.textContent.includes('Create a project'), 'the steps for a client of your own');
});

test('a calendar sign-in from another machine takes the address Google sent the browser to', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const opened = [];
  Object.assign(globalThis, { location: { hostname: 'fridolin-core.example.ts.net' }, window: { open: (u) => opened.push(u) } });
  t.after(() => { delete globalThis.location; delete globalThis.window; });
  const card = calendarCard('work');
  document.body.append(card);
  net.answer(0, { connected: false, client_configured: true });
  await tick();
  button(card, 'Connect Google Calendar').click();
  await tick();
  net.answer(1, { auth_url: 'https://accounts.google.com/o/oauth2/v2/auth?state=s' });
  await tick();
  assert.deepEqual(opened, ['https://accounts.google.com/o/oauth2/v2/auth?state=s'], 'Google opens in a tab of its own');
  const input = card.querySelectorAll('input').find((i) => (i.getAttribute('placeholder') || '').includes('state='));
  assert.ok(input, card.textContent);
  input.value = 'http://127.0.0.1:8765/?state=s&code=c';
  button(card, 'Finish the sign-in').click();
  await tick();
  assert.equal(net.calls[2].url, '/api/google/callback');
  assert.deepEqual(JSON.parse(net.calls[2].init.body), { url: 'http://127.0.0.1:8765/?state=s&code=c' });
  net.answer(2, { redirect: '/#/brains/work' });
  await tick();
  assert.equal(net.calls[3].url, '/api/brains/work/calendar', 'the card shows the result');
});

test('a check result names each item', () => {
  installDom();
  const el = clientCheckResult({ status: 'warn', source: 'own', type: 'web', items: [{ name: 'Redirect', status: 'warn', detail: 'must be one of its Authorised redirect URIs' }] });
  assert.ok(el.textContent.includes('The client works, with a note') && el.textContent.includes('Authorised redirect URIs'), el.textContent);
});
