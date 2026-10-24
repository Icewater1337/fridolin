// The connect screen names the sign-in steps for the way the bridge runs:
// a container core has no host data folder with its token in it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { connectScreen } from '../assets/app/connect.js';

async function screen(t, ready) {
  installDom();
  noConsoleErrors(t);
  serve(t, (path) => (path === '/ready' ? ready : { status: 404, body: { error: 'not found' } }));
  const el = document.createElement('div');
  document.body.append(el);
  connectScreen(el, { onConnected: () => {} });
  await settle();
  return el.querySelector('.steps-list').textContent;
}

test('a native bridge: fridolin open and the token in the data folder', async (t) => {
  const text = await screen(t, { ready: true, whatsapp: 'connected', container: false });
  assert.match(text, /In a terminal on this computer, runfridolin open/);
  assert.match(text, /~\/\.fridolin\/dashboard\.token/);
  assert.doesNotMatch(text, /docker exec/);
});

test('a container core: docker exec, !dashboard, and the token in the volume', async (t) => {
  const text = await screen(t, { ready: true, whatsapp: 'connected', container: true });
  assert.match(text, /docker exec fridolin-core fridolin open --print/);
  assert.match(text, /!dashboard/);
  assert.match(text, /docker exec fridolin-core cat \/data\/dashboard\.token/);
  assert.doesNotMatch(text, /~\/\.fridolin/, 'never the host home folder');
});

test('a container core that is not ready yet answers 503, with the same body', async (t) => {
  const text = await screen(t, { status: 503, body: { ready: false, whatsapp: 'pairing', container: true } });
  assert.match(text, /docker exec fridolin-core fridolin open --print/);
});

test('without an answer from /ready, the native steps stay', async (t) => {
  const text = await screen(t, { status: 500, body: 'boom' });
  assert.match(text, /fridolin open/);
  assert.doesNotMatch(text, /docker exec/);
});
