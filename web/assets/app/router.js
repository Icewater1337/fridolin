// Matching the URL fragment (#/brains/work) against the route table. Pure,
// so unit-tested (web/test/router.test.js).

export const BRAND = 'Fridolin';

/** The browser tab title for a page. */
export function docTitle(title) {
  return title ? `${title} · ${BRAND}` : BRAND;
}

function decode(part) {
  try { return decodeURIComponent(part); } catch { return part; }
}

/**
 * Finds the route for hash. A route's path is its segments, where ':name'
 * captures one segment and ':name?' an optional one. An empty hash is the
 * first route. Returns {route, params} or null.
 */
export function matchRoute(routes, hash) {
  const parts = String(hash || '').replace(/^#\/?/, '').split('/').filter(Boolean).map(decode);
  if (!parts.length) return { route: routes[0], params: {} };
  for (const route of routes) {
    const segs = route.path.split('/');
    if (parts.length > segs.length) continue;
    const params = {};
    const ok = segs.every((seg, i) => {
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
 * What signs in from the fragment: { code } from a #login=… link, the way
 * `fridolin open` passes it, { token } from an older #token=… link, or
 * null. A malformed escape is kept as typed: the bridge then refuses it and
 * the connect screen says so, where throwing would stop the app from starting.
 */
export function credentialFromHash(hash) {
  const h = String(hash || '');
  if (h.startsWith('#login=')) return { code: decode(h.slice('#login='.length)) };
  if (h.startsWith('#token=')) return { token: decode(h.slice('#token='.length)) };
  return null;
}

/** The link to a route without its parameters, as the sidebar uses it. */
export function routeHref(route) {
  return `#/${route.path.split('/').filter((s) => !s.startsWith(':')).join('/')}`;
}

/** The sidebar entry a route belongs to: its own, or the one named by navId. */
export function activeNav(route) {
  if (!route) return '';
  return route.nav ? route.id : route.navId || '';
}
