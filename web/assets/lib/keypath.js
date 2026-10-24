// Dotted keys such as queue.mode, which name a setting inside the nested
// objects of config.json. Pure, so unit-tested (web/test/keypath.test.js).

/**
 * The value at key in obj, or undefined when a part of the way is missing.
 * @param {unknown} obj
 * @param {string} key
 * @returns {unknown}
 */
export function getPath(obj, key) {
  return key.split('.').reduce((o, k) => (o && typeof o === 'object' ? /** @type {Record<string, unknown>} */ (o)[k] : undefined), obj);
}

/**
 * Sets the value at key in obj, making the objects on the way that are
 * missing (or are not objects); undefined deletes the key. The keys beside
 * it stay as they are.
 * @param {Record<string, any>} obj
 * @param {string} key
 * @param {unknown} value
 */
export function setPath(obj, key, value) {
  const parts = key.split('.');
  let o = obj;
  for (const p of parts.slice(0, -1)) {
    if (!o[p] || typeof o[p] !== 'object') o[p] = {};
    o = o[p];
  }
  if (value === undefined) delete o[parts[parts.length - 1]];
  else o[parts[parts.length - 1]] = value;
}
