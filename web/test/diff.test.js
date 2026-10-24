import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sortKeys, fieldDiff, lineDiff } from '../assets/lib/diff.js';

test('sortKeys sorts nested object keys and keeps array order', () => {
  const got = sortKeys({ b: 1, a: { d: [3, { z: 1, y: 2 }], c: null } });
  assert.equal(JSON.stringify(got), '{"a":{"c":null,"d":[3,{"y":2,"z":1}]},"b":1}');
});

test('fieldDiff', async (t) => {
  /** @type {[string, object, object, {path: string, a: string, b: string}[]][]} */
  const cases = [
    ['equal', { name: 'A' }, { name: 'A' }, []],
    ['changed value', { name: 'A' }, { name: 'B' }, [{ path: 'name', a: '"A"', b: '"B"' }]],
    ['nested path', { routing: { default: false } }, { routing: { default: true } }, [{ path: 'routing.default', a: 'false', b: 'true' }]],
    ['added key', {}, { model: 'opus' }, [{ path: 'model', a: '(not set)', b: '"opus"' }]],
    ['unset and empty string are equal', {}, { model: '' }, []],
    ['unset and empty list are equal', { routing: {} }, { routing: { groups: [] } }, []],
    ['null and unset are equal', { reply_chat: null }, {}, []],
    ['list items one per line', { tools: ['A'] }, { tools: ['A', 'B'] }, [{ path: 'tools', a: '"A"', b: '"A"\n"B"' }]],
    ['rows sorted by path', { b: 1, a: 1 }, { b: 2, a: 2 }, [{ path: 'a', a: '1', b: '2' }, { path: 'b', a: '1', b: '2' }]],
  ];
  for (const [name, a, b, want] of cases) {
    await t.test(name, () => assert.deepEqual(fieldDiff(a, b), want));
  }
});

test('lineDiff', async (t) => {
  const marks = (side) => side.map((l) => (l.changed ? `*${l.text}` : l.text));
  /** @type {[string, string, string, string[], string[]][]} */
  const cases = [
    ['same', 'a\nb', 'a\nb', ['a', 'b'], ['a', 'b']],
    ['changed middle line', 'a\nb\nc', 'a\nx\nc', ['a', '*b', 'c'], ['a', '*x', 'c']],
    ['added line', 'a\nc', 'a\nb\nc', ['a', 'c'], ['a', '*b', 'c']],
    ['removed line', 'a\nb\nc', 'a\nc', ['a', '*b', 'c'], ['a', 'c']],
  ];
  for (const [name, a, b, left, right] of cases) {
    await t.test(name, () => {
      const d = lineDiff(a, b);
      assert.deepEqual(marks(d.left), left);
      assert.deepEqual(marks(d.right), right);
    });
  }
});
