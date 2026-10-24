// Addresses of the Files and Wiki pages (docs/FILES.md). Pure.

const enc = (/** @type {string} */ p) => p.split('/').map(encodeURIComponent).join('/');

/**
 * The dashboard address of a folder (dir) or a file of a root.
 * @param {string} root
 * @param {string} path
 * @param {boolean} [dir]
 */
export function filesHref(root, path, dir) {
  return `#/${dir ? 'files' : 'file'}/${encodeURIComponent(root)}${path ? `/${enc(path)}` : ''}`;
}

/**
 * The dashboard address of a wiki page (its path in the brain folder), or of a brain's wiki.
 * @param {string} brain
 * @param {string} [path]
 */
export function wikiHref(brain, path) {
  return `#/wiki/${encodeURIComponent(brain)}${path ? `/${enc(path)}` : ''}`;
}

/**
 * The address a file of a root loads from, for an <img>, <audio>, <video>
 * or a download: /files/raw with the root's ticket (files_ticket.go).
 * @param {string} root
 * @param {string} path
 * @param {string} ticket
 * @param {boolean} [download]
 */
export function rawHref(root, path, ticket, download) {
  const p = new URLSearchParams({ root, path, ticket });
  if (download) p.set('download', '1');
  return `/files/raw?${p}`;
}

/**
 * Where a brain's file is on the site of domain, for whoever's role sees the brain.
 * @param {string} domain
 * @param {string} brain
 * @param {string} path
 */
export function siteFileHref(domain, brain, path) {
  return `https://${domain}/files/${encodeURIComponent(brain)}/${enc(path)}`;
}

/**
 * A WhatsApp chat with number (digits), its message box holding text.
 * @param {string} number
 * @param {string} text
 */
export function whatsappHref(number, text) {
  return `https://wa.me/${String(number).replace(/\D/g, '')}?text=${encodeURIComponent(text)}`;
}

/**
 * The parent folder of path ('' at the top).
 * @param {string} path
 */
export function parentOf(path) {
  const i = path.lastIndexOf('/');
  return i < 0 ? '' : path.slice(0, i);
}
