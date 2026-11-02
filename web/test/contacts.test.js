import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { contactsCard } from '../assets/components/calendar.js';

// setImmediate, because the tests mock setTimeout (the toasts' and the
// requests' timers) so nothing waits for them.
const tick = () => new Promise((r) => setImmediate(r));
const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const scope = 'https://www.googleapis.com/auth/contacts.readonly';
const status = (more) => ({ client_configured: true, connected: true, scope, token_path: '/Users/you/.fridolin/google_contacts_token_core.json', ...more });

test('Google Contacts: the one-time steps and Connect, then whether it works', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = contactsCard('core');
  document.body.append(card);
  assert.equal(net.calls[0].url, '/api/brains/core/contacts?check=1');
  net.answer(0, status({ connected: false }));
  await tick();
  assert.ok(card.textContent.includes('Enable the People API'), card.textContent);
  assert.ok(card.textContent.includes(scope), 'the scope to add on the consent screen');
  assert.ok(card.querySelectorAll('a').some((a) => (a.getAttribute('href') || '').includes('apiid=people.googleapis.com')), 'a link that enables the People API');
  assert.ok(button(card, 'Connect Google Contacts'));

  const broken = contactsCard('core');
  document.body.append(broken);
  net.answer(1, status({ works: false, check_error: 'Google\'s People API 403: People API has not been used. The People API is not enabled' }));
  await tick();
  assert.ok(broken.textContent.includes('Google did not answer a search'), broken.textContent);
  assert.ok(broken.textContent.includes('People API is not enabled'), broken.textContent);
  assert.ok(button(broken, 'Reconnect'));

  const working = contactsCard('core');
  document.body.append(working);
  net.answer(2, status({ works: true }));
  await tick();
  assert.ok(working.textContent.includes('Working'), working.textContent);
  assert.ok(!working.textContent.includes('Enable the People API'), 'no steps once it works');
});

test('Google Contacts: without a Google client it points to the setup', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = contactsCard('core');
  document.body.append(card);
  net.answer(0, status({ client_configured: false, connected: false }));
  await tick();
  assert.ok(card.textContent.includes('Settings → Google sign-in'), card.textContent);
  assert.equal(button(card, 'Connect Google Contacts'), undefined);
});

test('Google Contacts: Disconnect asks first, and offers to delete the token when Google does not confirm', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const card = contactsCard('core');
  document.body.append(card);
  net.answer(0, status({ works: true }));
  await tick();
  button(card, 'Disconnect').click();
  await tick();
  button(document.querySelector('dialog'), 'Disconnect').click();
  await tick();
  assert.equal(net.calls[1].init.method, 'DELETE');
  assert.equal(net.calls[1].url, '/api/brains/core/contacts');
  net.answer(1, { error: 'the revocation was not confirmed by Google: 503 Service Unavailable' }, 502);
  await tick();
  await tick();
  button(document.querySelectorAll('dialog').at(-1), 'Delete the token').click();
  await tick();
  assert.equal(net.calls[2].url, '/api/brains/core/contacts?local_only=1');
  net.answer(2, { status: 'disconnected', revoked: false, note: '' });
  await tick();
  assert.equal(net.calls[3].url, '/api/brains/core/contacts?check=1', 'the card reloads');
  net.answer(3, status({ connected: false }));
  await tick();
  assert.ok(button(card, 'Connect Google Contacts'), 'and shows the result');
});
