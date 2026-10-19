import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { ignoredResource, configResource, brainsResource } from '../assets/app/resources.js';
import { loadGroups } from '../assets/app/store.js';
import page from '../assets/pages/chats.js';

const GROUP = '120363000000000000@g.us';
const WAITING = '120363000000000001@g.us';
const brain = (id, routing = {}) => ({ id, name: id, status: 'ok', manifest: { id, name: id, routing: { groups: [], ...routing } } });

async function openPage(t, { brains = [brain('work', { default: true }), brain('family')], members = {} } = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = {
    numbers: ['15551234567'],
    ignored: [{ kind: 'group', jid: WAITING, name: 'Book club', count: 1, last_seen: '2026-10-18T10:00:00Z' }],
    brains,
    groups: [{ jid: GROUP, name: 'Family', participants: 4, allowed: true, brain: 'work' }],
    members,
    refuse: null, // what /api/chats/members answers with 422 instead of saving
  };
  const calls = serve(t, (path, init) => {
    if (path === '/api/chats/allow' && init.method === 'POST') {
      const body = JSON.parse(init.body);
      if (body.number === 'call me') return { status: 400, body: { error: '"call me" is not a phone number' } };
      return { status: 200, body: { status: 'allowed' } };
    }
    if (path === '/api/chats/revoke' && init.method === 'POST') {
      const body = JSON.parse(init.body);
      if (body.kind === 'dm' && !bridge.numbers.includes(body.number)) return { status: 404, body: { error: `${body.number} is not in allowed_numbers` } };
      bridge.numbers = bridge.numbers.filter((n) => n !== body.number);
      return { status: 200, body: { status: 'revoked' } };
    }
    if (path === '/api/chats/members' && init.method === 'POST') {
      const body = JSON.parse(init.body);
      if (bridge.refuse) return { status: 422, body: { error: bridge.refuse } };
      if (body.members === null) delete bridge.members[body.jid];
      else bridge.members[body.jid] = body.members.map((n) => (n === '*' ? n : n.replace(/\D/g, '')));
      return { status: 200, body: { status: 'saved' } };
    }
    if (path.startsWith('/api/chats/ignored')) return bridge.ignored;
    if (path.startsWith('/api/brains')) return bridge.brains;
    if (path.startsWith('/api/config')) return { etag: 'e', config: { allowed_numbers: bridge.numbers, allowed_groups: [GROUP], group_members: { ...bridge.members } }, effective: {} };
    if (path.startsWith('/api/whatsapp/groups')) return bridge.groups;
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const addNumber = () => root.querySelectorAll('form.add-form')[0];
  const waitingSelect = () => root.querySelector('.waiting-row select');
  const groupSelect = () => root.querySelector('.groups-table select');
  return { root, bridge, calls, addNumber, waitingSelect, groupSelect };
}

const toasts = (kind) => document.querySelectorAll(`.toast.${kind}`).map((el) => el.textContent);

test('typed text and a picked brain survive refreshes', async (t) => {
  const { root, bridge, addNumber, waitingSelect } = await openPage(t);
  const input = addNumber().querySelector('input');
  type(input, '+1 555 987');
  const sel = waitingSelect();
  sel.value = 'family';

  bridge.numbers = ['15551234567', '15559876543']; // added in another tab
  bridge.ignored = [{ ...bridge.ignored[0], count: 2 }]; // the group wrote again
  await Promise.all([configResource.refresh(), ignoredResource.refresh()]);
  await settle();
  assert.match(root.textContent, /2 messages/, 'the waiting row shows the new count');
  assert.match(root.textContent, /\+15559876543/, 'the list shows the new number');
  assert.equal(addNumber().querySelector('input'), input);
  assert.equal(input.value, '+1 555 987', 'the half-typed number is still there');
  assert.equal(waitingSelect(), sel);
  assert.equal(sel.value, 'family', 'the picked brain is still picked');
});

test('the bridge checks a number, and its refusal shows under the box', async (t) => {
  const { calls, addNumber } = await openPage(t);
  const form = addNumber();
  const input = form.querySelector('input');
  type(input, 'call me');
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  const err = form.querySelector('.form-error');
  assert.equal(err.hidden, false);
  assert.match(err.textContent, /not a phone number/);
  assert.equal(input.value, 'call me', 'kept for fixing');
  assert.deepEqual(calls.filter((c) => c.method === 'POST').map((c) => [c.path, c.body]), [['/api/chats/allow', { kind: 'dm', number: 'call me' }]]);

  type(input, '+1 555 987 6543');
  assert.equal(err.hidden, true, 'typing clears the refusal');
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  assert.equal(input.value, '', 'an accepted number clears the box');
});

test('removing a number asks the bridge to revoke it', async (t) => {
  const { root, calls } = await openPage(t);
  root.querySelector('.plain-list button').click();
  await settle();
  document.querySelectorAll('dialog button').find((b) => b.textContent === 'Remove').click();
  await settle();
  assert.deepEqual(calls.filter((c) => c.method === 'POST').map((c) => [c.path, c.body]), [['/api/chats/revoke', { kind: 'dm', number: '15551234567' }]]);
});

test('a brain picked for a group survives a refresh, and follows a change elsewhere', async (t) => {
  const { root, bridge, groupSelect } = await openPage(t, {
    brains: [brain('work', { default: true }), brain('family', { groups: [GROUP] }), brain('club')],
  });
  await loadGroups(true);
  await settle();
  const sel = groupSelect();
  assert.equal(sel.value, 'family', 'it shows the brain the group goes to');
  sel.value = 'club';

  bridge.groups = [{ ...bridge.groups[0], participants: 5 }]; // someone joined
  bridge.brains = [...bridge.brains, brain('garden')]; // a brain was added
  await Promise.all([loadGroups(true), brainsResource.refresh()]);
  await settle();
  assert.match(root.querySelector('.groups-table tbody').textContent, /5/, 'the row shows the new count');
  assert.equal(groupSelect(), sel);
  assert.equal(sel.value, 'club', 'the picked brain is still picked');

  // Routed to another brain elsewhere: the row shows where it goes now.
  bridge.brains = [brain('work', { default: true, groups: [GROUP] }), brain('family'), brain('club'), brain('garden')];
  await brainsResource.refresh();
  await settle();
  assert.equal(groupSelect(), sel);
  assert.equal(sel.value, 'work');
});

test('only the groups’ Refresh button has the bridge ask WhatsApp', async (t) => {
  const { root, calls } = await openPage(t);
  const asked = () => calls.filter((c) => c.path.startsWith('/api/whatsapp/groups')).map((c) => c.path);
  await loadGroups(true); // what a change on the page does
  const before = asked().length;
  assert.ok(before > 0 && asked().every((p) => p === '/api/whatsapp/groups'), `the page takes the bridge's list: ${asked()}`);
  root.querySelectorAll('button').find((b) => b.textContent.includes('Refresh')).click();
  await settle();
  assert.deepEqual(asked().slice(before), ['/api/whatsapp/groups?fresh=1']);
});

test('removing a number that is gone already says so, not that it was removed', async (t) => {
  const { root, bridge } = await openPage(t);
  bridge.numbers = []; // removed in another tab, before the page's next poll
  root.querySelector('.plain-list button').click();
  await settle();
  document.querySelectorAll('dialog button').find((b) => b.textContent === 'Remove').click();
  await settle();
  assert.deepEqual(toasts('ok'), []);
  assert.deepEqual(toasts('info'), ['15551234567 is not in allowed_numbers']);
  assert.match(root.textContent, /nobody can send direct messages yet/, 'the list shows it is gone');
});

// The allowed groups' list, the editor of who is heard in the first, and
// what the page asked the bridge to save.
const heard = (root) => root.querySelectorAll('.plain-list')[1].querySelector('.heard').textContent;
const dialogButton = (label) => document.querySelectorAll('dialog button').find((b) => b.textContent === label);
async function openMembers(root) {
  root.querySelectorAll('button').find((b) => b.textContent === 'Members').click();
  await settle();
  return document.querySelector('dialog');
}
const saved = (calls) => calls.filter((c) => c.path === '/api/chats/members').map((c) => c.body);

test('an allowed group says who is heard there, and the editor saves a list of numbers', async (t) => {
  const { root, calls } = await openPage(t);
  assert.equal(heard(root), 'Heard: the allowed numbers', 'a group without an entry hears the allowed numbers');

  const dlg = await openMembers(root);
  const mode = dlg.querySelector('select');
  assert.equal(mode.value, '', 'the editor starts from the group’s entry: none');
  mode.value = 'list';
  mode.dispatchEvent(new Event('change'));
  dialogButton('Add number').click();
  type(dlg.querySelector('.list-row input'), '+1 555 987 6543');
  dialogButton('Save').click();
  await settle();
  assert.deepEqual(saved(calls), [{ jid: GROUP, members: ['+1 555 987 6543'] }]);
  assert.equal(document.querySelector('dialog'), null, 'saved, the editor closes');
  assert.equal(heard(root), 'Heard: +15559876543');

  // Opened again, it shows the list the bridge keeps.
  const again = await openMembers(root);
  assert.equal(again.querySelector('select').value, 'list');
  assert.deepEqual(again.querySelectorAll('.list-row input').map((i) => i.value), ['15559876543']);
});

test('every member, and back to the allowed numbers', async (t) => {
  const { root, calls } = await openPage(t, { members: { [GROUP]: ['15551234567'] } });
  assert.equal(heard(root), 'Heard: +15551234567');
  for (const [value, members, summary] of [['*', ['*'], 'Heard: every member'], ['', null, 'Heard: the allowed numbers']]) {
    const dlg = await openMembers(root);
    const mode = dlg.querySelector('select');
    mode.value = value;
    mode.dispatchEvent(new Event('change'));
    assert.equal(dlg.querySelector('.list-editor').closest('.field').hidden, true, 'the numbers are only for a list');
    dialogButton('Save').click();
    await settle();
    assert.deepEqual(saved(calls).at(-1), { jid: GROUP, members });
    assert.equal(heard(root), summary);
  }
});

test('a number that is none stays in the editor, and so does the bridge’s refusal', async (t) => {
  const { root, bridge, calls } = await openPage(t);
  const dlg = await openMembers(root);
  const mode = dlg.querySelector('select');
  mode.value = 'list';
  mode.dispatchEvent(new Event('change'));
  dialogButton('Add number').click();
  for (const bad of ['call me', '15551234567@lid', '*']) {
    type(dlg.querySelector('.list-row input'), bad);
    dialogButton('Save').click();
    await settle();
    assert.deepEqual(saved(calls), [], `${bad} is not sent`);
    const err = dlg.querySelector('.form-error');
    assert.equal(err.hidden, false);
    assert.match(err.textContent, /no phone numbers/);
    assert.ok(document.querySelector('dialog'), 'the editor stays open to fix it');
  }

  bridge.refuse = `group_members: ${GROUP} is not in allowed_groups: allow the group too, or remove its entry`;
  type(dlg.querySelector('.list-row input'), '15559876543');
  dialogButton('Save').click();
  await settle();
  assert.equal(saved(calls).length, 1);
  assert.match(dlg.querySelector('.form-error').textContent, /is not in allowed_groups/);
  assert.ok(document.querySelector('dialog'), 'refused, the editor stays open');
  assert.deepEqual(toasts('ok'), []);
});
