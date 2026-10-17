import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { toast, toastError } from '../assets/components/toast.js';

installDom();
const shown = (msg) => document.querySelectorAll('.toast').filter((el) => el.textContent.includes(msg) && !el.classList.contains('leaving'));

test('the same message shows once at a time', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const a = toast('Saved', 'ok');
  assert.equal(toast('Saved', 'ok'), a);
  assert.equal(shown('Saved').length, 1);
  t.mock.timers.tick(5200);
  assert.equal(shown('Saved').length, 0, 'it goes after its timeout');
  assert.notEqual(toast('Saved', 'ok'), a, 'and can show again');
  t.mock.timers.tick(5200);
});

test('a closed toast cannot take the place of a newer one with its message', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const a = toast('Cannot save', 'error');
  a.querySelector('button').click();
  t.mock.timers.tick(250);
  const b = toast('Cannot save', 'error');
  assert.notEqual(b, a);
  t.mock.timers.tick(8850); // a's own timeout (at 9 s) passes; b's (at 9.25 s) not yet
  assert.equal(toast('Cannot save', 'error'), b, 'b still stands for the message');
  assert.equal(shown('Cannot save').length, 1);
  t.mock.timers.tick(2000);
  assert.equal(shown('Cannot save').length, 0);
});

test('toastError stays quiet about a 401 and an abort', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  assert.equal(toastError(Object.assign(new Error('missing token'), { status: 401 })), null);
  assert.equal(toastError(Object.assign(new Error('aborted'), { name: 'AbortError' })), null);
  assert.ok(toastError(new Error('Brain id taken')));
  assert.equal(shown('Brain id taken').length, 1);
  t.mock.timers.tick(10000);
});
