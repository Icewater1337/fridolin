import { test } from 'node:test';
import assert from 'node:assert/strict';
import { getPath, setPath } from '../assets/lib/keypath.js';

test('getPath', async (t) => {
  const cfg = { claude_path: 'claude', queue: { mode: 'interrupt', notify: false }, sandbox: { enabled: true, deny_paths: ['~/clients'] }, log_level: null };
  const cases = [
    ['a top-level key', 'claude_path', 'claude'],
    ['a nested key', 'queue.mode', 'interrupt'],
    ['a nested false', 'queue.notify', false],
    ['a nested list', 'sandbox.deny_paths', ['~/clients']],
    ['a missing nested key', 'queue.max_pending', undefined],
    ['a missing object on the way', 'debounce.quiet_ms', undefined],
    ['through a value that is no object', 'claude_path.x', undefined],
    ['through null', 'log_level.x', undefined],
  ];
  for (const [name, key, want] of cases) {
    await t.test(name, () => assert.deepEqual(getPath(cfg, key), want));
  }
});

test('setPath', async (t) => {
  const cases = [
    {
      name: 'a top-level key',
      from: { claude_path: 'claude' }, key: 'brain_model', value: 'opus',
      want: { claude_path: 'claude', brain_model: 'opus' },
    },
    {
      name: 'a nested key keeps its siblings',
      from: { queue: { mode: 'queue', notify: true } }, key: 'queue.mode', value: 'interrupt',
      want: { queue: { mode: 'interrupt', notify: true } },
    },
    {
      name: 'a nested key creates the object on the way',
      from: { claude_path: 'claude' }, key: 'sandbox.enabled', value: true,
      want: { claude_path: 'claude', sandbox: { enabled: true } },
    },
    {
      name: 'every missing object on the way',
      from: {}, key: 'a.b.c', value: 1,
      want: { a: { b: { c: 1 } } },
    },
    {
      name: 'a value that is no object on the way becomes one',
      from: { queue: 'queue' }, key: 'queue.mode', value: 'interrupt',
      want: { queue: { mode: 'interrupt' } },
    },
    {
      name: 'null on the way becomes an object',
      from: { sandbox: null }, key: 'sandbox.enabled', value: false,
      want: { sandbox: { enabled: false } },
    },
    {
      name: 'undefined deletes the key and keeps its siblings',
      from: { debounce: { quiet_ms: 2000, max_wait_ms: 10000 } }, key: 'debounce.quiet_ms', value: undefined,
      want: { debounce: { max_wait_ms: 10000 } },
    },
    {
      name: 'false and zero are values, not deletions',
      from: { queue: { notify: true, max_pending: 10 } }, key: 'queue.notify', value: false,
      want: { queue: { notify: false, max_pending: 10 } },
    },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      const obj = structuredClone(c.from);
      setPath(obj, c.key, c.value);
      assert.deepEqual(obj, c.want);
    });
  }
});
