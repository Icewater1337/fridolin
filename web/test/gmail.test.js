import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { gmailCard } from '../assets/components/calendar.js';

// setImmediate, because the tests mock setTimeout (the toasts' and the
// requests' timers) so nothing waits for them.
const tick = () => new Promise((r) => setImmediate(r));
const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const scope = 'https://mail.google.com/';
const status = (more) => ({ client_configured: true, connected: true, scope, token_path: '/Users/you/.fridolin/google_gmail_token_personal.json', ...more });

test('Gmail: its own sign-in, with the one-time steps and Connect, then whether it works', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = gmailCard('personal');
  document.body.append(card);
  assert.equal(net.calls[0].url, '/api/brains/personal/gmail?check=1');
  net.answer(0, status({ connected: false }));
  await tick();
  assert.ok(card.textContent.includes('Enable the Gmail API'), card.textContent);
  assert.ok(/https:\/\/mail\.google\.com\//.test(card.textContent), 'the scope to add on the consent screen');
  assert.ok(card.querySelectorAll('a').some((a) => (a.getAttribute('href') || '').includes('apiid=gmail.googleapis.com')), 'a link that enables the Gmail API');
  assert.ok(card.textContent.includes('the calendar stays as it is'), card.textContent);
  button(card, 'Connect Gmail').click();
  await tick();
  assert.equal(net.calls[1].url, '/api/brains/personal/gmail/connect', 'not the calendar’s sign-in');
  assert.equal(net.calls[1].init.method, 'POST');

  const broken = gmailCard('personal');
  document.body.append(broken);
  net.answer(2, status({ works: false, check_error: 'Gmail 403: Gmail API has not been used. The Gmail API is not enabled' }));
  await tick();
  assert.ok(broken.textContent.includes('Gmail did not answer'), broken.textContent);
  assert.ok(broken.textContent.includes('Gmail API is not enabled'), broken.textContent);
  assert.ok(button(broken, 'Reconnect'));

  const working = gmailCard('personal');
  document.body.append(working);
  net.answer(3, status({ works: true, account: 'you@example.com' }));
  await tick();
  assert.ok(working.textContent.includes('Working'), working.textContent);
  assert.ok(working.textContent.includes('you@example.com'), 'the mailbox');
  assert.ok(!working.textContent.includes('Enable the Gmail API'), 'no steps once it works');
});

test('Gmail: without a Google client it points to the setup', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = gmailCard('personal');
  document.body.append(card);
  net.answer(0, status({ client_configured: false, connected: false }));
  await tick();
  assert.ok(card.textContent.includes('Settings → Google sign-in'), card.textContent);
  assert.equal(button(card, 'Connect Gmail'), undefined);
});

test('Gmail: Disconnect asks first, and offers to delete the token when Google does not confirm', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = gmailCard('personal');
  document.body.append(card);
  net.answer(0, status({ works: true }));
  await tick();
  button(card, 'Disconnect').click();
  await tick();
  button(document.querySelector('dialog'), 'Disconnect').click();
  await tick();
  assert.equal(net.calls[1].init.method, 'DELETE');
  assert.equal(net.calls[1].url, '/api/brains/personal/gmail');
  net.answer(1, { error: 'the revocation was not confirmed by Google: 503 Service Unavailable' }, 502);
  await tick();
  await tick();
  button(document.querySelectorAll('dialog').at(-1), 'Delete the token').click();
  await tick();
  assert.equal(net.calls[2].url, '/api/brains/personal/gmail?local_only=1');
  net.answer(2, { status: 'disconnected', revoked: false, note: '' });
  await tick();
  assert.equal(net.calls[3].url, '/api/brains/personal/gmail?check=1', 'the card reloads');
  net.answer(3, status({ connected: false }));
  await tick();
  assert.ok(button(card, 'Connect Gmail'));
});
