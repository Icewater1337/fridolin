// Talking to the bridge's JSON API. Every call carries the dashboard token;
// writes also carry X-Bridge-Dashboard, which a cross-site form cannot send.

const TOKEN_KEY = 'bridgeToken';
let token = '';
try { token = localStorage.getItem(TOKEN_KEY) || ''; } catch { /* storage blocked */ }

export function getToken() { return token; }

export function setToken(t) {
  token = (t || '').trim();
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch { /* storage blocked: keep it in memory for this tab */ }
}

export class ApiError extends Error {
  constructor(message, status, data) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

const listeners = { unauthorized: new Set(), reachable: new Set() };

/** Subscribe to 'unauthorized' (401) or 'reachable' (bool) events. */
export function on(event, fn) {
  listeners[event].add(fn);
  return () => listeners[event].delete(fn);
}

let lastReachable = true;
function setReachable(ok) {
  if (ok === lastReachable) return;
  lastReachable = ok;
  listeners.reachable.forEach((fn) => fn(ok));
}

/**
 * Calls the API. Resolves with the parsed JSON (or null for an empty body),
 * rejects with an ApiError carrying the HTTP status (0 = network failure).
 */
export async function api(method, path, body, { signal } = {}) {
  const headers = { Accept: 'application/json' };
  if (token) headers['X-Bridge-Token'] = token;
  const init = { method, headers, signal, cache: 'no-store', credentials: 'same-origin' };
  if (method !== 'GET' && method !== 'HEAD') {
    headers['X-Bridge-Dashboard'] = '1';
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
  }
  let res;
  try {
    res = await fetch(path, init);
  } catch (e) {
    if (e && e.name === 'AbortError') throw e;
    setReachable(false);
    throw new ApiError('Cannot reach the bridge. Is it still running?', 0, null);
  }
  setReachable(true);
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = text; }
  }
  if (!res.ok) {
    const msg = (data && typeof data === 'object' && data.error) ? data.error
      : (typeof data === 'string' && data.trim()) ? data.trim()
      : `Request failed (${res.status} ${res.statusText})`;
    const err = new ApiError(msg, res.status, data);
    if (res.status === 401) listeners.unauthorized.forEach((fn) => fn(err));
    throw err;
  }
  return data;
}

export const get = (path, opts) => api('GET', path, undefined, opts);
export const post = (path, body, opts) => api('POST', path, body, opts);
export const put = (path, body, opts) => api('PUT', path, body, opts);
export const del = (path, opts) => api('DELETE', path, undefined, opts);

/** Builds a query string from an object, skipping empty values. */
export function qs(params) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : '';
}
