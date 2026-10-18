import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { resource } from '../assets/app/resources.js';
import page from '../assets/pages/integrations.js';

const status = (checked) => ({
  cli: { found: true }, connected: true, account: 'you@example.com', login_dir: '/Users/you/.fridolin/plaud',
  settings: { enabled: true, brain: '', polished: false, brain_access: false, message: '', interval_seconds: 60 },
  target_brain: 'work', last_poll: checked, seen: 3,
});

const button = (root, label) => root.querySelectorAll('button').find((b) => b.textContent === label);

async function openPage(t, first = status('2026-10-18T10:00:00Z')) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { status: first };
  const calls = serve(t, (path) => {
    if (path.startsWith('/api/plaud/status')) return bridge.status;
    if (path.startsWith('/api/brains')) return [{ id: 'work', name: 'Work' }];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const poll = async (next) => { bridge.status = next; await resource('/api/plaud/status').refresh(); await settle(); };
  const note = () => root.querySelector('.live-form').firstChild; // the "changed elsewhere" note
  return { root, ctx, poll, note, calls };
}

test('the Plaud status keeps refreshing while its settings have edits', async (t) => {
  const { root, ctx, poll, note } = await openPage(t);
  const msg = root.querySelector('textarea');
  type(msg, 'File it under recordings/');
  assert.equal(ctx.dirty(), true);
  await poll({ ...status('2026-10-18T10:05:00Z'), seen: 4 });
  assert.match(root.textContent, /4 recordings known/, 'the status follows the poll');
  assert.equal(root.querySelector('textarea'), msg, 'the settings were not rebuilt');
  assert.equal(msg.value, 'File it under recordings/');
  assert.equal(note().hidden, true, 'a new check is not a change to the settings');

  type(msg, '');
  assert.equal(ctx.dirty(), false, 'typed back, nothing is unsaved');
});

test('edited Plaud settings say when they changed elsewhere', async (t) => {
  const { root, poll, note } = await openPage(t);
  const msg = root.querySelector('textarea');
  type(msg, 'File it under recordings/');
  const now = status('2026-10-18T10:05:00Z');
  await poll({ ...now, settings: { ...now.settings, polished: true } }); // saved in another tab
  assert.equal(root.querySelector('textarea'), msg);
  assert.equal(msg.value, 'File it under recordings/', 'the edits are kept');
  assert.equal(note().hidden, false, 'and the change elsewhere is shown');
});

test('coming back to the page shows the Plaud status it knows at once', async (t) => {
  const first = await openPage(t);
  first.ctx.destroy(); // gone to another page
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  assert.match(root.textContent, /Signed in as you@example\.com/, 'painted before the bridge answers again');
});

test('cancelling the brain_access dialog leaves nothing unsaved', async (t) => {
  const { root, ctx } = await openPage(t);
  const access = root.querySelectorAll('label.switch').find((l) => l.textContent.includes('Let brains use')).input;
  access.checked = true;
  access.dispatchEvent(new Event('change', { bubbles: true }));
  await settle();
  assert.equal(ctx.dirty(), true, 'the change is kept while you decide');
  button(document.querySelector('dialog'), 'Cancel').click();
  await settle();
  assert.equal(access.checked, false, 'the switch is back off');
  assert.equal(ctx.dirty(), false, 'and nothing is unsaved');
});

// A card that waits for you to connect again still has a login on record:
// it offers Disconnect, which stops the checks and the daily reminder,
// without signing out a login in ~/.plaud that Fridolin did not make.
for (const c of [
  { name: 'the login expired', over: { connected: false, expired: true }, says: /daily reminder in the chat stops/ },
  {
    name: 'brain_access moved the login',
    over: { connected: false, login_moved: true, login_dir: '/Users/you/.plaud', settings: { ...status('').settings, brain_access: true } },
    says: /login in ~\/\.plaud\/ stays/,
  },
]) {
  test(`the card offers Disconnect when ${c.name}`, async (t) => {
    const { root, calls } = await openPage(t, { ...status('2026-10-18T10:00:00Z'), ...c.over });
    assert.ok(button(root, 'Connect again') || button(root, 'Connect Plaud'), 'connecting again is offered');
    button(root, 'Disconnect').click();
    await settle();
    const dialog = document.querySelector('dialog');
    assert.match(dialog.textContent, c.says);
    button(dialog, 'Disconnect').click(); // confirm
    await settle();
    assert.ok(calls.some((call) => call.path === '/api/plaud/disconnect' && call.method === 'POST'), 'Disconnect was posted');
  });
}

test('a card that was never connected offers only Connect', async (t) => {
  const { root } = await openPage(t, { ...status('2026-10-18T10:00:00Z'), connected: false });
  assert.deepEqual(root.querySelectorAll('button').map((b) => b.textContent), ['Connect Plaud']);
});
