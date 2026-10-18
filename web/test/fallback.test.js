// The fallback brain: the one the chats no brain claims go to, and the
// DMs while no brain handles them. Without a default brain it is the first
// brain, which no brain.json names, so the dashboard takes it from the
// bridge (/api/brains marks it) and names it where those chats show.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { routingSummary, hasDMBrain, defaultBrainOption, takeover } from '../assets/lib/brain.js';
import { setStatus } from '../assets/app/store.js';
import chatsPage from '../assets/pages/chats.js';
import brainPage from '../assets/pages/brain.js';
import brainsPage from '../assets/pages/brains.js';
import overviewPage from '../assets/pages/overview.js';
import addBrainPage from '../assets/pages/add-brain.js';

const WAITING = '120363000000000001@g.us';
const GROUP = '120363000000000000@g.us';

// brain(id, routing, more): an /api/brains entry of a brain that runs.
const brain = (id, routing = {}, more = {}) => ({
  id, name: id, path: `/Users/you/${id}`, status: 'ok', issues: [],
  manifest: { version: 1, id, name: id, routing: { groups: [], ...routing } },
  runtime: { id, name: id, routing: { groups: [], ...routing }, queue: [] },
  fallback: false,
  ...more,
});

test('routingSummary names the fallback brain', async (t) => {
  const cases = [
    ['the first brain, without a default', { groups: ['x'] }, { fallback: true }, 'Fallback for other chats · 1 group'],
    ['and no brain handles DMs', {}, { fallback: true, noDMBrain: true }, 'Fallback for other chats and direct messages'],
    ['the default brain', { default: true }, { fallback: true }, 'Fallback for other chats'],
    ['the DM brain that is the fallback', { handles_dms: true }, { fallback: true }, 'Direct messages · Fallback for other chats'],
    ['a default brain the bridge does not fall back to', { default: true }, {}, 'Default for other chats'],
    ['not the fallback, with no DM brain', { groups: ['x'] }, { noDMBrain: true }, '1 group'],
    ['a fallback without a brain.json the page knows', null, { fallback: true }, 'Fallback for other chats'],
  ];
  for (const [name, routing, opts, want] of cases) {
    await t.test(name, () => assert.equal(routingSummary(routing, opts), want));
  }
});

test('hasDMBrain', async (t) => {
  const cases = [
    ['none', [brain('a'), brain('b')], false],
    ['one, running', [brain('a'), brain('b', { handles_dms: true })], true],
    // It keeps the DMs while it cannot run: no other brain gets them.
    ['one that does not run', [brain('a'), brain('b', { handles_dms: true }, { runtime: undefined, status: 'missing' })], true],
    ['an entry without a brain.json', [{ path: '/Users/you/x', status: 'unapproved', manifest: null }], false],
  ];
  for (const [name, list, want] of cases) {
    await t.test(name, () => assert.equal(hasDMBrain(list), want));
  }
});

test('defaultBrainOption names the fallback brain', async (t) => {
  const cases = [
    ['the first brain, without a default', [brain('family', {}, { fallback: true }), brain('work')], 'Default brain (family)'],
    ['the default brain', [brain('family'), brain('work', { default: true }, { fallback: true, name: 'Office' })], 'Default brain (Office)'],
    ['none', [], 'Default brain'],
  ];
  for (const [name, list, want] of cases) {
    await t.test(name, () => assert.equal(defaultBrainOption(list), want));
  }
});

test('takeover names the brain that answers a removed brain’s chats', async (t) => {
  const personal = brain('personal', { handles_dms: true });
  const cases = [
    // name, list, removed, the brain that takes over, whether it starts on the DMs
    ['a group brain', [personal, brain('work', { default: true }, { fallback: true }), brain('club', { groups: [GROUP] })], 'club', 'work', false],
    ['the DM brain', [personal, brain('work', { default: true }, { fallback: true })], 'personal', 'work', true],
    ['the first brain, the fallback, with no DM brain', [brain('family', { groups: [GROUP] }, { fallback: true }), brain('work')], 'family', 'work', true],
    ['the fallback, with a DM brain', [brain('work', { default: true }, { fallback: true }), personal, brain('club')], 'work', 'personal', false],
    ['the fallback, before a brain that does not run', [brain('a', {}, { fallback: true }), brain('b', {}, { runtime: undefined, status: 'missing' }), brain('c')], 'a', 'c', true],
    ['the only brain', [brain('solo', { handles_dms: true }, { fallback: true })], 'solo', null, false],
  ];
  for (const [name, list, removed, want, dms] of cases) {
    await t.test(name, () => {
      const got = takeover(list, removed);
      assert.equal(got.brain ? got.brain.id : null, want);
      assert.equal(got.dms, dms);
      assert.equal(got.copy, false);
    });
  }
  // The bridge removes the first entry with the id: a second folder that
  // claims it, which does not run now, is left and takes over.
  await t.test('a brain with a second folder of the same id', () => {
    const copy = brain('family', {}, { path: '/Users/you/family-copy', runtime: undefined, status: 'invalid' });
    const got = takeover([brain('family', { handles_dms: true }, { fallback: true }), brain('work'), copy], 'family');
    assert.equal(got.brain, copy);
    assert.equal(got.copy, true);
    assert.equal(got.dms, false);
  });
});

// render(t, page, brains, status): the page, with brains the /api/brains
// list and status the app's status; resolves to its root.
async function render(t, page, brains, status = null) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  serve(t, (path) => {
    if (path.startsWith('/api/brains')) return brains;
    if (path.startsWith('/api/activity')) return [];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => { ctx.destroy(); setStatus(null); });
  if (status) setStatus({ whatsapp: { state: 'connected' }, issues: [], warnings: [], claude: {}, sandbox: {}, backup: {}, ...status });
  page.render(root, ctx);
  await settle();
  return root;
}

test('the Brains page and the Overview say which brain is the fallback', async (t) => {
  // No default brain and no DM brain: the first brain gets both.
  const brains = [brain('family', { groups: [GROUP] }, { fallback: true }), brain('work')];
  await t.test('Brains', async (t) => {
    const root = await render(t, brainsPage, brains);
    const facts = root.querySelectorAll('.brain-card').map((c) => c.querySelector('.bc-facts').textContent);
    assert.deepEqual(facts, ['Fallback for other chats and direct messages · 1 group', 'No chats routed yet']);
  });
  await t.test('Overview', async (t) => {
    const root = await render(t, overviewPage, brains, { brains: brains.map((b) => b.runtime) });
    const rows = root.querySelectorAll('.brain-row').map((r) => r.querySelector('.muted.small').textContent);
    assert.deepEqual(rows, ['Fallback for other chats and direct messages · 1 group', 'No chats routed yet']);
  });
});

test('the Chats page names the fallback as the default brain for a group', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  serve(t, (path) => {
    if (path.startsWith('/api/chats/ignored')) return [{ kind: 'group', jid: WAITING, name: 'Book club', count: 1, last_seen: '2026-10-18T10:00:00Z' }];
    // No brain is marked default: the first one is the fallback.
    if (path.startsWith('/api/brains')) return [brain('family', { groups: [GROUP] }, { fallback: true }), brain('work')];
    if (path.startsWith('/api/config')) return { etag: 'e', config: { allowed_numbers: [], allowed_groups: [GROUP] }, effective: {} };
    if (path.startsWith('/api/whatsapp/groups')) return [];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  chatsPage.render(root, ctx);
  await settle();
  const sel = root.querySelector('.waiting-row select');
  assert.ok(sel, 'the waiting group has a brain to pick');
  assert.equal(sel.value, '', 'it goes to the default brain unless one is picked');
  assert.equal(sel.querySelector('option').textContent, 'Default brain (family)');
});

// removeDialogOf(t, brains, id): opens the Brain page of brain id, with
// brains the /api/brains list, and resolves to the text of its Remove
// dialog, closed again without removing anything.
async function removeDialogOf(t, brains, id) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, (path) => {
    if (path.startsWith(`/api/brains/${id}/calendar`)) return { client_configured: false, connected: false };
    // A brain's entry has a status field of its own: answer it as the body.
    if (path === `/api/brains/${id}`) return { status: 200, body: brains.find((b) => b.id === id) };
    if (path.startsWith('/api/brains')) return brains;
    if (path.startsWith('/api/config')) return { etag: 'e1', path: '/Users/you/.fridolin/config.json', config: {}, effective: {} };
    if (path.startsWith('/api/whatsapp/groups')) return [];
    if (path.startsWith('/api/activity')) return [];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage({ params: { id } });
  t.after(() => ctx.destroy());
  brainPage.render(root, ctx);
  await settle();
  root.querySelectorAll('button').find((b) => b.textContent.includes('Remove brain')).click();
  await settle();
  const text = document.querySelector('dialog').textContent;
  document.querySelectorAll('dialog button').find((b) => b.textContent === 'Cancel').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.method === 'DELETE'), [], 'nothing was removed');
  return text;
}

test('the Remove dialog names the brain that takes over', async (t) => {
  await t.test('a group brain: the fallback brain', async (t) => {
    const text = await removeDialogOf(t, [brain('personal', { handles_dms: true }), brain('work', { default: true }, { fallback: true }), brain('club', { groups: [GROUP] })], 'club');
    assert.match(text, /Its chats go to the fallback brain, work\./);
    assert.doesNotMatch(text, /direct messages/);
  });
  await t.test('the implicit fallback: the next brain, which gets the DMs too', async (t) => {
    const text = await removeDialogOf(t, [brain('family', { groups: [GROUP] }, { fallback: true }), brain('work')], 'family');
    assert.match(text, /It is the fallback brain, so its chats and those no other brain claims go to work, which becomes the fallback\./);
    assert.match(text, /work will also answer your direct messages, since no other brain handles them/);
  });
  await t.test('the DM brain: a warning that the fallback gets the DMs', async (t) => {
    const text = await removeDialogOf(t, [brain('personal', { handles_dms: true }), brain('work', { default: true }, { fallback: true })], 'personal');
    assert.match(text, /Its chats go to the fallback brain, work\./);
    assert.match(text, /work will also answer your direct messages/);
  });
  await t.test('the only brain: nobody', async (t) => {
    const text = await removeDialogOf(t, [brain('solo', { handles_dms: true }, { fallback: true })], 'solo');
    assert.match(text, /No other brain takes its chats over/);
  });
  await t.test('a brain with a second folder of the same id: that folder', async (t) => {
    const copy = brain('family', {}, { path: '/Users/you/family-copy', runtime: undefined, status: 'invalid' });
    const text = await removeDialogOf(t, [brain('family', { groups: [GROUP] }, { fallback: true }), brain('work'), copy], 'family');
    assert.match(text, /Another folder with this id stays in the list, \/Users\/you\/family-copy: it takes this brain’s place/);
    assert.doesNotMatch(text, /work/);
  });
});

// addThroughWizard(t, before, after, off): adds the brain Notes (id
// notes) with the Add-brain wizard, with before the /api/brains list until
// it is added and after the list then, turning off the routing switches
// labelled off; resolves to the done step.
async function addThroughWizard(t, before, after, off) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  globalThis.window = { scrollTo() {} };
  t.after(() => { delete globalThis.window; });
  let added = false;
  serve(t, (path, init) => {
    if (path.startsWith('/api/fs/capabilities')) return { picker: '', home: '/Users/you' };
    if (path.startsWith('/api/fs/list')) return { path: '/Users/you', home: '/Users/you', parent: '', entries: [] };
    if (path.startsWith('/api/brains/inspect')) return { exists: false, abs_path: '/Users/you/notes', suggested: { name: 'Notes', id: 'notes' } };
    if (path === '/api/brains' && init.method === 'POST') {
      added = true;
      return { id: 'notes', path: '/Users/you/notes', created: ['brain.json'] };
    }
    if (path === '/api/brains') return added ? after : before;
    if (path.startsWith('/api/config')) return { config: { allowed_groups: [GROUP] }, effective: {} };
    if (path.startsWith('/api/whatsapp/groups')) return [];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  addBrainPage.render(root, ctx);
  await settle();
  const button = (label) => root.querySelectorAll('button').find((b) => b.textContent === label);
  button('Type a path').click();
  const typed = root.querySelector('form.inline-add');
  type(typed.querySelector('input'), '~/notes');
  typed.dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  button('Continue').click();
  await settle();
  for (const label of off) {
    const sw = root.querySelectorAll('label.switch').find((l) => l.textContent === label).querySelector('input');
    sw.checked = false;
    sw.dispatchEvent(new Event('change', { bubbles: true }));
  }
  root.querySelector('form.wizard-card').dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  const done = root.querySelector('.done-card');
  assert.ok(done, 'the brain was added');
  return done;
}

test('the Add-brain wizard says when the new brain is the fallback', async (t) => {
  const notes = (routing, more) => brain('notes', routing, more);
  const DM = 'Handles direct messages';
  const DEF = 'Default for unassigned chats';
  const cases = [
    // name, before, after, switches turned off, the fallback note (null: none), whether "No chats yet" shows
    ['the first brain, without DMs or default: it gets the other chats and the DMs', [], [notes({}, { fallback: true })], [DM, DEF],
      'Notes answers the allowed groups no other brain claims, and your direct messages too, since no brain handles them. To hand them to another brain, turn on “Default for unassigned chats” there. See The fallback brain in docs/BRAINS.md.', false],
    ['the first brain, marked default', [], [notes({ default: true }, { fallback: true })], [DM],
      'Notes answers the allowed groups no other brain claims, and your direct messages too, since no brain handles them. See The fallback brain in docs/BRAINS.md.', false],
    ['the fallback, while the DM brain does not run', [brain('personal', { handles_dms: true }, { runtime: undefined, status: 'missing' })],
      [brain('personal', { handles_dms: true }, { runtime: undefined, status: 'missing' }), notes({}, { fallback: true })], [],
      'Notes answers the allowed groups no other brain claims. To hand them to another brain, turn on “Default for unassigned chats” there. See The fallback brain in docs/BRAINS.md.', false],
    ['a brain after the fallback, without chats', [brain('work', { default: true }, { fallback: true })],
      [brain('work', { default: true }, { fallback: true }), notes({})], [DM], null, true],
  ];
  for (const [name, before, after, off, note, quiet] of cases) {
    await t.test(name, async (t) => {
      const done = await addThroughWizard(t, before, after, off);
      const callouts = done.querySelectorAll('.callout');
      const fallback = callouts.find((c) => c.querySelector('strong') && c.querySelector('strong').textContent === 'The fallback brain');
      assert.equal(fallback ? fallback.querySelector('.callout-text').textContent : null, note);
      assert.equal(callouts.some((c) => c.textContent.includes('No chats yet')), quiet);
    });
  }
});
