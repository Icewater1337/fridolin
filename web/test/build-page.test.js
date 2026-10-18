import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/build.js';

const message = (role, text, at) => ({ role, text, at });
const chat = (over = {}) => ({
  messages: [message('user', 'Add an events page', '2026-10-18T10:00:00Z')],
  thread: 1, running: false, queued: 0, activity: [],
  preview: { local: 'http://127.0.0.1:5301/_fridolin/preview-login?token=one' },
  proposal: { brain: 'web', title: 'Events page', at: '2026-10-18T10:05:00Z' },
  ...over,
});

async function openPage(t, first = chat()) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { chat: first };
  const calls = serve(t, (path) => {
    if (path === '/api/site/builders') return { brains: [{ id: 'web', name: 'Web', builder: true }, { id: 'docs', name: 'docs', builder: true }, { id: 'home', name: 'Home', builder: false }] };
    if (path.startsWith('/api/site/build/')) return bridge.chat;
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  // The page asks again after 5 s, or 1.5 s while the brain works.
  const poll = async (next, ms = 5000) => { bridge.chat = next; t.mock.timers.tick(ms); await settle(); };
  const nodes = () => root.querySelectorAll('*');
  return { root, ctx, bridge, calls, poll, nodes };
}

test('an identical answer replaces no nodes', async (t) => {
  const cases = [
    ['idle', chat()],
    ['working', chat({ running: true, activity: ['[10:01:02] 🔧 Write modules/events/page.tsx'] })],
    ['waiting its turn, nothing to show yet', chat({ messages: [], queued: 1, preview: null, proposal: null })],
  ];
  for (const [name, answer] of cases) {
    await t.test(name, async (t) => {
      const { poll, nodes } = await openPage(t, answer);
      const before = nodes();
      await poll(structuredClone(answer));
      const after = nodes();
      assert.equal(after.length, before.length);
      assert.ok(after.every((n, i) => n === before[i]), 'every node stays where it was');
    });
  }
});

test('a new message repaints the messages, and only them', async (t) => {
  const { root, poll } = await openPage(t);
  const texts = () => root.querySelectorAll('.build-msg .build-text').map((n) => n.textContent);
  assert.deepEqual(texts(), ['Add an events page']);
  const frame = root.querySelector('iframe');
  const proposal = root.querySelector('.callout');
  const next = chat();
  next.messages.push(message('assistant', 'Done: modules/events', '2026-10-18T10:04:00Z'));
  await poll(next);
  assert.deepEqual(texts(), ['Add an events page', 'Done: modules/events']);
  assert.equal(root.querySelector('iframe'), frame, 'the preview keeps its frame');
  assert.equal(root.querySelector('.callout'), proposal);
});

test('the working line follows the run, and the Stop button stays', async (t) => {
  const working = (activity) => chat({ running: true, activity });
  const { root, poll } = await openPage(t, working(['[10:01:02] 🔧 Read modules/']));
  const stopButton = () => root.querySelectorAll('button').find((b) => b.textContent === 'Stop');
  const stop = stopButton();
  assert.ok(stop);
  const status = root.querySelector('.build-status');
  assert.equal(status.hidden, false);
  assert.equal(root.querySelector('.build-last').textContent, '🔧 Read modules/');
  await poll(working(['[10:01:02] 🔧 Read modules/', '[10:01:03] plain output']), 1500);
  assert.equal(stopButton(), stop, 'a line that is not shown changes nothing');
  await poll(working(['[10:01:02] 🔧 Read modules/', '[10:01:09] 💬 Writing the page']), 1500);
  assert.equal(root.querySelector('.build-last').textContent, '💬 Writing the page');
  assert.equal(stopButton(), stop, 'nor does one that is: Stop stays under the pointer');
  await poll(chat({ running: false, queued: 1, activity: ['[10:01:09] 💬 Writing the page'] }), 1500);
  assert.match(root.querySelector('.build-working').textContent, /Waiting its turn/);
  assert.equal(stopButton(), undefined, 'nothing to stop while it waits');
  await poll(chat({ running: false }), 1500);
  assert.equal(root.querySelector('.build-working'), null, 'gone once the run is over');
  assert.equal(status.hidden, true);
});

test('a restarted preview gets a new frame', async (t) => {
  const { root, poll } = await openPage(t);
  const frame = root.querySelector('iframe');
  await poll(chat({ preview: { local: 'http://127.0.0.1:5302/_fridolin/preview-login?token=two' } }));
  const next = root.querySelector('iframe');
  assert.notEqual(next, frame);
  assert.equal(next.getAttribute('src'), 'http://127.0.0.1:5302/_fridolin/preview-login?token=two');
});

test('the builder brains are offered by name and id', async (t) => {
  const { root, bridge, calls } = await openPage(t);
  const picker = root.querySelector('select');
  assert.deepEqual(picker.querySelectorAll('option').map((o) => [o.value, o.textContent]), [['web', 'Web (web)'], ['docs', 'docs']]);
  bridge.chat = chat({ messages: [] });
  picker.value = 'docs';
  picker.dispatchEvent(new Event('change', { bubbles: true }));
  await settle();
  assert.equal(calls.at(-1).path, '/api/site/build/docs');
  assert.ok(root.querySelector('.build-empty'), 'the other brain’s chat is shown');
});
