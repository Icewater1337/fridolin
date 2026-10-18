import { test } from 'node:test';
import assert from 'node:assert/strict';
import { stubFetch } from './stub-fetch.js';
import { api, get, post, on, signIn, signOut, rotateToken, ApiError } from '../assets/app/api.js';
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

test('requests carry no token: the session signs them', async (t) => {
  const net = stubFetch(t);
  const p = get('/api/status');
  net.answer(0, {});
  await p;
  const { init } = net.calls[0];
  assert.equal(init.headers['X-Bridge-Token'], undefined);
  assert.equal(init.credentials, 'same-origin');
});

test('signIn trades a login code for a session', async (t) => {
  const net = stubFetch(t);
  const p = signIn({ code: 'abc' });
  net.answer(0, { status: 'signed in' });
  await p;
  assert.equal(net.calls[0].url, '/api/login');
  assert.equal(net.calls[0].init.method, 'POST');
  assert.equal(net.calls[0].init.body, '{"code":"abc"}');
});

// answered runs call() and answers its one request with body.
async function answered(net, call, body) {
  const n = net.calls.length;
  const p = call();
  net.answer(n, body);
  return p;
}

test('every call carries the binding the sign-in returned, until sign-out', async (t) => {
  const net = stubFetch(t);
  await answered(net, () => signIn({ code: 'abc' }), { status: 'signed in', binding: 'bind-1' });
  await answered(net, () => get('/api/status'), {});
  await answered(net, () => post('/api/restart'), {});
  assert.equal(net.calls[1].init.headers['X-Bridge-Session'], 'bind-1', 'on reads');
  assert.equal(net.calls[2].init.headers['X-Bridge-Session'], 'bind-1', 'and on writes');

  await answered(net, () => rotateToken(), { status: 'rotated', binding: 'bind-2' });
  await answered(net, () => get('/api/status'), {});
  assert.equal(net.calls[4].init.headers['X-Bridge-Session'], 'bind-2', 'rotating signs this browser in again');

  await answered(net, () => signOut(), { status: 'signed out' });
  await answered(net, () => get('/api/status'), {});
  assert.equal(net.calls[6].init.headers['X-Bridge-Session'], undefined, 'gone after signing out');
});

test('the binding is kept in localStorage, where every tab finds it', async (t) => {
  const store = new Map();
  globalThis.localStorage = {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
    removeItem: (k) => store.delete(k),
  };
  t.after(() => { delete globalThis.localStorage; });
  const net = stubFetch(t);
  await answered(net, () => signIn({ token: 'pasted' }), { status: 'signed in', binding: 'bind-1' });
  assert.equal(store.get('fridolin.session'), 'bind-1');
  assert.ok(![...store.values()].includes('pasted'), 'the token is not stored');

  store.set('fridolin.session', 'bind-from-another-tab');
  await answered(net, () => get('/api/status'), {});
  assert.equal(net.calls[1].init.headers['X-Bridge-Session'], 'bind-from-another-tab');

  await answered(net, () => signOut(), {});
  assert.equal(store.has('fridolin.session'), false);
});

test('a refused sign-in does not sign the app out', async (t) => {
  const net = stubFetch(t);
  let told = 0;
  t.after(on('unauthorized', () => { told++; }));
  const p = signIn({ code: 'used' });
  net.answer(0, { error: 'that link was already used' }, 401);
  await assert.rejects(p, { status: 401 });
  assert.equal(told, 0, 'the session the browser has may still be good');
});

test('the token an older dashboard kept in localStorage is removed', async () => {
  const removed = [];
  globalThis.localStorage = {
    getItem: () => 'old-token',
    setItem: () => { throw new Error('nothing may be stored'); },
    removeItem: (k) => removed.push(k),
  };
  try {
    await import('../assets/app/api.js?with-storage');
  } finally {
    delete globalThis.localStorage;
  }
  assert.deepEqual(removed, ['bridgeToken']);
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
