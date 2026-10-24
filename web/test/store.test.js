import { test } from 'node:test';
import assert from 'node:assert/strict';
import { stubFetch } from './stub-fetch.js';
import { createStore, store, setStatus, setRestartRequired, pollStatus, rememberName, chatName, loadGroups } from '../assets/app/store.js';

// The tests give only the fields of the status that they read.
/** @import { Status } from '../types/api.js' */

test('subscribe calls back now and on every change of what it selects', () => {
  const s = createStore({ a: 1, b: 1 });
  const seen = [];
  const off = s.subscribe((x) => x.a, (v) => seen.push(v));
  assert.deepEqual(seen, [1], 'called at once with the current value');
  s.set({ b: 2 });
  assert.deepEqual(seen, [1], 'not called when something else changed');
  s.set({ a: 2 });
  s.set({ a: 2 });
  assert.deepEqual(seen, [1, 2], 'called once per change');
  off();
  s.set({ a: 3 });
  assert.deepEqual(seen, [1, 2], 'not called after unsubscribing');
  assert.deepEqual(s.get(), { a: 3, b: 2 });
});

test('a failing subscriber does not stop the others', () => {
  const s = createStore({ a: 1 });
  const errors = console.error;
  console.error = () => {};
  try {
    s.subscribe((x) => x.a, (v) => { if (v === 2) throw new Error('boom'); });
    const seen = [];
    s.subscribe((x) => x.a, (v) => seen.push(v));
    s.set({ a: 2 });
    assert.deepEqual(seen, [1, 2]);
  } finally {
    console.error = errors;
  }
});

test('pending restarts mirror what the bridge says', () => {
  setStatus(/** @type {Status} */ ({ restart_required: ['log_level'] }));
  setRestartRequired(['http_listen']);
  assert.deepEqual(store.get().status.restart_required, ['http_listen'], 'replaced, not merged with the old list');
  setRestartRequired([]);
  assert.deepEqual(store.get().status.restart_required, []);
  setRestartRequired(undefined);
  assert.deepEqual(store.get().status.restart_required, []);
});

test('a status poll from before a save cannot hide its restart', async (t) => {
  const tick = () => new Promise((r) => setImmediate(r));
  /** @type {[string, boolean][]} */
  const cases = [
    ['its answer is still on the way', false],
    ['its answer is in, not yet in the store', true],
  ];
  for (const [name, answeredFirst] of cases) {
    await t.test(name, async (t) => {
      t.mock.timers.enable({ apis: ['setTimeout'] });
      const net = stubFetch(t);
      const seen = [];
      const poll = pollStatus((s) => seen.push(s.restart_required));
      t.after(() => poll.stop());
      const told = seen.length; // the status known from before, if any
      net.answer(0, { restart_required: [] });
      await tick();
      t.mock.timers.tick(3000); // the next poll starts (call 1), then the save lands
      if (answeredFirst) net.answer(1, { restart_required: [] });
      setRestartRequired(['log_level']);
      assert.deepEqual(store.get().status.restart_required, ['log_level'], 'shown at once');
      assert.equal(net.calls[1].signal.aborted, true, 'the older poll is dropped');
      await tick();
      assert.deepEqual(store.get().status.restart_required, ['log_level'], 'the older answer does not hide it');
      assert.equal(net.calls.length, 3, 'the status is asked for again');
      net.answer(2, { restart_required: ['log_level'] });
      await tick();
      assert.deepEqual(seen.slice(told), [[], ['log_level']]);
    });
  }
});

test('chat names', async (t) => {
  const GROUP = '120363000000000000@g.us';
  const OTHER = '120363000000000001@g.us';
  const seen = [];
  t.after(store.subscribe((s) => s.names, (names) => seen.push(names.size)));
  const before = seen.length;

  rememberName(GROUP, 'Family');
  assert.equal(chatName(GROUP), 'Family');
  rememberName(GROUP, 'Other name');
  assert.equal(chatName(GROUP), 'Family', 'a remembered name does not overwrite a known one');
  assert.equal(seen.length, before + 1, 'subscribers hear about a new name once');

  const net = stubFetch(t);
  const loading = loadGroups(true);
  net.answer(0, [{ jid: GROUP, name: 'Family (renamed)' }, { jid: OTHER, name: 'Work' }]);
  await loading;
  assert.equal(chatName(GROUP), 'Family (renamed)', 'the groups list is the authority');
  assert.equal(chatName(OTHER), 'Work');
  assert.equal(chatName('15551234567@s.whatsapp.net'), '');
  assert.equal(seen.length, before + 2);
});

test('a fresh load of the groups waits for one on its way, then asks for a fresh list', async (t) => {
  const net = stubFetch(t);
  const plain = loadGroups(true);
  const fresh = loadGroups(true, { fresh: true });
  const shared = loadGroups(true);
  assert.deepEqual(net.calls.map((c) => c.url), ['/api/whatsapp/groups'], 'a plain load shares the one on its way');
  net.answer(0, [{ jid: '120363000000000000@g.us', name: 'Family' }]);
  await Promise.all([plain, shared]);
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(net.calls.map((c) => c.url), ['/api/whatsapp/groups', '/api/whatsapp/groups?fresh=1']);
  net.answer(1, [{ jid: '120363000000000000@g.us', name: 'Family (renamed)' }]);
  await fresh;
  assert.equal(chatName('120363000000000000@g.us'), 'Family (renamed)');
});
