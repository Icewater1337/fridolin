import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { calendarCard, googleClientSetup } from '../assets/components/calendar.js';

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
