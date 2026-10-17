import { test } from 'node:test';
import assert from 'node:assert/strict';
import { BRAIN_ID_RE, slugify, routingSummary } from '../assets/lib/brain.js';
import { changed } from '../assets/lib/memo.js';

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

test('changed remembers the last value per key', () => {
  const memo = {};
  assert.equal(changed(memo, 'a', { x: 1 }), true);
  assert.equal(changed(memo, 'a', { x: 1 }), false);
  assert.equal(changed(memo, 'b', { x: 1 }), true);
  assert.equal(changed(memo, 'a', { x: 2 }), true);
});
