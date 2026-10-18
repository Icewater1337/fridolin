import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createResource, resource } from '../assets/app/resources.js';
import { createPage } from '../assets/app/page.js';
import { stubFetch } from './stub-fetch.js';

// Lets a finished fetch tell its followers and schedule the next one.
const settle = () => new Promise((r) => setImmediate(r));

// A resource whose fetches the test answers, one by one.
function controlled() {
  const fetches = [];
  const load = (signal) => new Promise((resolve, reject) => {
    fetches.push({ signal, resolve });
    signal.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), { name: 'AbortError' })));
  });
  return { fetches, res: createResource(load) };
}

test('followers share one fetch, as often as the most eager wants', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const { fetches, res } = controlled();
  const a = [];
  const b = [];
  const offA = res.follow((d) => a.push(d), 10000);
  const offB = res.follow((d) => b.push(d), 3000);
  assert.equal(fetches.length, 1, 'the second follower does not fetch again');
  fetches[0].resolve({ n: 1 });
  await settle();
  assert.deepEqual([a, b], [[{ n: 1 }], [{ n: 1 }]]);

  t.mock.timers.tick(3000);
  assert.equal(fetches.length, 2, 'the shortest interval wins');
  fetches[1].resolve({ n: 1 });
  await settle();
  assert.deepEqual([a.length, b.length], [1, 1], 'an unchanged answer tells nobody');

  offB();
  t.mock.timers.tick(3000);
  assert.equal(fetches.length, 3, 'the fetch planned before it left');
  fetches[2].resolve({ n: 2 });
  await settle();
  assert.deepEqual(a, [{ n: 1 }, { n: 2 }]);
  assert.deepEqual(b, [{ n: 1 }], 'a follower that left hears nothing more');
  t.mock.timers.tick(9999);
  assert.equal(fetches.length, 3, 'then the others set the pace');
  t.mock.timers.tick(1);
  assert.equal(fetches.length, 4);
  fetches[3].resolve({ n: 2 });
  await settle();

  offA();
  t.mock.timers.tick(60000);
  assert.equal(fetches.length, 4, 'nobody follows: no more fetches');
  assert.deepEqual(res.data(), { n: 2 }, 'the last answer is kept');
});

test('a new follower gets what is known at once, and fresh data when it is old', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const { fetches, res } = controlled();
  t.after(res.follow(() => {}, 10000));
  fetches[0].resolve(['a']);
  await settle();

  const cases = [
    // name, how long after the last answer, the follower's interval, a fetch?
    ['fresh enough', 2000, 3000, false],
    ['older than it wants', 4000, 3000, true],
  ];
  for (const [name, after, every, fetches1] of cases) {
    await t.test(name, async () => {
      const before = fetches.length;
      t.mock.timers.tick(after);
      const seen = [];
      const off = res.follow((d) => seen.push(d), every);
      assert.deepEqual(seen, [['a']], 'told at once');
      assert.equal(fetches.length > before, fetches1);
      if (fetches1) { fetches.at(-1).resolve(['a']); await settle(); }
      off();
    });
  }
});

test('refresh drops a fetch in flight and resolves once followers have the answer', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const { fetches, res } = controlled();
  const seen = [];
  t.after(res.follow((d) => seen.push(d), 3000));
  let done = false;
  const r = res.refresh().then(() => { done = true; });
  assert.equal(fetches[0].signal.aborted, true, 'the older fetch is dropped');
  await settle();
  assert.equal(fetches.length, 2);
  fetches[0].resolve('stale');
  await settle();
  assert.deepEqual(seen, [], 'a dropped answer tells nobody');
  assert.equal(done, false);
  fetches[1].resolve('fresh');
  await r;
  assert.deepEqual(seen, ['fresh']);
});

test('a resource sends back the ETag of what it has, and a 304 keeps it', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const net = stubFetch(t);
  const res = resource('/api/activity?limit=7');
  const seen = [];
  t.after(res.follow((d) => seen.push(d), 3000));
  const sent = (i) => net.calls[i].init.headers['If-None-Match'];
  const poll = async (...answer) => {
    t.mock.timers.tick(3000);
    net.answer(net.calls.length - 1, ...answer);
    await settle();
  };

  assert.equal(sent(0), undefined, 'nothing to send back yet');
  net.answer(0, [{ text: 'hi' }], 200, { ETag: '"e1"' });
  await settle();
  assert.deepEqual(seen, [[{ text: 'hi' }]]);

  await poll(undefined, 304, { ETag: '"e1"' });
  assert.equal(sent(1), '"e1"');
  assert.deepEqual(seen, [[{ text: 'hi' }]], 'a 304 tells nobody');
  assert.deepEqual(res.data(), [{ text: 'hi' }], 'and keeps the data');

  await poll([{ text: 'hi' }, { text: 'again' }], 200, { ETag: '"e2"' });
  assert.equal(sent(2), '"e1"');
  assert.deepEqual(seen.at(-1), [{ text: 'hi' }, { text: 'again' }], 'a new answer is told');
  await poll(undefined, 304);
  assert.equal(sent(3), '"e2"', 'with its own ETag from then on');

  await poll([], 200);
  assert.deepEqual(seen.at(-1), []);
  t.mock.timers.tick(3000);
  assert.equal(sent(5), undefined, 'an answer without an ETag leaves nothing to send back');
  net.answer(5, []);
  await settle();
});

test('a 304 for an answer a refresh dropped still brings that answer', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const net = stubFetch(t);
  const res = resource('/api/brains/dropped');
  const seen = [];
  t.after(res.follow((d) => seen.push(d), 3000));
  net.answer(0, { name: 'Work' }, 200, { ETag: '"e1"' });
  await settle();

  t.mock.timers.tick(3000);
  net.answer(1, { name: 'Office' }, 200, { ETag: '"e2"' });
  const refreshed = res.refresh(); // before the answer reached the resource
  await settle();
  assert.deepEqual(seen, [{ name: 'Work' }], 'the dropped answer tells nobody');
  assert.equal(net.calls[2].init.headers['If-None-Match'], '"e2"', 'though its ETag is what the bridge is asked about');
  net.answer(2, undefined, 304);
  await refreshed;
  assert.deepEqual(seen, [{ name: 'Work' }, { name: 'Office' }], 'so the 304 brings its data');
});

test('a 304 brings the answer whose ETag went out, whatever came in meanwhile', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const net = stubFetch(t);
  const res = resource('/api/brains/overlap');
  let off = res.follow(() => {}, 3000);
  t.after(() => off());
  net.answer(0, { name: 'Work' }, 200, { ETag: '"e1"' });
  await settle();

  // The page is left and opened again just as an answer comes: the poll
  // it stopped takes that answer in after the new poll asked with e1.
  t.mock.timers.tick(3000);
  net.answer(1, { name: 'Office' }, 200, { ETag: '"e2"' });
  off();
  off = res.follow(() => {}, 3000);
  assert.equal(net.calls[2].init.headers['If-None-Match'], '"e1"');
  await settle();
  net.answer(2, undefined, 304);
  await settle();
  assert.deepEqual(res.data(), { name: 'Work' }, 'the 304 was for e1');
  t.mock.timers.tick(3000);
  assert.equal(net.calls[3].init.headers['If-None-Match'], '"e1"', 'which is what holds from then on');
  net.answer(3, undefined, 304);
  await settle();
});

test('one resource per path', () => {
  assert.equal(resource('/api/brains'), resource('/api/brains'));
  assert.notEqual(resource('/api/activity?limit=10'), resource('/api/activity?limit=30'));
});

test('a page stops following when it goes', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const { fetches, res } = controlled();
  const ctx = createPage();
  const seen = [];
  ctx.follow(res, (d) => seen.push(d), 3000);
  ctx.destroy();
  fetches[0].resolve('late');
  await settle();
  t.mock.timers.tick(60000);
  assert.deepEqual(seen, []);
  assert.equal(fetches.length, 1);
});
