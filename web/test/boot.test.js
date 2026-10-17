// theme.js watches the boot: if the app never marks the page ready, the
// "Loading dashboard…" message turns into an error the user can act on.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { installDom } from './fake-dom.js';

const source = fs.readFileSync(new URL('../assets/theme.js', import.meta.url), 'utf8');

function boot(t) {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const msg = document.createElement('div');
  msg.className = 'boot-msg';
  msg.textContent = 'Loading dashboard…';
  document.body.append(msg);
  // Events reach window's capturing listeners with the element that failed
  // as their target, as a browser does.
  const listeners = [];
  const window = { addEventListener: (type, fn) => listeners.push([type, fn]) };
  vm.runInNewContext(source, { window, document, localStorage: { getItem: () => 'dark' }, setTimeout });
  const fire = (type, props = {}) => listeners.forEach(([t, fn]) => { if (t === type) fn({ type, target: window, ...props }); });
  return { document, msg, fire };
}

test('the saved theme applies before the page paints', (t) => {
  const { document } = boot(t);
  assert.equal(document.documentElement.getAttribute('data-theme'), 'dark');
});

test('a module that fails to load is reported', (t) => {
  const { msg, fire } = boot(t);
  fire('error', { target: { tagName: 'SCRIPT' } });
  assert.match(msg.textContent, /failed to load/);
});

test('a module that throws while loading is reported', (t) => {
  const { msg, fire } = boot(t);
  fire('error', { error: new URIError('URI malformed') });
  assert.match(msg.textContent, /failed to load/);
});

test('a broken image is not a failed boot', (t) => {
  const { msg, fire } = boot(t);
  fire('error', { target: { tagName: 'IMG' } });
  assert.equal(msg.textContent, 'Loading dashboard…');
});

test('an app that never starts is reported after a while', (t) => {
  const { msg, fire } = boot(t);
  fire('load');
  t.mock.timers.tick(9000);
  assert.equal(msg.textContent, 'Loading dashboard…');
  t.mock.timers.tick(2000);
  assert.match(msg.textContent, /failed to load/);
});

test('nothing is reported once the app is ready', (t) => {
  const { document, msg, fire } = boot(t);
  document.documentElement.setAttribute('data-ready', '1');
  fire('error', { error: new Error('later') });
  fire('load');
  t.mock.timers.tick(11000);
  assert.equal(msg.textContent, 'Loading dashboard…');
});
