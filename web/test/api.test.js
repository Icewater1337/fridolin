import { test } from 'node:test';
import assert from 'node:assert/strict';
import { stubFetch } from './stub-fetch.js';
import { api, get, post, on, ApiError } from '../assets/app/api.js';
import { withDeadline } from '../assets/lib/abort.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function watchReachable(t) {
  const seen = [];
  const off = on('reachable', (ok) => seen.push(ok));
  t.after(off);
  return seen;
}

test('a request the bridge never answers times out', async (t) => {
  const net = stubFetch(t);
  const reachable = watchReachable(t);
  const err = await get('/api/status', { timeout: 20 }).catch((e) => e);
  assert.ok(err instanceof ApiError, `got ${err}`);
  assert.equal(err.status, 0, 'counts as the bridge not answering, which the polls do not toast');
  assert.match(err.message, /did not answer within/);
  assert.equal(net.calls[0].signal.aborted, true, 'the request is abandoned');
  assert.deepEqual(reachable, [false], 'the offline banner shows');

  // The next answer brings the dashboard back.
  const ok = get('/api/status');
  net.answer(1, { ok: true });
  assert.deepEqual(await ok, { ok: true });
  assert.deepEqual(reachable, [false, true]);
});

test('the caller can still abort, and that is no error to show', async (t) => {
  const cases = [
    ['without a reason', undefined, (e) => e.name === 'AbortError'],
    ['with a reason', new Error('left the page'), (e) => e.message === 'left the page'],
  ];
  for (const [name, reason, want] of cases) {
    await t.test(name, async (t) => {
      stubFetch(t);
      const reachable = watchReachable(t);
      const ctl = new AbortController();
      const p = get('/api/brains', { signal: ctl.signal });
      ctl.abort(reason);
      const err = await p.catch((e) => e);
      assert.ok(want(err), `got ${err}`);
      assert.deepEqual(reachable, [], 'the offline banner stays away');
    });
  }
});

test('timeout 0 waits as long as it takes', async (t) => {
  const net = stubFetch(t);
  const p = post('/api/fs/pick', undefined, { timeout: 0 });
  await sleep(30);
  assert.equal(net.calls[0].signal.aborted, false);
  net.answer(0, { path: '/Users/you/brain' });
  assert.deepEqual(await p, { path: '/Users/you/brain' });
});

test('answers and refusals', async (t) => {
  const cases = [
    ['JSON body', 200, { a: 1 }, { data: { a: 1 } }],
    ['empty body', 200, undefined, { data: null }],
    ['error from the bridge', 422, { error: 'brain id taken' }, { error: 'brain id taken', status: 422 }],
    ['error without a message', 500, undefined, { error: 'Request failed (500 )', status: 500 }],
  ];
  for (const [name, status, body, want] of cases) {
    await t.test(name, async (t) => {
      const net = stubFetch(t);
      const p = api('GET', '/api/x');
      net.answer(0, body, status);
      const got = await p.then((data) => ({ data }), (e) => ({ error: e.message, status: e.status }));
      assert.deepEqual(got, want);
    });
  }
});

test('a 401 tells the app to ask for the token', async (t) => {
  const net = stubFetch(t);
  let told = 0;
  t.after(on('unauthorized', () => { told++; }));
  const p = get('/api/status');
  net.answer(0, { error: 'missing or wrong dashboard token' }, 401);
  await assert.rejects(p, { status: 401 });
  assert.equal(told, 1);
});

test('writes carry the dashboard header and a JSON body', async (t) => {
  const net = stubFetch(t);
  const p = post('/api/brains', { id: 'work' });
  net.answer(0, {});
  await p;
  const { init } = net.calls[0];
  assert.equal(init.method, 'POST');
  assert.equal(init.headers['X-Bridge-Dashboard'], '1');
  assert.equal(init.headers['Content-Type'], 'application/json');
  assert.equal(init.body, '{"id":"work"}');
});

test('withDeadline', async (t) => {
  await t.test('passes on the caller\'s abort and its reason', () => {
    const ctl = new AbortController();
    const d = withDeadline(ctl.signal, 0);
    ctl.abort(new Error('left the page'));
    assert.equal(d.signal.aborted, true);
    assert.equal(d.signal.reason.message, 'left the page');
    assert.equal(d.expired, false);
  });
  await t.test('an aborted signal aborts at once', () => {
    assert.equal(withDeadline(AbortSignal.abort(), 0).signal.aborted, true);
  });
  await t.test('expires after ms', async () => {
    const d = withDeadline(undefined, 5);
    await sleep(20);
    assert.equal(d.expired, true);
    assert.equal(d.signal.reason.name, 'TimeoutError');
  });
  await t.test('clear stops the deadline', async () => {
    const d = withDeadline(undefined, 5);
    d.clear();
    await sleep(20);
    assert.equal(d.signal.aborted, false);
  });
});
