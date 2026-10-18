// A stand-in for the bridge's API in page tests: fetch answers at once with
// what route(path, init) returns for the path (a body, or {status, body}
// for another status or a body that has a status field of its own), so a
// test sets up what the bridge holds and lets the page poll it.

export function serve(t, route) {
  const real = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    if (init.signal && init.signal.aborted) throw new DOMException('aborted', 'AbortError');
    const path = String(url);
    calls.push({ path, method: init.method || 'GET', body: init.body ? JSON.parse(init.body) : undefined });
    let r = route(path, init);
    if (!r || typeof r !== 'object' || !('status' in r)) r = { status: 200, body: r };
    const text = r.body === undefined ? '' : JSON.stringify(r.body);
    return { ok: r.status >= 200 && r.status < 300, status: r.status, statusText: '', text: async () => text };
  };
  t.after(() => { globalThis.fetch = real; });
  return calls;
}

/** Lets the requests a page started come back and their answers paint. */
export async function settle(rounds = 10) {
  for (let i = 0; i < rounds; i++) await new Promise((r) => setImmediate(r));
}

/** Sets an input's value as typing does, firing the input event. */
export function type(input, text) {
  input.value = text;
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

/**
 * Fails the test when the page logs an error: a store or resource
 * subscriber that throws is caught and only logged, so a page could break
 * without anything else failing.
 */
export function noConsoleErrors(t) {
  const real = console.error;
  const seen = [];
  console.error = (...args) => seen.push(args.map(String).join(' '));
  t.after(() => {
    console.error = real;
    if (seen.length) throw new Error(`console.error: ${seen.join('; ')}`);
  });
}
