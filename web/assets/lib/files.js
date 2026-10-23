// Addresses of the Files and Wiki pages (docs/FILES.md). Pure.

const enc = (p) => p.split('/').map(encodeURIComponent).join('/');

/** The dashboard address of a folder (dir) or a file of a root. */
export function filesHref(root, path, dir) {
  return `#/${dir ? 'files' : 'file'}/${encodeURIComponent(root)}${path ? `/${enc(path)}` : ''}`;
}

/** The dashboard address of a wiki page (its path in the brain folder), or of a brain's wiki. */
export function wikiHref(brain, path) {
  return `#/wiki/${encodeURIComponent(brain)}${path ? `/${enc(path)}` : ''}`;
}

/**
 * The address a file of a root loads from, for an <img>, <audio>, <video>
 * or a download: /files/raw with the root's ticket (files_ticket.go).
 */
export function rawHref(root, path, ticket, download) {
  const p = new URLSearchParams({ root, path, ticket });
  if (download) p.set('download', '1');
  return `/files/raw?${p}`;
}
