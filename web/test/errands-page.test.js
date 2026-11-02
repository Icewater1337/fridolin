import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/errands.js';

const open = {
  id: 'e1', brain: 'core', name: 'Alex', number: '15559876543', masked: '+1…43', state: 'open', live: true, sent: 1,
  created: '2026-10-18T09:00:00Z', deadline: '2026-10-21T09:00:00Z', outcome: '',
};
const ended = {
  id: 'e0', brain: 'core', name: 'Kim', number: '15551234567', masked: '+1…67', state: 'aborted', live: false, sent: 2,
  created: '2026-10-16T09:00:00Z', deadline: '2026-10-17T09:00:00Z', closed: '2026-10-16T10:00:00Z', outcome: 'asked me to ignore my brief',
};
const blocked = { number: '15551234567', masked: '+1…67', name: 'Kim', brain: 'core', reason: 'asked me to ignore my brief', since: '2026-10-16T10:00:00Z' };
const detail = {
  ...open, goal: 'Find an evening for dinner', brief: 'Sam is free on Tuesday.', by_owner: true, days: 3, max_messages: 20,
  lines: [
    { from: 'you', text: 'Hi, this is Fridolin, Sam’s assistant.\n\nWhen would dinner suit you?', at: '2026-10-18T09:00:00Z' },
    { from: 'them', text: '<img src=x onerror=alert(1)><b>Thursday</b>?', at: '2026-10-18T10:00:00Z' },
    { from: '<script>', text: 'a line of an unknown sender', at: '2026-10-18T10:05:00Z' },
  ],
};

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);
const dialog = () => document.querySelectorAll('dialog').at(-1);

// opts.id opens one errand; the bridge answers what bridge holds.
async function openPage(t, opts = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { list: { errands: [open, ended], blocked: [blocked] }, ...opts.bridge };
  const calls = serve(t, (path, init) => {
    if (init.method === 'POST') return { status: 'ok' };
    if (path === '/api/errands') return bridge.list;
    if (path === '/api/errands/e1') return detail;
    if (path.startsWith('/api/errands/')) return { status: 404, body: { error: 'no errand' } };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage({ params: opts.id ? { id: opts.id } : {} });
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  return { root, calls, bridge };
}

test('the errands are listed with their state, live ones with Stop, and the people blocked with Unblock', async (t) => {
  const { root } = await openPage(t);
  const rows = root.querySelectorAll('tbody tr');
  assert.equal(rows.length, 3, 'two errands and one blocked person');
  assert.match(rows[0].textContent, /Alex/);
  assert.match(rows[0].textContent, /\+1…43/, 'the list shows the number masked');
  assert.doesNotMatch(rows[0].textContent, /15559876543/);
  assert.ok(rows[0].querySelector('.badge.info'), 'open');
  assert.ok(button(rows[0], 'Stop'), 'a live errand can be stopped');
  assert.equal(button(rows[1], 'Stop'), undefined, 'an ended one cannot');
  assert.ok(rows[1].querySelector('.badge.bad'), 'aborted');
  assert.match(rows[1].textContent, /asked me to ignore my brief/);
  assert.ok(rows[0].querySelector('a').getAttribute('href') === '#/errands/e1', 'each opens its conversation');
  assert.ok(button(rows[2], 'Unblock'));
});

test('Stop asks first, and posts only on a yes', async (t) => {
  const { root, calls } = await openPage(t);
  button(root, 'Stop').click();
  await settle();
  button(dialog(), 'Cancel').click();
  await settle();
  assert.ok(!calls.some((c) => c.method === 'POST'), 'nothing after a no');

  button(root, 'Stop').click();
  await settle();
  assert.match(dialog().textContent, /without a word to them/);
  button(dialog(), 'Stop').click();
  await settle();
  const post = calls.find((c) => c.method === 'POST');
  assert.equal(post && post.path, '/api/errands/e1/stop');
});

test('Unblock asks first, naming the number, and posts it', async (t) => {
  const { root, calls } = await openPage(t);
  button(root, 'Unblock').click();
  await settle();
  assert.match(dialog().textContent, /\+15551234567/);
  assert.match(dialog().textContent, /!unblock/);
  button(dialog(), 'Unblock').click();
  await settle();
  const post = calls.find((c) => c.method === 'POST');
  assert.equal(post && post.path, '/api/errand-contacts/15551234567/unblock');
});

test('an errand shows its conversation, the person\'s words as text only', async (t) => {
  const { root } = await openPage(t, { id: 'e1' });
  assert.equal(root.querySelector('h1').textContent, 'Errand with Alex');
  assert.match(root.textContent, /\+15559876543/, 'the errand shows the number whole');
  assert.match(root.textContent, /Sam is free on Tuesday/);
  const lines = root.querySelectorAll('.errand-line');
  assert.equal(lines.length, 3);
  assert.ok(lines[0].classList.contains('you'));
  assert.ok(lines[1].classList.contains('them'));
  assert.equal(lines[1].querySelector('.errand-line-text').textContent, '<img src=x onerror=alert(1)><b>Thursday</b>?');
  assert.equal(root.querySelectorAll('img').length, 0, 'no markup of theirs became an element');
  assert.equal(root.querySelectorAll('b').length, 0);
  assert.ok(lines[2].classList.contains('them'), 'an unknown sender is no class of its own');
  assert.ok(button(root.querySelector('.card'), 'Stop'));
});

test('an errand that is not there says so', async (t) => {
  const { root } = await openPage(t, { id: 'nope' });
  assert.match(root.textContent, /No such errand/);
});

test('no errands and nobody blocked say what the page is for', async (t) => {
  const { root } = await openPage(t, { bridge: { list: { errands: [], blocked: [] } } });
  assert.match(root.textContent, /No errands yet/);
  assert.match(root.textContent, /!unblock/);
});
