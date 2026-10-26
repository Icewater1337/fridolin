import { test } from 'node:test';
import assert from 'node:assert/strict';
import { budgetState, callerOf, monthName, spendText, usedText } from '../assets/lib/connections.js';

test('monthName', async (t) => {
  /** @type {[string | undefined, string][]} */
  const cases = [
    ['2026-10', 'October 2026'],
    ['2027-01', 'January 2027'],
    ['2026-13', '2026-13'],
    ['', 'this month'],
    [undefined, 'this month'],
  ];
  for (const [input, want] of cases) {
    await t.test(String(input), () => assert.equal(monthName(input), want));
  }
});

test('usedText names what each provider bills by', async (t) => {
  const none = { audio_seconds: 0, input_tokens: 0, output_tokens: 0, characters: 0 };
  /** @type {[string, typeof none, string][]} */
  const cases = [
    ['nothing', none, ''],
    ['audio', { ...none, audio_seconds: 340.5 }, '5 min 41 s of audio'],
    ['tokens', { ...none, input_tokens: 1200, output_tokens: 800 }, '1,200 tokens in, 800 out'],
    ['characters', { ...none, characters: 3400 }, '3,400 characters'],
    ['one character', { ...none, characters: 1 }, '1 character'],
    ['all', { audio_seconds: 30, input_tokens: 10, output_tokens: 0, characters: 5 }, '30 s of audio · 10 tokens in, 0 out · 5 characters'],
  ];
  for (const [name, input, want] of cases) {
    await t.test(name, () => assert.equal(usedText(input), want));
  }
});

test('spendText and budgetState', () => {
  assert.equal(spendText(0.05, 20), '$0.05 of $20');
  assert.equal(spendText(0.05, 0), '$0.05');
  assert.equal(spendText(0, 7.5), '$0 of $7.50');
  assert.equal(budgetState(0.05, 20), '');
  assert.equal(budgetState(16, 20), 'near');
  assert.equal(budgetState(20, 20), 'over');
  assert.equal(budgetState(500, 0), '', 'no limit, nothing to be over');
});

test('callerOf', () => {
  const brains = [{ id: 'personal', name: 'Personal' }];
  assert.deepEqual(callerOf('module:hands-study', brains), { kind: 'module', id: 'hands-study', name: 'hands-study' });
  assert.deepEqual(callerOf('brain:personal', brains), { kind: 'brain', id: 'personal', name: 'Personal (personal)' });
  assert.deepEqual(callerOf('brain:gone', brains), { kind: 'brain', id: 'gone', name: 'gone' });
  assert.deepEqual(callerOf('something'), { kind: '', id: 'something', name: 'something' });
});
