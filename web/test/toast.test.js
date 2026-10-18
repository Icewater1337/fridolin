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

test('errors are announced as alerts, the rest politely', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const err = toast('Could not save', 'error');
  const ok = toast('Saved the brain', 'ok');
  assert.equal(err.parentNode.getAttribute('role'), 'alert');
  assert.equal(ok.parentNode.getAttribute('role'), 'status');
  assert.equal(ok.parentNode.getAttribute('aria-live'), 'polite');
  assert.equal(err.parentNode.parentNode, ok.parentNode.parentNode, 'both in the same corner');
  t.mock.timers.tick(10000);
});

test('a toast under the pointer or with the focus stays until they leave', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const fire = (el, type) => el.dispatchEvent(new Event(type));
  const cases = [['pointer', 'mouseenter', 'mouseleave'], ['focus', 'focusin', 'focusout']];
  for (const [name, enter, leave] of cases) {
    await t.test(name, () => {
      const el = toast(`Held by the ${name}`, 'error');
      t.mock.timers.tick(6000);
      fire(el, enter);
      t.mock.timers.tick(60000);
      assert.equal(shown(`Held by the ${name}`).length, 1, 'held past its 9 s');
      fire(el, leave);
      t.mock.timers.tick(2900);
      assert.equal(shown(`Held by the ${name}`).length, 1, 'then gets the 3 s it had left');
      t.mock.timers.tick(200);
      assert.equal(shown(`Held by the ${name}`).length, 0);
    });
  }
  const late = toast('Held at the end', 'ok');
  t.mock.timers.tick(4900);
  fire(late, 'mouseenter');
  fire(late, 'mouseleave');
  t.mock.timers.tick(1400);
  assert.equal(shown('Held at the end').length, 1, 'at least 1.5 s to move away');
  t.mock.timers.tick(200);
  assert.equal(shown('Held at the end').length, 0);
});
