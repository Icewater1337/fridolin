import { test } from 'node:test';
import assert from 'node:assert/strict';
import { errorMessage, errorName } from '../assets/lib/errors.js';
import { ApiError, errorStatus } from '../assets/app/api.js';

test('what a catch can tell of what it caught', () => {
  /** @type {[string, unknown, string, string, number][]} */
  const cases = [
    ['an Error', new Error('disk full'), 'disk full', 'Error', 0],
    ['an Error without a message', new Error(''), 'Error', 'Error', 0],
    ['an ApiError', new ApiError('Brain id taken', 409, null), 'Brain id taken', 'ApiError', 409],
    ['an abort', new DOMException('aborted', 'AbortError'), 'aborted', 'AbortError', 0],
    ['a string', 'nope', 'nope', '', 0],
    ['an object with a status', { status: 404, message: 'gone' }, '[object Object]', '', 0],
    ['undefined', undefined, 'undefined', '', 0],
    ['null', null, 'null', '', 0],
  ];
  for (const [name, e, message, ename, status] of cases) {
    assert.equal(errorMessage(e), message, name);
    assert.equal(errorName(e), ename, name);
    assert.equal(errorStatus(e), status, name);
  }
});
