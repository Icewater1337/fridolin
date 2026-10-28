import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import page from '../assets/pages/fride.js';

/** @import { FrideInstanceView, FrideThread } from '../types/api.js' */

/**
 * An instance as GET /api/fride lists it.
 * @param {Partial<FrideInstanceView>} over
 * @returns {FrideInstanceView}
 */
const instance = (over) => ({
  name: 'work', description: 'work repositories', repos: ['https://gitlab.example.com/group/app.git'],
  identities: ['gitlab'], egress: ['registry.example.com'], nodes: ['mac-mini'],
  memory_mb: 0, idle_stop_minutes: 30,
  state: 'running', node: 'mac-mini', node_id: '7216c2648b04', running_memory_mb: 6144,
  open_threads: 1, problem: '',
  ...over,
});

/**
 * @param {Partial<FrideThread>} over
 * @returns {FrideThread}
 */
const thread = (over) => ({
  thread: 12, state: 'open', chat: '120363000000000000@g.us', brain: 'main', message: 'Fix the login page',
  node: 'mac-mini', task: '7', branch: 'fix-login', updated: '2026-10-28T10:00:00Z', news: [],
  ...over,
});

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

/**
 * Opens the page on a bridge that answers GET /api/fride with what
 * bridge.data holds; route answers the rest (undefined: {}).
 * @param {import('node:test').TestContext} t
 * @param {{threads: FrideThread[], instances?: FrideInstanceView[] | null}} data
 * @param {(path: string, init: import('./serve.js').RouteInit, bridge: {data: object}) => unknown} [route]
 */
async function openPage(t, data, route = () => undefined) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { data };
  const calls = serve(t, (path, init) => {
    const own = route(path, init, bridge);
    if (own !== undefined) return own;
    if (path === '/api/fride') return bridge.data;
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  // The resource is shared with the tests before: ask the bridge again.
  await resource('/api/fride').refresh();
  await settle();
  return { root, calls, bridge };
}

/**
 * The row of the instance called name.
 * @param {ParentNode} root
 * @param {string} name
 */
const rowOf = (root, name) => root.querySelectorAll('li.fride-instance').find((li) => li.querySelector('code').textContent === name);

test('a thread handed to an instance names it; one for the node’s own FrIDE does not', async (t) => {
  const { root } = await openPage(t, { threads: [thread({ instance: 'work' }), thread({ thread: 11, node: 'laptop', task: '3', branch: '' })], instances: [] });
  const cards = root.querySelectorAll('article.card');
  assert.equal(cards.length, 2);
  assert.match(cards[0].textContent, /mac-mini · instance work · task 7 · branch fix-login/);
  assert.match(cards[1].textContent, /laptop · task 3/);
  assert.doesNotMatch(cards[1].textContent, /instance/);
  assert.equal(root.querySelector('.fride-instances-card'), null, 'no instances configured: no list');
});

test('the instances show where they run, their memory and threads, and start and stop', async (t) => {
  const instances = [
    instance({ name: 'fridolin', description: 'fridolin and FrIDE themselves', identities: ['github'], nodes: [], egress: [], state: '', node: '', node_id: '', running_memory_mb: 0, open_threads: 0, idle_stop_minutes: 0 }),
    instance({}),
    instance({ name: 'big', description: 'the heavy stack', memory_mb: 16384, state: 'failed', problem: 'sbx could not start the VM', open_threads: 0 }),
  ];
  const { root, calls, bridge } = await openPage(t, { threads: [], instances }, (path, init, b) => {
    if (init.method !== 'POST') return undefined;
    if (path === '/api/fride/instances/fridolin/start') {
      const started = instance({ name: 'fridolin', description: 'fridolin and FrIDE themselves', state: 'running', node: 'laptop', running_memory_mb: 3072, open_threads: 0 });
      b.data = { threads: [], instances: [started, instances[1], instances[2]] };
      return started;
    }
    if (path === '/api/fride/instances/big/start') return { status: 409, body: { error: 'no node has 16 GB free for big: mac-mini runs work (6 GB, 1 open thread)' } };
    return undefined;
  });
  assert.match(root.textContent, /No FrIDE work yet/, 'the threads keep their empty state');

  const fridolin = rowOf(root, 'fridolin');
  assert.match(fridolin.textContent, /fridolinnot running/);
  assert.match(fridolin.textContent, /fridolin and FrIDE themselves/);
  assert.match(fridolin.textContent, /may run on any node that offers fride_host · sized at each start · 0 open threads · never stops by itself/);
  assert.equal(button(fridolin, 'Stop'), undefined);

  const work = rowOf(root, 'work');
  assert.match(work.textContent, /workrunning/);
  assert.match(work.textContent, /on mac-mini · 6 GB · 1 open thread · stops after 30 minutes idle/);
  assert.equal(button(work, 'Start'), undefined, 'a running one only stops');
  assert.ok(button(work, 'Stop'));

  const big = rowOf(root, 'big');
  assert.match(big.textContent, /bigfailed/);
  assert.match(big.textContent, /sbx could not start the VM/);
  assert.equal(big.querySelector('select'), null, 'a fixed size is the size: nothing to choose');

  const size = fridolin.querySelector('select');
  assert.deepEqual(size.querySelectorAll('option').map((o) => o.textContent), ['Default size', 'Small, 3 GB', 'Medium, 6 GB', 'Large, 12 GB']);
  size.value = 'small';
  button(fridolin, 'Start').click();
  await settle();
  assert.deepEqual(calls.find((c) => c.path === '/api/fride/instances/fridolin/start').body, { size: 'small' });
  assert.ok(document.querySelectorAll('.toast').some((el) => el.textContent.includes('Started fridolin on laptop')));
  assert.match(rowOf(root, 'fridolin').textContent, /on laptop · 3 GB/);
  assert.equal(rowOf(root, 'work'), work, 'the rows that did not change stay');

  button(big, 'Start').click();
  await settle();
  assert.deepEqual(calls.find((c) => c.path === '/api/fride/instances/big/start').body, {}, 'no size: the core takes the fixed one');
  assert.ok(document.querySelectorAll('.toast').some((el) => /no node has 16 GB free for big/.test(el.textContent)), 'a refusal says why');
  assert.equal(bridge.data.instances[2].state, 'failed');
});

test('stopping an instance with an open thread asks first, then sends force', async (t) => {
  const { root, calls } = await openPage(t, { threads: [thread({ instance: 'work' })], instances: [instance({})] }, (path, init, b) => {
    if (path !== '/api/fride/instances/work/stop') return undefined;
    if (!JSON.parse(init.body).force) return { status: 409, body: { error: 'work has an open thread (#12)' } };
    b.data = { threads: [thread({ instance: 'work' })], instances: [instance({ state: 'stopped' })] };
    return { ok: true };
  });
  button(rowOf(root, 'work'), 'Stop').click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.match(dialog.textContent, /Stop work anyway\?/);
  assert.match(dialog.textContent, /work has an open thread \(#12\)\. Stopping it cuts off the work in progress; the instance’s disk stays\./);
  dialog.querySelectorAll('button').find((b) => b.textContent === 'Stop anyway').click();
  await settle();
  const stops = calls.filter((c) => c.path === '/api/fride/instances/work/stop');
  assert.deepEqual(stops.map((c) => [c.method, c.body]), [['POST', {}], ['POST', { force: true }]]);
  assert.ok(document.querySelectorAll('.toast').some((el) => el.textContent.includes('Stopped work')));
  const row = rowOf(root, 'work');
  assert.match(row.textContent, /workstopped/);
  assert.ok(button(row, 'Start'), 'a stopped one starts again');
});

test('a stop without an open thread asks nothing, and an unknown instance says so', async (t) => {
  const { root, calls } = await openPage(t, { threads: [], instances: [instance({ open_threads: 0 })] }, (path) => {
    if (path === '/api/fride/instances/work/stop') return { status: 404, body: { error: 'no FrIDE instance work' } };
    return undefined;
  });
  button(rowOf(root, 'work'), 'Stop').click();
  await settle();
  assert.equal(document.querySelector('dialog'), null);
  assert.equal(calls.filter((c) => c.path === '/api/fride/instances/work/stop').length, 1);
  assert.ok(document.querySelectorAll('.toast').some((el) => el.textContent.includes('no FrIDE instance work')));
});
