import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { brainsResource } from '../assets/app/resources.js';
import { brainSettingsFields } from '../assets/components/brain-fields.js';
import page from '../assets/pages/brain.js';

const manifest = (name) => ({ version: 1, id: 'work', name, routing: { handles_dms: true, default: false, groups: [] }, model: '', effort: '' });
const view = (name, runtime = null) => ({ id: 'work', name, path: '/Users/you/work', status: 'ok', manifest: manifest(name), issues: [], runtime });

// opts.brains is what the bridge holds at first, opts.approve what it
// answers POST /api/brains/work/approve with.
async function openPage(t, opts = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { brains: opts.brains || [view('Work')] };
  const calls = serve(t, (path, init) => {
    if (path === '/api/brains/work/approve' && init.method === 'POST') return opts.approve;
    if (path.startsWith('/api/brains/work/calendar')) return { client_configured: false, connected: false };
    if (path === '/api/brains/work' && init.method === 'PUT') return { ok: true };
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
  return { root, ctx, nameIn, note, bar, poll, calls };
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

test('approving drift sends the brain.json shown', async (t) => {
  // A rename only, so no confirmation dialog stands in the way.
  const drifted = { ...view('Work'), status: 'drift', disk_manifest: manifest('Office'), issues: [{ kind: 'drift', detail: 'brain.json changed on disk' }] };
  const cases = [
    // name, the bridge's answer, the toast it gives
    ['the bridge approves it', { status: 200, body: { status: 'approved' } }, ['ok', /Changes approved/]],
    ['brain.json changed again meanwhile', { status: 409, body: { error: 'brain.json changed again since you reviewed it' } }, ['warn', /changed again since you reviewed it/]],
  ];
  for (const [name, approve, [kind, message]] of cases) {
    await t.test(name, async (t) => {
      const { root, calls } = await openPage(t, { brains: [drifted], approve });
      const button = root.querySelectorAll('button').find((b) => b.textContent.includes('Approve changes'));
      assert.ok(button, 'the drift card offers approval');
      button.click();
      await settle();
      const posts = calls.filter((c) => c.method === 'POST');
      assert.deepEqual(posts.map((c) => c.path), ['/api/brains/work/approve']);
      assert.deepEqual(posts[0].body, { manifest: drifted.disk_manifest }, 'exactly the manifest the page showed');
      const toasts = (k) => document.querySelectorAll(`.toast.${k}`).map((el) => el.textContent);
      assert.deepEqual(toasts('error'), []);
      assert.ok(toasts(kind).some((m) => message.test(m)), `${kind} toasts: ${toasts(kind)}`);
      const after = calls.slice(calls.indexOf(posts[0]) + 1);
      assert.ok(after.some((c) => c.method === 'GET' && c.path.startsWith('/api/brains')), 'and the page asks for the brains again');
    });
  }
});

const helps = (els) => [els].flat().flatMap((el) => el.querySelectorAll('.help').map((x) => x.textContent));
const fieldset = (root, legend) => root.querySelectorAll('fieldset').find((f) => f.querySelector('legend').textContent === legend);

test('the model, effort and backup settings are the wizard’s, and save with the rest', async (t) => {
  const m = { ...manifest('Work'), model: 'sonnet', effort: 'high', backup: { enabled: true, exclude: ['node_modules/'] }, prompt_file: 'brain-prompt.md', reply_chat: '' };
  const { root, calls } = await openPage(t, { brains: [{ ...view('Work'), manifest: m }] });
  const shared = brainSettingsFields();
  const model = fieldset(root, 'Model and tools');
  assert.deepEqual(helps(model).slice(0, 2), helps(shared.model), 'the model fields are the shared ones');
  assert.deepEqual(helps(fieldset(root, 'Backup')), helps(shared.backup), 'and so are the backup fields');
  const modelIn = model.querySelector('input');
  assert.equal(modelIn.value, 'sonnet');
  const effort = model.querySelector('select');
  assert.equal(effort.value, 'high');
  const backup = fieldset(root, 'Backup').querySelector('input[type=checkbox]');
  assert.equal(backup.checked, true);
  const excludes = fieldset(root, 'Backup').querySelector('textarea');
  assert.equal(excludes.value, 'node_modules/');

  type(modelIn, 'opus');
  effort.value = '';
  effort.dispatchEvent(new Event('change', { bubbles: true }));
  backup.checked = false;
  backup.dispatchEvent(new Event('change', { bubbles: true }));
  type(excludes, 'node_modules/\n*.log');
  root.querySelector('.brain-form').dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  const saved = calls.find((c) => c.path === '/api/brains/work' && c.method === 'PUT');
  assert.ok(saved, 'the settings were saved');
  assert.deepEqual(saved.body.manifest, { ...m, model: 'opus', effort: '', backup: { enabled: false, exclude: ['node_modules/', '*.log'] } });
  const toasts = (k) => document.querySelectorAll(`.toast.${k}`).map((el) => el.textContent);
  assert.deepEqual(toasts('error'), []);
  assert.ok(toasts('ok').includes('Settings saved'), `toasts: ${toasts('ok')}`);
});
