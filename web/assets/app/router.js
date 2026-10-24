// Matching the URL fragment (#/brains/work) against the route table. Pure,
// so unit-tested (web/test/router.test.js).

/** @import { PageModule } from './page.js' */

export const BRAND = 'Fridolin';

/**
 * A route of the table in routes.js: id names it, path is matched as
 * matchRoute says, nav is its sidebar entry (none for a page that belongs
 * to another's, named by navId), load imports its page.
 * @typedef {object} Route
 * @property {string} id
 * @property {string} path
 * @property {{label: string, icon: string, devOnly?: boolean}} [nav]
 * @property {string} [navId]
 * @property {() => Promise<{default: PageModule}>} load
 */

/**
 * The browser tab title for a page.
 * @param {string} [title]
 */
export function docTitle(title) {
  return title ? `${title} · ${BRAND}` : BRAND;
}

/** @param {string} part */
function decode(part) {
  try { return decodeURIComponent(part); } catch { return part; }
}

/**
 * Finds the route for hash. A route's path is its segments, where ':name'
 * captures one segment, ':name?' an optional one and ':name*', last, the
 * rest of them, joined by '/' ('' for none). An empty hash is the first
 * route. Returns {route, params} or null.
 * @template {Pick<Route, 'path'>} R
 * @param {readonly R[]} routes
 * @param {string | null | undefined} hash
 * @returns {{route: R, params: Record<string, string>} | null}
 */
export function matchRoute(routes, hash) {
  const parts = String(hash || '').replace(/^#\/?/, '').split('/').filter(Boolean).map(decode);
  if (!parts.length) return { route: routes[0], params: {} };
  for (const route of routes) {
    const segs = route.path.split('/');
    const rest = segs[segs.length - 1].endsWith('*');
    if (parts.length > segs.length && !rest) continue;
    /** @type {Record<string, string>} */
    const params = {};
    const ok = segs.every((seg, i) => {
      if (seg.endsWith('*')) {
        params[seg.slice(1, -1)] = parts.slice(i).join('/');
        return true;
      }
      if (!seg.startsWith(':')) return parts[i] === seg;
      const optional = seg.endsWith('?');
      if (i < parts.length) params[seg.slice(1, optional ? -1 : undefined)] = parts[i];
      return i < parts.length || optional;
    });
    if (ok) return { route, params };
  }
  return null;
}

/**
 * What signs a browser in: a login code (from a link `fridolin open` or
 * !dashboard made) or the dashboard token.
 * @typedef {{code: string, token?: undefined} | {token: string, code?: undefined}} Credential
 */

/**
 * What signs in from the fragment: { code } from a #login=… link, the way
 * `fridolin open` passes it, { token } from an older #token=… link, or
 * null. A malformed escape is kept as typed: the bridge then refuses it and
 * the connect screen says so, where throwing would stop the app from starting.
 * @param {string | null | undefined} hash
 * @returns {Credential | null}
 */
export function credentialFromHash(hash) {
  const h = String(hash || '');
  if (h.startsWith('#login=')) return { code: decode(h.slice('#login='.length)) };
  if (h.startsWith('#token=')) return { token: decode(h.slice('#token='.length)) };
  return null;
}

/**
 * The link to a route without its parameters, as the sidebar uses it.
 * @param {Pick<Route, 'path'>} route
 */
export function routeHref(route) {
  return `#/${route.path.split('/').filter((s) => !s.startsWith(':')).join('/')}`;
}

/**
 * The sidebar entry a route belongs to: its own, or the one named by navId.
 * @param {Pick<Route, 'id' | 'nav' | 'navId'> | null | undefined} route
 */
export function activeNav(route) {
  if (!route) return '';
  return route.nav ? route.id : route.navId || '';
}
