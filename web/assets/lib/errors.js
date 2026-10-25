// What a catch can tell of what it caught. A caught value is unknown: an
// Error (an ApiError, a DOMException from fetch or an abort), or whatever
// else was thrown, so these narrow it before reading it. Pure functions,
// unit-tested in web/test/errors.test.js.

/**
 * The message of what was thrown: an Error's, else the value as text.
 * @param {unknown} e
 * @returns {string}
 */
export function errorMessage(e) {
  return (e instanceof Error && e.message) || String(e);
}

/**
 * The name of what was thrown, such as 'AbortError' or 'TimeoutError';
 * '' for a value that is no Error.
 * @param {unknown} e
 * @returns {string}
 */
export function errorName(e) {
  return e instanceof Error ? e.name : '';
}
