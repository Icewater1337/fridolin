import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseTs, relTime, hhmmss, fmtDuration, fmtBytes, plural, prettyJSON } from '../assets/lib/format.js';

test('parseTs', async (t) => {
  const cases = [
    ['empty string', '', null],
    ['zero', 0, null],
    ['Go zero time', '0001-01-01T00:00:00Z', null],
    ['unparseable', 'soon', null],
    ['1970', '1970-06-01T00:00:00Z', null],
    ['ISO string', '2026-10-17T12:00:00Z', Date.parse('2026-10-17T12:00:00Z')],
    ['milliseconds', Date.parse('2026-10-17T12:00:00Z'), Date.parse('2026-10-17T12:00:00Z')],
  ];
  for (const [name, input, want] of cases) {
    await t.test(name, () => {
      const got = parseTs(input);
      assert.equal(got === null ? null : got.getTime(), want);
    });
  }
  await t.test('a Date passes through', () => {
    const d = new Date('2026-10-17T12:00:00Z');
    assert.equal(parseTs(d), d);
  });
});

test('relTime', async (t) => {
  const now = Date.parse('2026-10-17T12:00:00Z');
  const ago = (s) => new Date(now - s * 1000).toISOString();
  const cases = [
    ['never', '', 'never'],
    ['10 s ago', ago(10), 'just now'],
    ['3 s ahead counts as now', ago(-3), 'just now'],
    ['20 s ahead', ago(-20), 'in a few seconds'],
    ['60 s', ago(60), '1 min ago'],
    ['10 min', ago(600), '10 min ago'],
    ['in 10 min', ago(-600), 'in 10 min'],
    ['70 min', ago(70 * 60), '1 hour ago'],
    ['5 hours', ago(5 * 3600), '5 hours ago'],
    ['30 hours', ago(30 * 3600), 'yesterday'],
    ['in 30 hours', ago(-30 * 3600), 'tomorrow'],
    ['4 days', ago(4 * 86400), '4 days ago'],
    ['in 4 days', ago(-4 * 86400), 'in 4 days'],
  ];
  for (const [name, ts, want] of cases) {
    await t.test(name, () => assert.equal(relTime(ts, now), want));
  }
  await t.test('beyond 30 days shows a date', () => {
    assert.doesNotMatch(relTime(ago(60 * 86400), now), /ago|in /);
  });
});

test('hhmmss', async (t) => {
  await t.test('local time, zero-padded', () => assert.equal(hhmmss(new Date(2026, 0, 2, 3, 4, 5).toISOString()), '03:04:05'));
  await t.test('unparseable', () => assert.equal(hhmmss('later'), ''));
});

test('fmtDuration', async (t) => {
  const cases = [
    [null, ''],
    [undefined, ''],
    [NaN, ''],
    [0.25, '250 ms'],
    [5, '5 s'],
    [60, '1 min'],
    [65, '1 min 5 s'],
    [3700, '1 h 1 min'],
    [90000, '1 d 1 h'],
  ];
  for (const [input, want] of cases) {
    await t.test(String(input), () => assert.equal(fmtDuration(input), want));
  }
});

test('fmtBytes', async (t) => {
  const cases = [
    [0, '0 B'],
    [999, '999 B'],
    [1000, '1.0 KB'],
    [123456, '123 KB'],
    [1500000000, '1.5 GB'],
    [2e15, '2000 TB'],
  ];
  for (const [input, want] of cases) {
    await t.test(String(input), () => assert.equal(fmtBytes(input), want));
  }
});

test('plural', async (t) => {
  const cases = [
    [[1, 'brain'], '1 brain'],
    [[0, 'brain'], '0 brains'],
    [[2, 'brain'], '2 brains'],
    [[2, 'person', 'people'], '2 people'],
  ];
  for (const [args, want] of cases) {
    await t.test(want, () => assert.equal(plural(...args), want));
  }
});

test('prettyJSON indents by two spaces', () => {
  assert.equal(prettyJSON({ a: [1] }), '{\n  "a": [\n    1\n  ]\n}');
});
