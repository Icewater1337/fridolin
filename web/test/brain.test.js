import { test } from 'node:test';
import assert from 'node:assert/strict';
import { BRAIN_ID_RE, BRIDGE_BACKUP_ID, slugify, routingSummary, brainLabel, backupOwner, issueLabel } from '../assets/lib/brain.js';
import { installDom } from './fake-dom.js';
import { brainStatusPill, runtimeSummary } from '../assets/components/brain.js';
import { h } from '../assets/lib/dom.js';

test('slugify', async (t) => {
  const cases = [
    ['Personal', 'personal'],
    ['My Work Brain', 'my-work-brain'],
    ['  --Mixed__Case.name  ', 'mixed-case-name'],
    ['Ünïcödé', 'ncd'],
    ['!!!', 'brain'],
    ['', 'brain'],
    ['a'.repeat(39) + ' b', 'a'.repeat(39)],
  ];
  for (const [input, want] of cases) {
    await t.test(JSON.stringify(input), () => {
      const got = slugify(input);
      assert.equal(got, want);
      assert.match(got, BRAIN_ID_RE);
    });
  }
});

test('BRAIN_ID_RE', async (t) => {
  const cases = [['personal', true], ['a_b-1', true], ['-lead', false], ['Upper', false], ['a'.repeat(40), true], ['a'.repeat(41), false]];
  for (const [id, want] of cases) {
    await t.test(id, () => assert.equal(BRAIN_ID_RE.test(id), want));
  }
});

test('routingSummary', async (t) => {
  const cases = [
    ['none', null, 'No chats'],
    ['empty', {}, 'No chats routed yet'],
    ['DMs', { handles_dms: true }, 'Direct messages'],
    ['everything', { handles_dms: true, default: true, groups: ['x', 'y'] }, 'Direct messages · Default for other chats · 2 groups'],
    ['one group', { groups: ['x'] }, '1 group'],
  ];
  for (const [name, routing, want] of cases) {
    await t.test(name, () => assert.equal(routingSummary(routing), want));
  }
});

test('brainLabel', async (t) => {
  const cases = [
    ['a name of its own', { id: 'work', name: 'Office' }, 'Office (work)'],
    ['the name is the id', { id: 'work', name: 'work' }, 'work'],
    ['no name', { id: 'work' }, 'work'],
  ];
  for (const [name, b, want] of cases) {
    await t.test(name, () => assert.equal(brainLabel(b), want));
  }
});

test('backupOwner', async (t) => {
  const brains = [{ id: 'work', name: 'Office' }, { id: 'notes' }];
  const cases = [
    ['the bridge', BRIDGE_BACKUP_ID, 'Bridge settings'],
    ['a brain with a name', 'work', 'Office'],
    ['a brain without one', 'notes', 'notes'],
    ['a brain that is gone', 'old', 'old'],
  ];
  for (const [name, id, want] of cases) {
    await t.test(name, () => assert.equal(backupOwner(id, brains), want));
  }
  assert.equal(BRAIN_ID_RE.test(BRIDGE_BACKUP_ID), false, 'no brain can have the bridge’s id');
});

test('one name per issue kind, short on a pill', async (t) => {
  installDom();
  const cases = [
    // kind, long, short (the status pill)
    ['drift', 'brain.json changed on disk', 'Changed on disk'],
    ['missing', 'Brain folder missing', 'Folder missing'],
    ['invalid', 'Invalid brain configuration', 'Invalid'],
    ['unapproved', 'Brain not approved', 'Not approved'],
    ['someday', 'someday', 'someday'],
  ];
  for (const [kind, long, short] of cases) {
    await t.test(kind, () => {
      assert.equal(issueLabel(kind), long);
      assert.equal(issueLabel(kind, { short: true }), short);
      assert.equal(brainStatusPill(kind).textContent, short);
    });
  }
  assert.equal(brainStatusPill('ok').textContent, 'Active');
});

test('runtimeSummary', async (t) => {
  installDom();
  const cases = [
    ['idle, never ran', {}, 'IdleNo runs yet'],
    ['working with a queue', { running: { preview: 'hi' }, queue: [{}, {}], last: { outcome: 'ok', finished: '' } }, 'Working2 queuedRepliednever'],
    ['the last run failed', { last: { outcome: 'error', error: 'exit 1' } }, 'IdleFailednever'],
  ];
  for (const [name, rt, want] of cases) {
    await t.test(name, () => assert.equal(h('div', runtimeSummary(rt)).textContent, want));
  }
});
