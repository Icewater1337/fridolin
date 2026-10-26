import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import { connectionCard } from '../assets/components/connection-card.js';

/** @import { Connection, Connections } from '../types/api.js' */

const KEY = 'sk-test-0123456789abcdefa1b2';

/** @returns {Connection} */
const openai = (over = {}) => ({
  id: 'openai', title: 'OpenAI', configured: false, key_hint: '', organization: '', project: '',
  ops: ['transcribe', 'responses'], brains: [], brain_monthly_usd: 0, ...over,
});

/** @returns {Connection} */
const elevenlabs = (over = {}) => ({
  id: 'elevenlabs', title: 'ElevenLabs', configured: false, key_hint: '', voice: '',
  ops: ['tts', 'transcribe', 'voices'], brains: [], brain_monthly_usd: 0, ...over,
});

/** @returns {Connections} */
const answer = (over = {}) => ({ month: '2026-10', connections: [openai(), elevenlabs()], modules: [], usage: [], ...over });

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const toasts = () => document.querySelectorAll('.toast').map((el) => el.textContent);
/** @type {(root: any, label: string) => HTMLInputElement | undefined} */
const inputFor = (root, label) => {
  const l = root.querySelectorAll('label').find((x) => x.textContent.startsWith(label));
  return l ? /** @type {HTMLInputElement} */ (document.getElementById(l.getAttribute('for'))) : undefined;
};
/**
 * A switch toggle() made: its label, with the checkbox as .input.
 * @typedef {ReturnType<typeof import('../assets/components/fields.js').toggle>} Switch
 */
/** @type {(root: any, label: string) => Switch} */
const brainSwitch = (root, label) => root.querySelectorAll('label.switch').find((l) => l.textContent === label);

/**
 * The card of id on a page, with a stand-in bridge that holds first and
 * keeps what the card saves.
 * @param {import('node:test').TestContext} t
 * @param {'openai' | 'elevenlabs'} id
 * @param {Connections} first
 * @param {{check?: {ok: boolean, detail?: string, error?: string}}} [options] what a check answers
 */
async function openCard(t, id, first, { check = { ok: true, detail: 'the key works' } } = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { data: structuredClone(first), check };
  const entry = () => bridge.data.connections.find((c) => c.id === id);
  const calls = serve(t, (path, init) => {
    const method = init.method || 'GET';
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (path === '/api/connections' && method === 'GET') return structuredClone(bridge.data);
    if (path === '/api/brains') return [{ id: 'personal', name: 'Personal' }, { id: 'work', name: 'Work' }];
    if (path === `/api/connections/${id}` && method === 'PUT') {
      const c = entry();
      if (!body.api_key && !c.configured) return { status: 400, body: { error: 'set a key first' } };
      if (body.api_key) Object.assign(c, { configured: true, key_hint: `…${body.api_key.slice(-4)}` });
      if (body.organization !== undefined) c.organization = body.organization;
      if (body.project !== undefined) c.project = body.project;
      return c;
    }
    if (path === `/api/connections/${id}` && method === 'DELETE') {
      Object.assign(entry(), { configured: false, key_hint: '', organization: '', project: '' });
      return { status: 204 };
    }
    if (path === `/api/connections/${id}/brains` && method === 'PUT') {
      Object.assign(entry(), { brains: body.brains, brain_monthly_usd: body.brain_monthly_usd }, body.voice !== undefined ? { voice: body.voice } : {});
      return entry();
    }
    if (path === `/api/connections/${id}/check` && method === 'POST') return bridge.check;
    return { status: 404, body: { error: 'not found' } };
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  const card = connectionCard(ctx, id);
  root.append(card);
  await resource('/api/connections').refresh();
  await settle();
  /** @param {Connections} next */
  const poll = async (next) => { bridge.data = structuredClone(next); await resource('/api/connections').refresh(); await settle(); };
  const sent = (path, method) => calls.filter((c) => c.path === path && c.method === method);
  return { root, card, bridge, calls, sent, poll };
}

test('a connection without a key says what it is for and saves one', async (t) => {
  const { root, card, sent } = await openCard(t, 'openai', answer());
  assert.match(root.textContent, /Not set up/);
  assert.match(root.textContent, /Site modules and brains use OpenAI through Fridolin .*the key stays here\./);
  assert.equal(button(root, 'Check'), undefined, 'nothing to check without a key');
  assert.equal(button(root, 'Remove'), undefined);

  button(root, 'Save key').click();
  await settle();
  assert.deepEqual(toasts(), ['Paste the OpenAI key first']);
  assert.equal(sent('/api/connections/openai', 'PUT').length, 0, 'nothing is sent without a key');

  const key = root.querySelector('input[type=password]');
  type(key, KEY);
  type(inputFor(root, 'Organization'), ' org-test ');
  type(inputFor(root, 'Project'), 'proj_test');
  assert.equal(card.dirty(), true, 'a key being typed counts as unsaved');
  button(root, 'Save key').click();
  await settle();
  assert.deepEqual(sent('/api/connections/openai', 'PUT').map((c) => c.body), [{ api_key: KEY, organization: 'org-test', project: 'proj_test' }]);
  assert.equal(card.dirty(), false);
  assert.match(root.textContent, /Key set/);
  assert.match(root.textContent, /Key ending …a1b2\. Organization org-test\. Project proj_test\./);
  assert.ok(toasts().includes('OpenAI key saved'));
  // The key went to the bridge, and is nowhere on the page now.
  assert.equal(root.textContent.includes(KEY), false);
  assert.ok(document.querySelectorAll('input').every((i) => i.value !== KEY), 'no field holds the key once it is saved');
});

test('a blank key is no key', async (t) => {
  const { root, card } = await openCard(t, 'elevenlabs', answer());
  const key = root.querySelector('input[type=password]');
  type(key, '   ');
  button(root, 'Save key').click();
  await settle();
  assert.deepEqual(toasts(), ['Paste the ElevenLabs key first'], 'blank is no key');
  assert.equal(card.dirty(), true);
});

test('a connection with a key checks it, replaces it and removes it', async (t) => {
  const first = answer({ connections: [openai({ configured: true, key_hint: '…a1b2', brains: ['personal'] }), elevenlabs()] });
  const { root, bridge, sent, poll } = await openCard(t, 'openai', first);
  assert.match(root.textContent, /Key ending …a1b2\./);
  assert.equal(root.querySelector('input[type=password]'), null, 'the key form is closed');

  button(root, 'Check').click();
  await settle();
  assert.equal(sent('/api/connections/openai/check', 'POST').length, 1);
  assert.ok(toasts().includes('OpenAI: the key works'));

  bridge.check = { ok: false, error: 'OpenAI says the key is wrong (401)' };
  button(root, 'Check').click();
  await settle();
  assert.match(root.textContent, /The key did not work.*OpenAI says the key is wrong \(401\)/);
  // A key replaced elsewhere is not the one that failed.
  await poll(answer({ connections: [openai({ configured: true, key_hint: '…9z9z', brains: ['personal'] }), elevenlabs()] }));
  assert.match(root.textContent, /Key ending …9z9z\./);
  assert.doesNotMatch(root.textContent, /The key did not work/);

  button(root, 'Check').click();
  await settle();
  assert.match(root.textContent, /The key did not work/);
  // Replace key opens the same form; left empty, it keeps the key, and
  // what changed makes the failed check old news.
  button(root, 'Replace key').click();
  await settle();
  const key = root.querySelector('input[type=password]');
  assert.equal(key.getAttribute('placeholder'), 'saved — type to replace');
  type(inputFor(root, 'Project'), 'proj_new');
  button(root, 'Save key').click();
  await settle();
  assert.deepEqual(sent('/api/connections/openai', 'PUT').map((c) => c.body), [{ api_key: '', project: 'proj_new' }], 'only what changed');
  assert.ok(toasts().includes('OpenAI settings saved'));
  assert.equal(root.querySelector('input[type=password]'), null, 'closed again');
  assert.match(root.textContent, /Key ending …9z9z\. Project proj_new\./);
  assert.doesNotMatch(root.textContent, /The key did not work/);

  button(root, 'Remove').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.match(dialog.textContent, /Remove the OpenAI key\?/);
  assert.match(dialog.textContent, /Which brains may use it, and this month’s usage, stay\./);
  button(dialog, 'Remove').click();
  await settle();
  assert.equal(sent('/api/connections/openai', 'DELETE').length, 1);
  assert.ok(toasts().includes('OpenAI key removed'));
  assert.match(root.textContent, /Not set up/);
  assert.ok(root.querySelector('input[type=password]'), 'the form to set a key is back');
});

test('Cancel closes the replace form and forgets what was typed', async (t) => {
  const first = answer({ connections: [openai({ configured: true, key_hint: '…a1b2' }), elevenlabs()] });
  const { root, card } = await openCard(t, 'openai', first);
  button(root, 'Replace key').click();
  await settle();
  type(root.querySelector('input[type=password]'), 'sk-test-typed-but-cancelled');
  assert.equal(card.dirty(), true);
  button(root, 'Cancel').click();
  await settle();
  assert.equal(card.dirty(), false);
  assert.equal(root.querySelector('input[type=password]'), null);
  assert.ok(document.querySelectorAll('input').every((i) => i.value === '' || !i.value.startsWith('sk-test')), 'the typed key is gone');
});

test('the brains that may use a connection, with their monthly limit', async (t) => {
  const first = answer({ connections: [openai({ configured: true, key_hint: '…a1b2', brains: ['personal'], brain_monthly_usd: 10 }), elevenlabs()] });
  const { root, card, sent, poll } = await openCard(t, 'openai', first);
  assert.equal(brainSwitch(root, 'Personal (personal)').input.checked, true);
  assert.equal(brainSwitch(root, 'Work (work)').input.checked, false);
  assert.match(root.textContent, /openai_transcribe/);
  const limit = inputFor(root, 'Monthly limit per brain (USD, estimate)');
  assert.equal(limit.value, '10');
  assert.equal(inputFor(root, 'Default voice'), undefined, 'OpenAI has no voice');

  const work = brainSwitch(root, 'Work (work)').input;
  work.checked = true;
  work.dispatchEvent(new Event('change', { bubbles: true }));
  type(limit, '12.5');
  assert.equal(card.dirty(), true);

  // A refresh with new usage keeps the edits.
  await poll({ ...first, usage: [{ connection: 'openai', caller: 'brain:personal', requests: 1, errors: 0, audio_seconds: 30, input_tokens: 0, output_tokens: 0, characters: 0, usd: 0.01, limit_usd: 10 }] });
  assert.equal(brainSwitch(root, 'Work (work)').input.checked, true);
  assert.equal(inputFor(root, 'Monthly limit per brain (USD, estimate)').value, '12.5');

  button(root.querySelector('.connection-part'), 'Save').click();
  await settle();
  assert.deepEqual(sent('/api/connections/openai/brains', 'PUT').map((c) => c.body), [{ brains: ['personal', 'work'], brain_monthly_usd: 12.5 }]);
  assert.ok(toasts().includes('Saved which brains may use OpenAI'));
  assert.equal(card.dirty(), false);
});

test('a limit that is no amount is not sent', async (t) => {
  const { root, sent } = await openCard(t, 'openai', answer());
  type(inputFor(root, 'Monthly limit per brain (USD, estimate)'), '-3');
  button(root.querySelector('.connection-part'), 'Save').click();
  await settle();
  assert.equal(sent('/api/connections/openai/brains', 'PUT').length, 0);
  assert.deepEqual(toasts(), ['The monthly limit is an amount in dollars, or empty for none']);
});

test('ElevenLabs saves its default voice with the brains', async (t) => {
  const first = answer({ connections: [openai(), elevenlabs({ configured: true, key_hint: '…c3d4', brains: ['work'], voice: 'voice-old' })] });
  const { root, sent } = await openCard(t, 'elevenlabs', first);
  assert.match(root.textContent, /elevenlabs_speak, elevenlabs_transcribe, elevenlabs_voices/);
  assert.equal(inputFor(root, 'Organization'), undefined, 'ElevenLabs has no organization');
  const voice = inputFor(root, 'Default voice');
  assert.equal(voice.value, 'voice-old');
  type(voice, ' voice-new ');
  button(root.querySelector('.connection-part'), 'Save').click();
  await settle();
  assert.deepEqual(sent('/api/connections/elevenlabs/brains', 'PUT').map((c) => c.body), [{ brains: ['work'], brain_monthly_usd: 0, voice: 'voice-new' }]);
});

test('a brain that is gone keeps its switch, named as gone', async (t) => {
  const first = answer({ connections: [openai({ configured: true, key_hint: '…a1b2', brains: ['old'] }), elevenlabs()] });
  const { root } = await openCard(t, 'openai', first);
  assert.equal(brainSwitch(root, 'old (no such brain)').input.checked, true);
});

test('the modules that use a connection and this month’s usage, as estimates', async (t) => {
  const first = answer({
    connections: [openai({ configured: true, key_hint: '…a1b2', brain_monthly_usd: 10 }), elevenlabs()],
    modules: [
      { module: 'hands-study', connection: 'openai', use: ['transcribe', 'responses'], models: ['gpt-4o-transcribe'], monthly_usd: 20 },
      { module: 'notes', connection: 'openai', use: ['responses'], models: [], monthly_usd: 0 },
      { module: 'reader', connection: 'elevenlabs', use: ['tts'], models: [], monthly_usd: 5 },
    ],
    usage: [
      { connection: 'openai', caller: 'module:hands-study', requests: 12, errors: 1, audio_seconds: 340.5, input_tokens: 1200, output_tokens: 800, characters: 0, usd: 0.05, limit_usd: 20 },
      { connection: 'openai', caller: 'brain:personal', requests: 3, errors: 0, audio_seconds: 0, input_tokens: 0, output_tokens: 0, characters: 0, usd: 10.2, limit_usd: 10 },
      { connection: 'elevenlabs', caller: 'module:reader', requests: 2, errors: 0, audio_seconds: 0, input_tokens: 0, output_tokens: 0, characters: 3400, usd: 0.9, limit_usd: 5 },
    ],
  });
  const { root } = await openCard(t, 'openai', first);
  const text = root.textContent;
  assert.match(text, /hands-study — transcribe, responses · models gpt-4o-transcribe · up to \$20 a month/);
  assert.match(text, /notes — responses · any model · no monthly limit/);
  assert.doesNotMatch(text, /reader/, 'only the modules of this connection');

  assert.match(text, /Usage in October 2026/);
  const rows = root.querySelectorAll('tbody tr').map((r) => r.querySelectorAll('td').map((td) => td.textContent));
  assert.deepEqual(rows, [
    ['Site module hands-study', '12 (1 failed)', '5 min 41 s of audio · 1,200 tokens in, 800 out', '$0.05 of $20'],
    ['Brain Personal (personal)', '3', '—', '$10.20 of $10 over its limit'],
  ]);
  assert.match(text, /About \$10\.25 in all\./);
  assert.match(text, /estimates from a price table of its own, not OpenAI’s bill\. OpenAI’s own project budget is the hard limit/);
});

test('a key not set while live modules use the connection is a warning', async (t) => {
  const first = answer({ modules: [{ module: 'hands-study', connection: 'openai', use: ['transcribe'], models: [], monthly_usd: 20 }] });
  const { root } = await openCard(t, 'openai', first);
  assert.match(root.textContent, /1 live module uses OpenAI.*Their calls fail until a key is set\./);
  assert.equal(root.querySelector('table'), null, 'no usage to show');
});

test('nothing used yet, and no module, says so', async (t) => {
  const first = answer({ connections: [openai(), elevenlabs({ configured: true, key_hint: '…c3d4' })] });
  const { root } = await openCard(t, 'elevenlabs', first);
  assert.match(root.textContent, /No live site module uses ElevenLabs\./);
  assert.match(root.textContent, /capabilities\.elevenlabs/);
  assert.match(root.textContent, /Nothing used yet this month\./);
  assert.match(root.textContent, /Your ElevenLabs plan’s credits are the hard limit\./);
});

test('a connection the bridge does not offer shows no card', async (t) => {
  const { card } = await openCard(t, 'elevenlabs', answer({ connections: [openai()] }));
  assert.equal(card.hidden, true);
});
