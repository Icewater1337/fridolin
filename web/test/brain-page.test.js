import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { brainsResource, resource } from '../assets/app/resources.js';
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
    if (path.startsWith('/api/brains/work/intents/') && init.method === 'DELETE') return { status: 'removed' };
    if (path === '/api/brains/work') {
      const b = bridge.brains.find((x) => x.id === 'work');
      return b ? { status: 200, body: b } : { status: 404, body: { error: 'no such brain' } };
    }
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
  const note = () => /** @type {HTMLElement} */ (root.querySelector('.live-form').firstChild); // the "changed elsewhere" note
  const bar = () => root.querySelector('.save-bar');
  const poll = async (brains) => {
    bridge.brains = brains;
    await Promise.all([resource('/api/brains/work').refresh(), brainsResource.refresh()]);
    await settle();
  };
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

test('a scheduled agent\'s run shows beside the chat\'s', async (t) => {
  const { root, poll } = await openPage(t);
  await poll([view('Work', {
    running: null, queue: [],
    background: { chat: 'schedule:upkeep', sender: 'schedule', preview: '', silent: false, started: '2026-10-18T10:00:00Z', pid: 0 },
    background_queue: [{ chat: 'schedule:digest', sender: 'schedule', preview: '', silent: false, queued: '2026-10-18T10:05:00Z' }],
  })]);
  const text = root.textContent;
  assert.match(text, /Waiting for messages\./, 'no chat runs');
  assert.match(text, /Scheduled agent upkeep/, 'the agent that runs');
  assert.match(text, /Scheduled agent digest/, 'the agent that waits');
  assert.match(text, /1 waiting/);
  assert.ok(root.querySelectorAll('button').some((b) => b.textContent === 'Stop'), 'the agent\'s run can be stopped');
});

test('a setting typed back to what it was is no unsaved change', async (t) => {
  const { ctx, nameIn, bar } = await openPage(t);
  type(nameIn(), 'Home');
  type(nameIn(), 'Work');
  assert.equal(ctx.dirty(), false);
  assert.equal(bar().hidden, true);
});

test('edits whose form left the page are not unsaved changes', async (t) => {
  /** @type {[string, object[]][]} */
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
  const all = ['Now', 'Settings', 'Calendar', 'Standing intents', 'Skills', 'Recent messages', 'Remove'];
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
  /** @type {[string, {status: number, body: object}, [string, RegExp]][]} */
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
  assert.deepEqual(saved.body.manifest, { ...m, model: 'opus', effort: '', observe: 'history', approvals: 'ask', backup: { enabled: false, exclude: ['node_modules/', '*.log'] } });
  const toasts = (k) => document.querySelectorAll(`.toast.${k}`).map((el) => el.textContent);
  assert.deepEqual(toasts('error'), []);
  assert.ok(toasts('ok').includes('Settings saved'), `toasts: ${toasts('ok')}`);
});

test('the page polls its own brain, and the list of all only for the others’ routing', async (t) => {
  const { calls } = await openPage(t);
  const count = (path) => calls.filter((c) => c.method === 'GET' && c.path === path).length;
  for (let i = 0; i < 4; i++) {
    t.mock.timers.tick(3000);
    await settle();
  }
  assert.equal(count('/api/brains/work'), 5, 'its brain every 3 s');
  assert.equal(count('/api/brains'), 1, 'the list once');
  t.mock.timers.tick(60000 - 12000);
  await settle();
  assert.equal(count('/api/brains'), 2, 'and then every minute');
});

test('a brain that is not there is asked after every 30 s, not every 3 s', async (t) => {
  const { root, calls, poll } = await openPage(t, { brains: [] });
  const count = () => calls.filter((c) => c.method === 'GET' && c.path === '/api/brains/work').length;
  assert.match(root.textContent, /No such brain/);
  for (let i = 0; i < 9; i++) {
    t.mock.timers.tick(3000);
    await settle();
  }
  assert.equal(count(), 1, 'each 404 is an error in the browser\'s console');
  t.mock.timers.tick(3000);
  await settle();
  assert.equal(count(), 2);
  await poll([view('Work')]);
  const before = count();
  t.mock.timers.tick(3000);
  await settle();
  assert.equal(count(), before + 1, 'every 3 s again once it is there');
});

test('the settings say what untagged group messages get, and save it', async (t) => {
  const { root, calls } = await openPage(t);
  const observe = root.querySelector('.brain-form select[name=observe]');
  assert.equal(observe.value, 'history', 'a brain.json without the setting keeps them in the history');
  observe.value = 'run';
  observe.dispatchEvent(new Event('change', { bubbles: true }));
  root.querySelector('.brain-form').dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  const saved = calls.filter((c) => c.method === 'PUT').map((c) => [c.path, c.body.manifest.observe]);
  assert.deepEqual(saved, [['/api/brains/work', 'run']]);
});

test('the settings say whether the brain asks before an irreversible step, and save it', async (t) => {
  const { root, calls } = await openPage(t);
  const approvals = root.querySelector('.brain-form select[name=approvals]');
  assert.equal(approvals.value, 'ask', 'a brain.json without the setting asks');
  assert.match(fieldset(root, 'Approvals').textContent, /security trade-off/);
  approvals.value = 'auto';
  approvals.dispatchEvent(new Event('change', { bubbles: true }));
  root.querySelector('.brain-form').dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  const saved = calls.filter((c) => c.method === 'PUT').map((c) => [c.path, c.body.manifest.approvals]);
  assert.deepEqual(saved, [['/api/brains/work', 'auto']]);
});

const other = (id, name, extra = {}) => ({ id, name, path: `/Users/you/${id}`, status: 'ok', issues: [], manifest: { version: 1, id, name, routing: { handles_dms: false, default: false, groups: [] }, ...extra } });
const save = async (root, calls) => {
  root.querySelector('.brain-form').dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  return calls.filter((c) => c.method === 'PUT' && c.path === '/api/brains/work').map((c) => c.body.manifest);
};

test('the core brain’s grants are edited per brain and saved in its brain.json', async (t) => {
  const core = { ...view('Work'), manifest: { ...manifest('Work'), role: 'core', grants: { family: ['read'] } } };
  const { root, calls } = await openPage(t, { brains: [core, other('family', 'Family'), other('taxes', 'Taxes')] });
  const set = fieldset(root, 'Other brains');
  assert.ok(set, 'the form has the core brain’s section');
  const coreSw = set.querySelector('input[role=switch]');
  assert.equal(coreSw.checked, true);
  const box = (label) => set.querySelectorAll('input[type=checkbox]').find((i) => i.getAttribute('aria-label') === label);
  assert.equal(box('Read notes: Family').checked, true);
  assert.equal(box('Delegate: Family').checked, false);
  assert.equal(box('Delegate: Taxes').checked, false);

  box('Delegate: Family').checked = true;
  box('Delegate: Family').dispatchEvent(new Event('change', { bubbles: true }));
  box('Files: Taxes').checked = true;
  box('Files: Taxes').dispatchEvent(new Event('change', { bubbles: true }));
  const [saved] = await save(root, calls);
  assert.equal(saved.role, 'core');
  assert.deepEqual(saved.grants, { family: ['delegate', 'read'], taxes: ['files'] }, 'kinds sorted, as brain.json keeps them');
});

test('turning the core brain off saves neither role nor grants', async (t) => {
  const core = { ...view('Work'), manifest: { ...manifest('Work'), role: 'core', grants: { family: ['delegate'] } } };
  const { root, calls } = await openPage(t, { brains: [core, other('family', 'Family')] });
  const coreSw = fieldset(root, 'Other brains').querySelector('input[role=switch]');
  coreSw.checked = false;
  coreSw.dispatchEvent(new Event('change', { bubbles: true }));
  const [saved] = await save(root, calls);
  assert.equal('role' in saved, false);
  assert.equal('grants' in saved, false);
});

test('a sub-brain shows no grants, and cannot become a second core', async (t) => {
  const { root, calls } = await openPage(t, { brains: [view('Work'), other('main', 'Main', { role: 'core' })] });
  const set = fieldset(root, 'Other brains');
  const coreSw = set.querySelector('input[role=switch]');
  assert.equal(coreSw.checked, false);
  assert.equal(coreSw.disabled, true, 'Main is the core brain');
  assert.match(set.textContent, /Main is the core brain, and there is one/);
  assert.equal(set.querySelector('.grants-editor').parentNode.parentNode.hidden, true, 'no grants on a sub-brain');
  const [saved] = await save(root, calls);
  assert.equal('role' in saved || 'grants' in saved, false);
});

test('standing intents are listed, and one is removed after a confirmation', async (t) => {
  const intents = [
    { id: 'i1a2b3c', text: 'Remind the owner to call the dentist', schedule: { kind: 'weekly', day: 'sun', time: '18:00' }, chat: '15551234567@s.whatsapp.net', created_by: '15551234567', created: '2026-10-18T10:00:00Z' },
    { id: 'i4d5e6f', text: 'Tell the owner about the boat', match: ['boat', { text: 'harbour', match: 'word' }], chat: '15551234567@s.whatsapp.net', created_by: '15551234567', created: '2026-10-18T10:00:00Z' },
  ];
  const { root, calls } = await openPage(t, { brains: [{ ...view('Work'), intents }] });
  const items = root.querySelectorAll('.intent-list li');
  assert.equal(items.length, 2);
  assert.match(items[0].textContent, /call the dentist.*every sun at 18:00/);
  assert.match(items[1].textContent, /when a message mentions boat or harbour, once/);
  items[0].querySelectorAll('button').find((b) => b.textContent.includes('Remove')).click();
  await settle();
  const dialog = document.querySelector('dialog');
  assert.ok(dialog, 'a confirm dialog');
  dialog.querySelectorAll('button').find((b) => b.textContent === 'Remove').click();
  await settle();
  assert.ok(calls.some((c) => c.method === 'DELETE' && c.path === '/api/brains/work/intents/i1a2b3c'), 'the intent was removed');
});

test('a brain without standing intents says how one is set', async (t) => {
  const { root } = await openPage(t);
  const section = root.querySelectorAll('section.section').find((s) => s.querySelector('h2').textContent === 'Standing intents');
  assert.match(section.textContent, /remind you/);
});
