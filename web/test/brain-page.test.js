import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { brainsResource } from '../assets/app/resources.js';
import page from '../assets/pages/brain.js';

const manifest = (name) => ({ version: 1, id: 'work', name, routing: { handles_dms: true, default: false, groups: [] }, model: '', effort: '' });
const view = (name, runtime = null) => ({ id: 'work', name, path: '/Users/you/work', status: 'ok', manifest: manifest(name), issues: [], runtime });

async function openPage(t) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { brains: [view('Work')] };
  serve(t, (path) => {
    if (path.startsWith('/api/brains/work/calendar')) return { client_configured: false, connected: false };
    if (path.startsWith('/api/brains')) return bridge.brains;
    if (path.startsWith('/api/config')) return { etag: 'e1', path: '/Users/you/.fridolin/config.json', config: {}, effective: {} };
    if (path.startsWith('/api/whatsapp/groups')) return [];
    if (path.startsWith('/api/activity')) return [];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage({ params: { id: 'work' } });
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const nameIn = () => root.querySelector('.brain-form input');
  const note = () => root.querySelector('.live-form').firstChild; // the "changed elsewhere" note
  const bar = () => root.querySelector('.save-bar');
  const poll = async (brains) => { bridge.brains = brains; await brainsResource.refresh(); await settle(); };
  return { root, ctx, nameIn, note, bar, poll };
}

test('typed settings survive a refresh, and a change elsewhere is flagged', async (t) => {
  const { root, ctx, nameIn, note, bar, poll } = await openPage(t);
  const input = nameIn();
  assert.equal(input.value, 'Work');
  assert.equal(ctx.dirty(), false);
  type(input, 'Home');
  assert.equal(ctx.dirty(), true);
  assert.equal(bar().hidden, false);

  await poll([view('Work', { running: { chat: '15551234567@s.whatsapp.net', sender: '15551234567', started: '2026-10-18T10:00:00Z' }, queue: [] })]);
  assert.match(root.textContent, /Working/, 'the live state follows the poll');
  assert.equal(nameIn(), input, 'the form was not rebuilt');
  assert.equal(input.value, 'Home');
  assert.equal(note().hidden, true, 'a run is not a change to the settings');

  await poll([view('Office')]);
  assert.equal(nameIn(), input);
  assert.equal(input.value, 'Home', 'the edits are kept');
  assert.equal(note().hidden, false, 'and the change elsewhere is shown');

  note().querySelector('button').click();
  await settle();
  assert.equal(nameIn().value, 'Office');
  assert.equal(ctx.dirty(), false);
  assert.equal(bar().hidden, true);
});

test('a setting typed back to what it was is no unsaved change', async (t) => {
  const { ctx, nameIn, bar } = await openPage(t);
  type(nameIn(), 'Home');
  type(nameIn(), 'Work');
  assert.equal(ctx.dirty(), false);
  assert.equal(bar().hidden, true);
});

test('edits whose form left the page are not unsaved changes', async (t) => {
  const cases = [
    // name, what the bridge then has
    ['the brain was removed', []],
    ['the brain has no approved settings', [{ ...view('Work'), manifest: null, status: 'pending' }]],
  ];
  for (const [name, brains] of cases) {
    await t.test(name, async (t) => {
      const { ctx, nameIn, poll } = await openPage(t);
      type(nameIn(), 'Home');
      assert.equal(ctx.dirty(), true);
      await poll(brains);
      assert.equal(nameIn(), null, 'the form is off the page');
      assert.equal(ctx.dirty(), false, 'leaving does not ask about edits nobody can see');
    });
  }
});

test('a brain that does not exist shows only that', async (t) => {
  const { root, poll } = await openPage(t);
  const shown = () => root.querySelectorAll('section.section').filter((s) => !s.hidden).map((s) => s.querySelector('h2').textContent);
  const all = ['Now', 'Settings', 'Calendar', 'Recent messages', 'Remove'];
  assert.deepEqual(shown(), all);
  await poll([]);
  assert.match(root.textContent, /No such brain/);
  assert.deepEqual(shown(), [], 'no empty sections of a brain that is not there');
  await poll([view('Work')]);
  assert.deepEqual(shown(), all, 'back once it is');
});
