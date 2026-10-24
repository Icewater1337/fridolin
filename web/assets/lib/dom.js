// The element builder every view uses. It never parses HTML: strings always
// become text nodes, so untrusted text is safe wherever it ends up.

/**
 * What h() and append() take as children: a node, text (a string or a
 * number, which becomes a text node), nothing (null, undefined or a
 * boolean, which is left out, so `cond && h(...)` works) or an array of
 * these. Any other object would show as "[object Object]".
 * @typedef {Node | string | number | bigint | boolean | null | undefined | readonly Child[]} Child
 */

/**
 * h()'s attributes. class adds to the classes the tag names, text sets the
 * text, style and dataset are copied onto el.style and el.dataset (never a
 * style attribute, which the CSP refuses), an on* function becomes an event
 * listener, the PROPS set the element's property; anything else becomes an
 * attribute (true as an empty one; null, undefined and false leave it out).
 * @typedef {{
 *   class?: string | null | false,
 *   text?: string | number | null,
 *   style?: Partial<CSSStyleDeclaration>,
 *   dataset?: Record<string, string>,
 *   [name: string]: unknown,
 * }} Attrs
 */

/**
 * The tag name in h()'s first argument: 'input.mono' and 'div#id.wide'
 * are an input and a div, '.card' a div.
 * @template {string} S
 * @typedef {S extends `${infer N}.${string}` ? TagName<N> : S extends `${infer N}#${string}` ? (N extends '' ? 'div' : N) : S extends '' ? 'div' : S} TagName
 */

/**
 * The element h(tag) makes: an HTMLInputElement for 'input.mono', an
 * HTMLElement for a tag only known at run time.
 * @template {string} S
 * @typedef {TagName<S> extends keyof HTMLElementTagNameMap ? HTMLElementTagNameMap[TagName<S>] : HTMLElement} ElementFor
 */

const PROPS = new Set(['value', 'checked', 'disabled', 'selected', 'hidden', 'indeterminate', 'readOnly', 'multiple', 'open']);

/**
 * h('div.card.wide', {onclick, title}, child, 'text', [more]) builds an
 * element. Strings become text nodes, so untrusted text is always safe.
 * @template {string} T
 * @param {T} tag
 * @param {Attrs | Child} [attrs]
 * @param {...Child} children
 * @returns {ElementFor<T>}
 */
export function h(tag, attrs, ...children) {
  const parts = tag.split('.');
  let name = parts.shift() || 'div';
  let id = '';
  if (name.includes('#')) [name, id] = name.split('#');
  const el = document.createElement(name || 'div');
  if (id) el.id = id;
  if (parts.length) el.className = parts.join(' ');
  if (attrs !== null && attrs !== undefined && (typeof attrs !== 'object' || attrs instanceof Node || Array.isArray(attrs))) {
    children.unshift(attrs);
    attrs = null;
  }
  if (attrs) setAttrs(el, /** @type {Attrs} */ (attrs));
  append(el, children);
  return /** @type {ElementFor<T>} */ (el);
}

/**
 * @param {HTMLElement} el
 * @param {Attrs} attrs
 */
function setAttrs(el, attrs) {
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') {
      el.className = [el.className, v].filter(Boolean).join(' ');
    } else if (k === 'text') {
      el.textContent = String(v);
    } else if (k === 'style' && typeof v === 'object') {
      Object.assign(el.style, v);
    } else if (k === 'dataset') {
      Object.assign(el.dataset, v);
    } else if (k.startsWith('on') && typeof v === 'function') {
      el.addEventListener(k.slice(2).toLowerCase(), /** @type {EventListener} */ (v));
    } else if (PROPS.has(k)) {
      Object.assign(el, { [k]: v }); // as el[k] = v, which the types allow only per tag
    } else {
      el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  return el;
}

/**
 * Appends children (as h() takes them) to el, and returns el.
 * @template {Node} E
 * @param {E} el
 * @param {readonly Child[]} children
 * @returns {E}
 */
export function append(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false || c === true) continue;
    if (Array.isArray(c)) append(el, c);
    // The typeof test changes nothing for a node, but CodeQL only takes a
    // typeof test, not instanceof, as proof that no string reaches
    // appendChild, and would otherwise report text from inputs as XSS.
    else if (typeof c === 'object' && c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
  return el;
}

/**
 * Replaces all children of el.
 * @template {Element | DocumentFragment} E
 * @param {E} el
 * @param {...Child} children
 * @returns {E}
 */
export function mount(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

// What reconcile() knows about the nodes it made: the key and the JSON of
// the item each shows. paint() remembers the JSON of what a region shows.
/** @type {WeakMap<Node, {key: string, sig: string}>} */
const items = new WeakMap();
/** @type {WeakMap<Node, string>} */
const regions = new WeakMap();

/**
 * A node reconcile() keeps: one with an update(item) refreshes in place.
 * @template T
 * @typedef {ChildNode & {update?: (item: T) => void}} Updatable
 */

/**
 * reconcile(container, list, key, render) makes container's children one
 * node per item of list, in order, and returns container. key(item) names
 * an item from one call to the next; render(item) builds its node. A node
 * stays as long as its item is unchanged (compared as JSON), so focus,
 * typed text, an open <details> and the chosen <option> inside it survive
 * a refresh. A changed item goes to the node's update(item) when it has
 * one, which refreshes it in place; otherwise render(item) replaces the
 * node. Nodes of items that are gone, and anything else in container (a
 * spinner, an empty state), are removed first, so the nodes that stay
 * move only when the order changed.
 * @template {Element | DocumentFragment} C
 * @template T
 * @param {C} container
 * @param {readonly T[]} list
 * @param {(item: T) => string | number} key
 * @param {(item: T) => Updatable<T>} render
 * @returns {C}
 */
export function reconcile(container, list, key, render) {
  const wanted = new Set(list.map((item) => String(key(item))));
  /** @type {Map<string, Updatable<T>>} */
  const keep = new Map();
  for (const n of [...container.childNodes]) {
    const was = items.get(n);
    if (was && wanted.has(was.key) && !keep.has(was.key)) keep.set(was.key, n);
    else n.remove();
  }
  list.forEach((item, i) => {
    const k = String(key(item));
    const sig = JSON.stringify(item);
    let n = keep.get(k);
    keep.delete(k); // an item with the same key again gets a node of its own
    if (n && items.get(n).sig !== sig) {
      if (typeof n.update === 'function') {
        n.update(item);
        items.get(n).sig = sig;
      } else {
        n.remove();
        n = null;
      }
    }
    if (!n) {
      n = render(item);
      items.set(n, { key: k, sig });
    }
    const at = container.childNodes[i] || null;
    if (at !== n) container.insertBefore(n, at);
  });
  return container;
}

/**
 * paint(container, data, render) shows render(data) (nodes, text or an
 * array of them) in container, and builds it again only when data changed
 * since container was last painted (compared as JSON). Returns whether it
 * painted. Everything render shows must come from data, so a change to it
 * is a change to data.
 * @template T
 * @param {Element | DocumentFragment} container
 * @param {T} data
 * @param {(data: T) => Child} render
 * @returns {boolean}
 */
export function paint(container, data, render) {
  const sig = JSON.stringify(data);
  if (regions.get(container) === sig) return false;
  regions.set(container, sig);
  mount(container, render(data));
  return true;
}

/**
 * Gives a <select> these [value, label] options, keeping the choice while
 * it is still offered (else fallback, or the first option). An unchanged
 * list leaves the select alone.
 * @param {HTMLSelectElement} sel
 * @param {readonly (readonly [string, string])[]} options
 * @param {string} [fallback]
 * @returns {HTMLSelectElement}
 */
export function syncOptions(sel, options, fallback) {
  const now = [...sel.querySelectorAll('option')].map((o) => [o.value, o.textContent]);
  if (JSON.stringify(now) === JSON.stringify(options)) return sel;
  const v = sel.value;
  sel.replaceChildren(...options.map(([value, label]) => h('option', { value }, label)));
  const offered = (x) => options.some(([o]) => o === x);
  sel.value = offered(v) ? v : (fallback !== undefined && offered(fallback) ? fallback : (options[0] ? options[0][0] : ''));
  return sel;
}
