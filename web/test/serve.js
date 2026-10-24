// A stand-in for the bridge's API in page tests: fetch answers at once with
// what route(path, init) returns for the path (a body, or {status, body,
// headers} for another status, response headers, or a body that has a
// status field of its own), so a test sets up what the bridge holds and
// lets the page poll it. A route that returns a promise answers once it
// resolves, e.g. to answer a poll late with what the bridge held when it
// was asked; until then, the request fails once its signal aborts, as
// fetch does.

/** @import { TestContext } from 'node:test' */

/**
 * A request the page made: its body parsed from JSON (or as it was sent,
 * a FormData), its headers as the page passed them.
 * @typedef {{path: string, method: string, headers: Record<string, string>, body: any}} Call
 */

/**
 * What a route answers: a body (any JSON), or {status, body, headers};
 * or a promise of either.
 * @typedef {any} Answer
 */

/**
 * The request as a route sees it. body is the JSON the page sent (a
 * FormData for an upload, which no route reads as text).
 * @typedef {{method?: string, headers?: Record<string, string>, body?: string, signal?: AbortSignal | null}} RouteInit
 */

/**
 * @param {TestContext} t
 * @param {(path: string, init: RouteInit) => Answer} route
 * @returns {Call[]}
 */
export function serve(t, route) {
  const real = globalThis.fetch;
  /** @type {Call[]} */
  const calls = [];
  const aborted = () => new DOMException('aborted', 'AbortError');
  // Its answers have only what api.js reads of a Response.
  /** @type {(url: string | URL, init?: RequestInit) => Promise<object>} */
  const fetch = async (url, init = {}) => {
    if (init.signal && init.signal.aborted) throw aborted();
    const path = String(url);
    calls.push({ path, method: init.method || 'GET', headers: /** @type {Record<string, string>} */ (init.headers || {}), body: typeof init.body === 'string' ? JSON.parse(init.body) : init.body });
    let r = route(path, /** @type {RouteInit} */ (init));
    if (r && typeof r.then === 'function') {
      const { signal } = init;
      r = await (signal ? Promise.race([r, new Promise((_, reject) => signal.addEventListener('abort', () => reject(aborted()), { once: true }))]) : r);
    }
    if (!r || typeof r !== 'object' || !('status' in r)) r = { status: 200, body: r };
    const text = r.body === undefined ? '' : JSON.stringify(r.body);
    return { ok: r.status >= 200 && r.status < 300, status: r.status, statusText: '', headers: new Headers(r.headers), text: async () => text };
  };
  Object.assign(globalThis, { fetch });
  t.after(() => { globalThis.fetch = real; });
  return calls;
}

/**
 * Lets the requests a page started come back and their answers paint.
 * @param {number} [rounds]
 */
export async function settle(rounds = 10) {
  for (let i = 0; i < rounds; i++) await new Promise((r) => setImmediate(r));
}

/**
 * Sets an input's value as typing does, firing the input event.
 * @param {HTMLInputElement | HTMLTextAreaElement} input
 * @param {string} text
 */
export function type(input, text) {
  input.value = text;
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

/**
 * Fails the test when the page logs an error: a store or resource
 * subscriber that throws is caught and only logged, so a page could break
 * without anything else failing.
 * @param {TestContext} t
 */
export function noConsoleErrors(t) {
  const real = console.error;
  /** @type {string[]} */
  const seen = [];
  console.error = (...args) => seen.push(args.map(String).join(' '));
  t.after(() => {
    console.error = real;
    if (seen.length) throw new Error(`console.error: ${seen.join('; ')}`);
  });
}
