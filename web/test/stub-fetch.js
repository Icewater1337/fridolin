// A stand-in for fetch: records each request and answers it when the test
// says so, or rejects it the way fetch does once its signal aborts.

export function stubFetch(t) {
  const real = globalThis.fetch;
  const calls = [];
  globalThis.fetch = (url, init = {}) => new Promise((resolve, reject) => {
    const call = { url, init, signal: init.signal, resolve, reject };
    calls.push(call);
    const abort = () => reject(init.signal.reason instanceof Error ? init.signal.reason : new DOMException('aborted', 'AbortError'));
    if (init.signal) {
      if (init.signal.aborted) abort();
      else init.signal.addEventListener('abort', abort, { once: true });
    }
  });
  t.after(() => { globalThis.fetch = real; });
  return {
    calls,
    /** Answers request i with body as JSON (status 200 unless given) and the response headers given. */
    answer(i, body, status = 200, headers = {}) {
      const text = body === undefined ? '' : JSON.stringify(body);
      calls[i].resolve({ ok: status >= 200 && status < 300, status, statusText: '', headers: new Headers(headers), text: async () => text });
    },
  };
}
