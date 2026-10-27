import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import page from '../assets/pages/integrations.js';

const status = (checked) => ({
  cli: { found: true }, connected: true, account: 'you@example.com', login_dir: '/Users/you/.fridolin/plaud',
  settings: { enabled: true, brain: '', polished: false, brain_access: false, message: '', interval_seconds: 60 },
  target_brain: 'work', last_poll: checked, seen: 3,
});

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

/**
 * A switch toggle() made: its label, with the checkbox as .input.
 * @typedef {ReturnType<typeof import('../assets/components/fields.js').toggle>} Switch
 */

/**
 * @param {object} [first] the Plaud status the bridge holds at first
 * @param {object} [connections] what GET /api/connections answers
 */
async function openPage(t, first = status('2026-10-18T10:00:00Z'), connections = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { status: first };
  const calls = serve(t, (path) => {
    if (path.startsWith('/api/plaud/status')) return bridge.status;
    if (path === '/api/connections') return connections;
    if (path === '/api/gitlab') return { configured: false, base_url: '', host: '', token_hint: '', name: '', email: '', where: 'this machine’s data folder' };
    if (path === '/api/git-identities') return { identities: [], nodes: [] };
    if (path.startsWith('/api/brains')) return [{ id: 'work', name: 'Work' }, { id: 'personal', name: 'Personal' }];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const poll = async (next) => { bridge.status = next; await resource('/api/plaud/status').refresh(); await settle(); };
  const note = () => /** @type {HTMLElement} */ (plaudCard(root).querySelector('.live-form').firstChild); // the "changed elsewhere" note
  return { root, ctx, poll, note, calls };
}

// The Plaud card, apart from the GitHub and GitLab cards above it.
const plaudCard = (root) => root.querySelectorAll('section.card').find((c) => c.textContent.includes('Plaud recorder'));

test('the Plaud status keeps refreshing while its settings have edits', async (t) => {
  const { root, ctx, poll, note } = await openPage(t);
  const msg = root.querySelector('textarea');
  type(msg, 'File it under recordings/');
  assert.equal(ctx.dirty(), true);
  await poll({ ...status('2026-10-18T10:05:00Z'), seen: 4 });
  assert.match(root.textContent, /4 recordings known/, 'the status follows the poll');
  assert.equal(root.querySelector('textarea'), msg, 'the settings were not rebuilt');
  assert.equal(msg.value, 'File it under recordings/');
  assert.equal(note().hidden, true, 'a new check is not a change to the settings');

  type(msg, '');
  assert.equal(ctx.dirty(), false, 'typed back, nothing is unsaved');
});

test('edited Plaud settings say when they changed elsewhere', async (t) => {
  const { root, poll, note } = await openPage(t);
  const msg = root.querySelector('textarea');
  type(msg, 'File it under recordings/');
  const now = status('2026-10-18T10:05:00Z');
  await poll({ ...now, settings: { ...now.settings, polished: true } }); // saved in another tab
  assert.equal(root.querySelector('textarea'), msg);
  assert.equal(msg.value, 'File it under recordings/', 'the edits are kept');
  assert.equal(note().hidden, false, 'and the change elsewhere is shown');
});

test('coming back to the page shows the Plaud status it knows at once', async (t) => {
  const first = await openPage(t);
  first.ctx.destroy(); // gone to another page
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  assert.match(root.textContent, /Signed in as you@example\.com/, 'painted before the bridge answers again');
});

test('cancelling the brain_access dialog leaves nothing unsaved', async (t) => {
  const { root, ctx } = await openPage(t);
  /** @type {Switch[]} */
  const switches = root.querySelectorAll('label.switch');
  const access = switches.find((l) => l.textContent.includes('Let brains use')).input;
  access.checked = true;
  access.dispatchEvent(new Event('change', { bubbles: true }));
  await settle();
  assert.equal(ctx.dirty(), true, 'the change is kept while you decide');
  button(document.querySelector('dialog'), 'Cancel').click();
  await settle();
  assert.equal(access.checked, false, 'the switch is back off');
  assert.equal(ctx.dirty(), false, 'and nothing is unsaved');
});

// A card that waits for you to connect again still has a login on record:
// it offers Disconnect, which stops the checks and the daily reminder,
// without signing out a login in ~/.plaud that Fridolin did not make.
for (const c of [
  { name: 'the login expired', over: { connected: false, expired: true }, says: /daily reminder in the chat stops/ },
  {
    name: 'brain_access moved the login',
    over: { connected: false, login_moved: true, login_dir: '/Users/you/.plaud', settings: { ...status('').settings, brain_access: true } },
    says: /login in ~\/\.plaud\/ stays/,
  },
]) {
  test(`the card offers Disconnect when ${c.name}`, async (t) => {
    const { root, calls } = await openPage(t, { ...status('2026-10-18T10:00:00Z'), ...c.over });
    assert.ok(button(root, 'Connect again') || button(root, 'Connect Plaud'), 'connecting again is offered');
    button(root, 'Disconnect').click();
    await settle();
    const dialog = document.querySelector('dialog');
    assert.match(dialog.textContent, c.says);
    button(dialog, 'Disconnect').click(); // confirm
    await settle();
    assert.ok(calls.some((call) => call.path === '/api/plaud/disconnect' && call.method === 'POST'), 'Disconnect was posted');
  });
}

test('a card that was never connected offers Connect, or the phone app instead, but no Disconnect', async (t) => {
  const { root } = await openPage(t, { ...status('2026-10-18T10:00:00Z'), connected: false });
  assert.deepEqual(plaudCard(root).querySelectorAll('button').map((b) => b.textContent), ['Connect Plaud', 'Use the Plaud Sync phone app instead']);
});

test('the brains to pick are named with their ids', async (t) => {
  const now = status('2026-10-18T10:00:00Z');
  const { root } = await openPage(t, { ...now, settings: { ...now.settings, brain: 'personal' }, brain_missing: true });
  const options = root.querySelectorAll('select option').map((o) => [o.value, o.textContent]);
  assert.deepEqual(options.slice(1), [['work', 'Work (work)'], ['personal', 'Personal (personal) (missing)']]);
});

test('Automatic names the brain it picks, not the one picked instead', async (t) => {
  const now = status('2026-10-18T10:00:00Z');
  const { root } = await openPage(t, { ...now, settings: { ...now.settings, brain: 'work' }, target_brain: 'work', auto_brain: 'personal' });
  const automatic = root.querySelectorAll('select option').find((o) => o.value === '');
  assert.equal(automatic.textContent, 'Automatic (Personal)');
});

const toasts = () => document.querySelectorAll('.toast').map((el) => el.textContent);

test('Connect says the sign-in starts, not that a tab opened', async (t) => {
  const { root, calls } = await openPage(t, { cli: { found: true }, connected: false, settings: {} });
  button(root, 'Connect Plaud').click();
  await settle();
  assert.ok(calls.some((c) => c.path === '/api/plaud/connect' && c.method === 'POST'));
  assert.deepEqual(toasts(), ['Starting Plaud’s sign-in…']);
});

test('a sign-in from the connected card shows its progress and its failure', async (t) => {
  const expired = { ...status('2026-10-18T10:00:00Z'), expired: true };
  const { root, poll } = await openPage(t, { ...expired, connect: { state: 'running', auth_url: 'https://web.plaud.ai/platform/oauth?client_id=x' } });
  assert.match(root.textContent, /Approve access in the browser tab Plaud just opened.*\(web\.plaud\.ai\)/);
  assert.equal(button(root, 'Waiting for Plaud…').disabled, true, 'no second sign-in while one runs');
  await poll({ ...expired, connect: { state: 'error', error: 'PORT_IN_USE' } });
  assert.match(root.textContent, /Sign-in did not finish.*PORT_IN_USE/);
  assert.equal(button(root, 'Connect again').disabled, false);
});

test('a connected card with a working login shows no old sign-in failure', async (t) => {
  const { root } = await openPage(t, { ...status('2026-10-18T10:00:00Z'), connect: { state: 'error', error: 'exit status 1' } });
  assert.match(root.textContent, /Signed in as you@example\.com/);
  assert.doesNotMatch(root.textContent, /Sign-in did not finish/);
});

test('Disconnect says it is done', async (t) => {
  const { root } = await openPage(t);
  button(root, 'Disconnect').click();
  await settle();
  button(document.querySelector('dialog'), 'Disconnect').click(); // confirm
  await settle();
  assert.ok(toasts().includes('Plaud disconnected'));
});

// The status changes on its own only with a check, at most every 30 s,
// so the page asks for it seldom, except while a sign-in runs, whose end
// it shows at once.
test('the Plaud status is asked for every 2 s while a sign-in runs', async (t) => {
  const signingIn = { ...status('2026-10-18T10:00:00Z'), connected: false, connect: { state: 'running', auth_url: 'https://web.plaud.ai/platform/oauth?client_id=x' } };
  const { calls } = await openPage(t, signingIn);
  const asked = () => calls.filter((c) => c.path === '/api/plaud/status').length;
  const wait = async (ms) => { t.mock.timers.tick(ms); await settle(); };
  assert.equal(asked(), 1);
  await wait(1999);
  assert.equal(asked(), 1);
  await wait(1);
  assert.equal(asked(), 2, 'again after 2 s while signing in');
  await wait(2000);
  assert.equal(asked(), 3);
});

test('a finished sign-in slows the Plaud status down', async (t) => {
  const signingIn = { ...status('2026-10-18T10:00:00Z'), connected: false, connect: { state: 'running' } };
  const { calls, poll } = await openPage(t, signingIn);
  const asked = () => calls.filter((c) => c.path === '/api/plaud/status').length;
  const wait = async (ms) => { t.mock.timers.tick(ms); await settle(); };
  await poll(status('2026-10-18T10:00:30Z')); // signed in
  const n = asked();
  await wait(19999);
  assert.equal(asked(), n, 'nothing for almost 20 s');
  await wait(1);
  assert.equal(asked(), n + 1, 'then once');
});

test('a phone folder needs no Plaud CLI or login on the card', async (t) => {
  const s = status('2026-10-18T10:00:00Z');
  const { root } = await openPage(t, {
    ...s, cli: { found: false, install_hint: 'npm install -g @plaud-ai/cli' }, account: '',
    settings: { ...s.settings, phone_dir: '/Users/you/PlaudSync' },
  });
  assert.match(root.textContent, /Phone folder/);
  assert.match(root.textContent, /\/Users\/you\/PlaudSync/);
  assert.doesNotMatch(root.textContent, /Install Plaud’s command-line tool|Login stored in|Let brains use|AI-cleaned/);
  assert.equal(button(root, 'Disconnect'), undefined);
  assert.ok(button(plaudCard(root), 'Save') && button(root, 'Check now'));
});

test('the phone app part: keys, the build, its download and the paired phones', async (t) => {
  const s = status('2026-10-18T10:00:00Z');
  const app = {
    credentials: true, site_url: 'https://example.com', built: '2026-10-18T09:00:00Z',
    build: { state: 'done', started: '2026-10-18T08:58:00Z', pairing_expires: '2026-10-19T09:00:00Z' },
    devices: [{ id: 'dev_abc', paired: '2026-10-18T09:10:00Z', last_upload: '2026-10-18T09:20:00Z' }],
  };
  const { root, poll, calls } = await openPage(t, {
    ...s, cli: { found: false, install_hint: 'npm install -g @plaud-ai/cli' }, account: '',
    settings: { ...s.settings, phone_upload: true }, app,
  });
  assert.match(root.textContent, /Phone app/);
  assert.doesNotMatch(root.textContent, /Install Plaud’s command-line tool|Login stored in|AI-cleaned/);
  assert.ok(button(root, 'Download app (APK)') && button(root, 'Build again') && button(root, 'Unpair'));
  assert.match(root.textContent, /dev_abc/);

  const key = plaudCard(root).querySelector('input[type=password]');
  type(key, 'typed');
  await poll({ ...s, cli: { found: false }, account: '', settings: { ...s.settings, phone_upload: true }, app: { ...app, devices: [] } });
  assert.equal(plaudCard(root).querySelector('input[type=password]').value, 'typed', 'a refresh keeps keys being typed');
  assert.match(root.textContent, /No phone is paired yet/);

  button(root, 'Build again').click();
  await settle();
  assert.ok(calls.some((c) => c.path === '/api/plaud/app/build' && c.method === 'POST'), 'Build was posted');
});

test('without a site domain or keys, the app cannot be built', async (t) => {
  const s = status('2026-10-18T10:00:00Z');
  const { root } = await openPage(t, { ...s, settings: { ...s.settings, phone_upload: true }, app: { credentials: false, devices: [] } });
  assert.match(root.textContent, /The site has no domain yet/);
  assert.equal(button(root, 'Build phone app').disabled, true);
  assert.equal(button(root, 'Download app (APK)'), undefined);
});

test('where nothing can build the app, the card says how to add a node that can', async (t) => {
  const s = status('2026-10-18T10:00:00Z');
  const app = { credentials: true, site_url: 'https://example.com', devices: [], no_builder: 'this computer cannot build the app (no JDK)' };
  const { root, poll } = await openPage(t, { ...s, settings: { ...s.settings, phone_upload: true }, app });
  assert.match(root.textContent, /Nothing here can build the app/);
  assert.match(root.textContent, /android-build/);
  assert.equal(button(root, 'Build phone app').disabled, true);

  await poll({ ...s, settings: { ...s.settings, phone_upload: true }, app: { ...app, no_builder: undefined, builds_on: 'mac-mini' } });
  assert.doesNotMatch(root.textContent, /Nothing here can build the app/);
  assert.match(root.textContent, /the node mac-mini builds the app/);
  assert.equal(button(root, 'Build phone app').disabled, false);
});

test('a build on a node shows its progress, then its failure with the log', async (t) => {
  const s = status('2026-10-18T10:00:00Z');
  const app = { credentials: true, site_url: 'https://example.com', devices: [], builds_on: 'mac-mini' };
  const { root, poll } = await openPage(t, {
    ...s, settings: { ...s.settings, phone_upload: true },
    app: { ...app, build: { state: 'running', started: '2026-10-18T09:58:00Z', node: 'mac-mini', stage: 'Building on mac-mini (a few minutes)' } },
  });
  assert.match(root.textContent, /Building on mac-mini \(a few minutes\)…/);
  assert.equal(button(root, 'Building… (a few minutes)').disabled, true);

  await poll({
    ...s, settings: { ...s.settings, phone_upload: true },
    app: { ...app, build: { state: 'error', started: '2026-10-18T09:58:00Z', node: 'mac-mini', error: 'the build failed: exit status 1', log: 'FAILURE: Build failed' } },
  });
  assert.match(root.textContent, /Building the app on mac-mini failed/);
  assert.match(root.textContent, /FAILURE: Build failed/);
});

test('voice mode is offered with the phone app, and its brain once it is on', async (t) => {
  const s = status('2026-10-18T10:00:00Z');
  const app = { credentials: true, site_url: 'https://example.com', devices: [] };
  const { root, poll, calls } = await openPage(t, { ...s, settings: { ...s.settings, phone_upload: true }, app });
  assert.match(root.textContent, /Voice mode: the phone app may ask by voice/);
  assert.doesNotMatch(root.textContent, /Brain for voice questions/);

  await poll({ ...s, settings: { ...s.settings, phone_upload: true, voice: true, voice_brain: 'personal' }, app });
  assert.match(root.textContent, /Brain for voice questions/);
  assert.match(root.textContent, /go to the phone only, not WhatsApp/);

  button(plaudCard(root), 'Save').click();
  await settle();
  const saved = calls.find((c) => c.path === '/api/plaud/settings' && c.method === 'PUT');
  assert.equal(saved.body.voice, true);
  assert.equal(saved.body.voice_brain, 'personal');
  assert.equal(saved.body.voice_quiet, false);
});

test('without the phone app there is no voice mode', async (t) => {
  const { root } = await openPage(t);
  assert.doesNotMatch(root.textContent, /Voice mode/);
});

test('the OpenAI and ElevenLabs cards follow one request, after Plaud', async (t) => {
  const conn = (id, title) => ({ id, title, configured: false, key_hint: '', ops: [], brains: [], brain_monthly_usd: 0 });
  const { root, ctx, calls } = await openPage(t, status('2026-10-18T10:00:00Z'), {
    month: '2026-10', connections: [conn('openai', 'OpenAI'), conn('elevenlabs', 'ElevenLabs')], modules: [], usage: [],
  });
  assert.equal(calls.filter((c) => c.path === '/api/connections').length, 1, 'both cards share the one resource');
  const titles = root.querySelectorAll('section.card h2').map((x) => x.textContent);
  assert.deepEqual(titles, ['GitHub', 'GitLab', 'Plaud recorder', 'OpenAI', 'ElevenLabs']);
  assert.equal(ctx.dirty(), false);
  const openai = root.querySelectorAll('section.card').find((c) => c.querySelector('h2').textContent === 'OpenAI');
  type(openai.querySelector('input[type=password]'), 'sk-test-typed');
  assert.equal(ctx.dirty(), true, 'a key being typed is unsaved, and leaving asks first');
});

test('a GitLab token being typed is unsaved too', async (t) => {
  const { root, ctx } = await openPage(t);
  assert.equal(ctx.dirty(), false);
  const gitlab = root.querySelectorAll('section.card').find((c) => c.querySelector('h2').textContent === 'GitLab');
  type(gitlab.querySelector('input[type=password]'), 'test-gitlab-token-typed');
  assert.equal(ctx.dirty(), true);
});
