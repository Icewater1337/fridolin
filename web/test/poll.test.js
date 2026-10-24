import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makePoll, pausePollsWhen, resumePolls, retryPolls, backoff } from '../assets/app/poll.js';

const LONG = 60000; // no second scheduled run while a test looks
const tick = () => new Promise((r) => setTimeout(r, 0));
// Lets a finished run schedule its next one; setImmediate is never mocked.
const settle = () => new Promise((r) => setImmediate(r));

// A poll body whose runs finish when the test says so. A run rejects with
// AbortError when its signal fires, the way fetch does.
function controlledRuns() {
  const runs = [];
  /**
   * @param {AbortSignal} signal
   * @returns {Promise<void>}
   */
  const fn = (signal) => new Promise((resolve, reject) => {
    const run = { signal, resolve, painted: false };
    run.finish = () => { run.painted = true; resolve(); };
    signal.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), { name: 'AbortError' })));
    runs.push(run);
  });
  return { runs, fn };
}

// Mocked timers: node --test runs the files in parallel, and under that
// load real 15 ms timers fire late enough to miss a run.
test('a poll runs at once and then every interval', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let n = 0;
  const p = makePoll(async () => { n++; }, 15, { onError: assert.fail });
  t.after(() => p.stop());
  assert.equal(n, 1);
  for (const want of [2, 3, 4]) {
    await settle();
    t.mock.timers.tick(14);
    assert.equal(n, want - 1, 'not before the interval');
    t.mock.timers.tick(1);
    assert.equal(n, want);
  }
});

test('a kick during a run aborts it and runs again', async (t) => {
  const { runs, fn } = controlledRuns();
  const p = makePoll(fn, LONG, { onError: assert.fail });
  t.after(() => p.stop());
  assert.equal(runs.length, 1);
  let kicked = false;
  const k = p.kick().then(() => { kicked = true; });
  assert.equal(runs[0].signal.aborted, true, 'the stale run is aborted');
  await tick();
  assert.equal(runs.length, 2, 'a fresh run starts after the kick');
  assert.equal(kicked, false, 'the kick waits for the fresh run');
  runs[1].finish();
  await k;
  assert.equal(runs[0].painted, false);
  assert.equal(runs[1].painted, true);
});

test('a kick while idle runs at once and resolves after that run', async (t) => {
  const { runs, fn } = controlledRuns();
  const p = makePoll(fn, LONG, { onError: assert.fail });
  t.after(() => p.stop());
  runs[0].finish();
  await tick();
  let kicked = false;
  const k = p.kick().then(() => { kicked = true; });
  assert.equal(runs.length, 2);
  await tick();
  assert.equal(kicked, false);
  runs[1].finish();
  await k;
});

test('kicks during a run are answered by one fresh run', async (t) => {
  const { runs, fn } = controlledRuns();
  const p = makePoll(fn, LONG, { onError: assert.fail });
  t.after(() => p.stop());
  const k1 = p.kick();
  await tick();
  const k2 = p.kick();
  await tick();
  assert.equal(runs.length, 3);
  assert.deepEqual(runs.map((r) => r.signal.aborted), [true, true, false]);
  runs[2].finish();
  await Promise.all([k1, k2]);
});

test('paused polls wait and run when resumed', async (t) => {
  let isPaused = true;
  pausePollsWhen(() => isPaused);
  t.after(() => pausePollsWhen(() => false));
  let n = 0;
  const p = makePoll(async () => { n++; }, LONG, { onError: assert.fail });
  t.after(() => p.stop());
  const k = p.kick();
  assert.equal(n, 0);
  isPaused = false;
  resumePolls();
  await k;
  assert.equal(n, 1, 'one run answers the start and the kick');
});

test('stop aborts the run, answers pending kicks and ends the poll', async () => {
  const { runs, fn } = controlledRuns();
  const p = makePoll(fn, 5, { onError: assert.fail });
  const k = p.kick();
  await tick();
  p.stop();
  assert.equal(runs[1].signal.aborted, true);
  await k;
  await p.kick();
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(runs.length, 2, 'nothing runs after stop');
});

test('errors are reported once per message, aborts never', async (t) => {
  const reported = [];
  const results = [new Error('down'), new Error('down'), new Error('other'), null, new Error('down'),
    Object.assign(new Error('gone'), { name: 'AbortError' }), Object.assign(new Error('slow'), { name: 'TimeoutError' })];
  let i = 0;
  const p = makePoll(async () => { const r = results[i++]; if (r) throw r; }, LONG, { onError: (e) => reported.push(e.message) });
  t.after(() => p.stop());
  for (let n = 1; n < results.length; n++) { await tick(); await p.kick(); }
  assert.deepEqual(reported, ['down', 'other', 'down']);
});

test('backoff doubles per failure up to its cap', async (t) => {
  const cases = [
    // interval, failures in a row, wait
    [3000, 0, 3000],
    [3000, 1, 6000],
    [3000, 2, 12000],
    [3000, 3, 24000],
    [3000, 4, 30000],
    [3000, 20, 30000],
    [60000, 0, 60000],
    [60000, 1, 60000],
    [60000, 5, 60000],
  ];
  for (const [interval, failures, want] of cases) {
    await t.test(`${interval} ms after ${failures}`, () => assert.equal(backoff(interval, failures), want));
  }
});

test('a failing poll waits longer, until it succeeds or is retried', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let fail = true;
  let n = 0;
  const p = makePoll(async () => { n++; if (fail) throw Object.assign(new Error('down'), { status: 0 }); }, 1000, { onError: () => {} });
  t.after(() => p.stop());
  const waitFor = async (ms) => { await settle(); t.mock.timers.tick(ms - 1); assert.equal(n, runs, `not before ${ms} ms`); t.mock.timers.tick(1); runs++; assert.equal(n, runs, `after ${ms} ms`); };
  let runs = 1;
  await waitFor(2000);
  await waitFor(4000);
  fail = false;
  await waitFor(8000);
  await waitFor(1000); // a success resets the wait
  fail = true;
  await waitFor(1000);
  await settle();
  retryPolls();
  assert.equal(n, runs + 1, 'retryPolls runs a failing poll at once');
  runs++;
  await settle();
  retryPolls();
  assert.equal(n, runs + 1, 'and again while it keeps failing');
  fail = false;
  runs++;
  await waitFor(8000);
  await settle();
  retryPolls();
  assert.equal(n, runs, 'retryPolls leaves a healthy poll alone');
});
