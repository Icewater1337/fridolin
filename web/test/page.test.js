import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createPage } from '../assets/app/page.js';
import { createStore } from '../assets/app/store.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

test('leaving a page ends everything it started', async () => {
  const ctx = createPage({ params: { id: 'work' } });
  assert.deepEqual(ctx.params, { id: 'work' });

  let polled = 0;
  let pollSignal = null;
  ctx.poll(async (signal) => { polled++; pollSignal = signal; await sleep(50); }, 5);

  let timedOut = false;
  ctx.timeout(() => { timedOut = true; }, 5);
  let debounced = 0;
  const later = ctx.debounce(() => { debounced++; }, 5);
  later();

  const store = createStore({ n: 0 });
  const watched = [];
  ctx.watch(store, (s) => s.n, (n) => watched.push(n));

  const order = [];
  ctx.onCleanup(() => { order.push('first'); throw new Error('one cleanup failing does not stop the rest'); });
  ctx.onCleanup(() => order.push('second'));

  const errors = console.error;
  console.error = () => {};
  try {
    ctx.destroy();
    ctx.destroy();
  } finally {
    console.error = errors;
  }

  assert.equal(ctx.dead, true);
  assert.equal(ctx.signal.aborted, true, 'one-off requests are aborted');
  assert.equal(pollSignal.aborted, true, 'the running poll is aborted');
  assert.deepEqual(order, ['first', 'second'], 'cleanups run once, in order');
  store.set({ n: 1 });
  assert.deepEqual(watched, [0], 'store subscriptions end');
  await sleep(30);
  assert.equal(polled, 1, 'polls stop');
  assert.equal(timedOut, false, 'timeouts are cleared');
  assert.equal(debounced, 0, 'debounced calls are dropped');
});

test('debounce runs once after the last call', async () => {
  const ctx = createPage();
  const seen = [];
  const later = ctx.debounce((v) => seen.push(v), 10);
  later(1);
  later(2);
  later(3);
  await sleep(30);
  assert.deepEqual(seen, [3]);
  ctx.destroy();
});

test('replaceHash goes to the router only while the page is shown', () => {
  const seen = [];
  const ctx = createPage({ replaceHash: (h) => seen.push(h) });
  ctx.replaceHash('#/settings/claude');
  ctx.destroy();
  ctx.replaceHash('#/settings/sandbox');
  assert.deepEqual(seen, ['#/settings/claude']);
});

test('setTitle renames the page only while it is shown', () => {
  const titles = [];
  const ctx = createPage({ setTitle: (t) => titles.push(t) });
  ctx.setTitle('Work');
  ctx.destroy();
  ctx.setTitle('Late answer');
  assert.deepEqual(titles, ['Work']);
});
