// Talking to the bridge's JSON API. Every call carries the session signIn
// got: its cookie, which the browser sends, and its binding, in the
// X-Bridge-Session header. Writes also carry X-Bridge-Dashboard, which a
// cross-site form cannot send.
//
// The page never holds the dashboard token: it trades a login code (from
// `fridolin open`) or a pasted token for a session. The cookie is one page
// scripts cannot read, but the browser sends it to every server on this
// host, whatever its port, a brain's site preview included. The binding
// is what such a server lacks: it lives in this origin's localStorage,
// which another port cannot read, and the bridge takes the cookie only
// with it. That holds only while the browser's profile on disk, where the
// binding is too, is one the sandbox denies to brains, or while no brain's
// server is opened in it (see dashsession.go for the list and for Firefox
// forks, which need no such server). Older versions kept the token in
// localStorage, where it stayed for good, readable from the browser's
// files; it goes now.

import { withDeadline } from '../lib/abort.js';
import { h } from '../lib/dom.js';

try { localStorage.removeItem('bridgeToken'); } catch { /* storage blocked */ }

// localStorage, so that every tab of this dashboard shares the session;
// read on every call, so a tab follows another one's sign-in. Without
// storage it lasts as long as the tab.
const BINDING_KEY = 'fridolin.session';
let bindingInMemory = '';

function sessionBinding() {
  try {
    const stored = localStorage.getItem(BINDING_KEY);
    if (stored !== null) return stored;
  } catch { /* storage blocked */ }
  return bindingInMemory;
}

function keepBinding(binding) {
  bindingInMemory = binding || '';
  try {
    if (bindingInMemory) localStorage.setItem(BINDING_KEY, bindingInMemory);
    else localStorage.removeItem(BINDING_KEY);
  } catch { /* storage blocked: kept in memory for this tab */ }
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
export async function api(method, path, body, { signal, timeout } = {}) {
  return (await request(method, path, body, { signal, timeout })).data;
}

/** What getIfChanged resolves with when the answer the caller has still holds. */
export const NOT_MODIFIED = Symbol('not modified');

/**
 * get for a poll: sends etag, the ETag of the answer the caller has, back
 * in If-None-Match. Resolves with NOT_MODIFIED when the bridge answers 304
 * (that answer still holds, and nothing is downloaded), else with {data,
 * etag}: the new answer and its ETag, '' when it came without one. Rejects
 * as api does.
 */
export async function getIfChanged(path, etag, { signal, timeout } = {}) {
  const { res, data } = await request('GET', path, undefined, { signal, timeout, etag });
  if (res.status === 304) return NOT_MODIFIED;
  return { data, etag: res.headers.get('ETag') || '' };
}

async function request(method, path, body, { signal, timeout = DEFAULT_TIMEOUT_MS, etag = '' } = {}) {
  const deadline = withDeadline(signal, timeout);
  const headers = { Accept: 'application/json' };
  const binding = sessionBinding();
  if (binding) headers['X-Bridge-Session'] = binding;
  if (etag) headers['If-None-Match'] = etag;
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
  if (!res.ok && !(etag && res.status === 304)) {
    const msg = (data && typeof data === 'object' && data.error) ? data.error
      : (typeof data === 'string' && data.trim()) ? data.trim()
      : `Request failed (${res.status} ${res.statusText})`;
    const err = new ApiError(msg, res.status, data);
    // A refused sign-in says nothing about the session the browser has.
    if (res.status === 401 && path !== LOGIN) listeners.unauthorized.forEach((fn) => fn(err));
    throw err;
  }
  return { res, data };
}

const LOGIN = '/api/login';

/**
 * Signs this browser in with { code } from a login link or { token } the
 * person pasted. Rejects with an ApiError (401 when the bridge refused it);
 * a refusal leaves a session the browser had as it was.
 */
export async function signIn(credential) {
  // Short: the app waits for it before it starts, and the boot watch in
  // theme.js reports a dashboard that has not started after ten seconds.
  const r = await api('POST', LOGIN, credential, { timeout: 8000 });
  keepBinding(r && r.binding);
  return r;
}

/** Ends this browser's session. */
export async function signOut() {
  const r = await api('POST', '/api/logout');
  keepBinding('');
  return r;
}

/**
 * Signs every browser out and replaces the dashboard token. This browser
 * gets a new session, so it stays signed in.
 */
export async function rotateToken() {
  const r = await api('POST', '/api/token/rotate');
  if (r && r.binding) keepBinding(r.binding);
  return r;
}

/**
 * Saves the file a GET of path answers with as filename, signed in as the
 * API's calls are (a plain link would not be). Rejects with an ApiError.
 */
export async function download(path, filename) {
  const headers = {};
  const binding = sessionBinding();
  if (binding) headers['X-Bridge-Session'] = binding;
  const res = await fetch(path, { headers, cache: 'no-store', credentials: 'same-origin' });
  if (!res.ok) throw new ApiError(`Download failed (${res.status} ${res.statusText})`, res.status, null);
  const url = URL.createObjectURL(await res.blob());
  h('a', { href: url, download: filename }).click();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
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
