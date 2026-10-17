// Talking to the bridge's JSON API. Every call carries the dashboard token;
// writes also carry X-Bridge-Dashboard, which a cross-site form cannot send.

import { withDeadline } from '../lib/abort.js';

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

// How long a request may take. A handler that never answers would otherwise
// stall its poll for good, and since the connection stays up the offline
// banner would never show. The slower calls pass a longer timeout: rclone
// listing backup versions, Plaud's account check and logout, and the Site
// page's git, gh and npm calls (connecting the repository, checking it for
// changes, the starter files, a new GitHub repository, the deploy key,
// making a brain a builder, its preview, merging its proposal).
export const DEFAULT_TIMEOUT_MS = 30000;

/**
 * Calls the API. Resolves with the parsed JSON (or null for an empty body),
 * rejects with an ApiError carrying the HTTP status (0 = network failure or
 * no answer within timeout ms; 0 waits for ever, for a dialog a person
 * answers), or with what the caller's signal aborted with (an AbortError
 * unless the caller gave a reason).
 */
export async function api(method, path, body, { signal, timeout = DEFAULT_TIMEOUT_MS } = {}) {
  const deadline = withDeadline(signal, timeout);
  const headers = { Accept: 'application/json' };
  if (token) headers['X-Bridge-Token'] = token;
  const init = { method, headers, signal: deadline.signal, cache: 'no-store', credentials: 'same-origin' };
  if (method !== 'GET' && method !== 'HEAD') {
    headers['X-Bridge-Dashboard'] = '1';
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
  }
  let res;
  let text;
  try {
    res = await fetch(path, init);
    setReachable(true);
    text = await res.text();
  } catch (e) {
    if (deadline.expired) {
      setReachable(false);
      throw new ApiError(`The bridge did not answer within ${Math.round(timeout / 1000)} s. Is it stuck?`, 0, null);
    }
    // The caller gave up, maybe with a reason of its own: not the bridge's fault.
    if ((signal && signal.aborted) || (e && e.name === 'AbortError')) throw e;
    setReachable(false);
    throw new ApiError('Cannot reach the bridge. Is it still running?', 0, null);
  } finally {
    deadline.clear();
  }
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
