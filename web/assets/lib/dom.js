// The element builder every view uses. It never parses HTML: strings always
// become text nodes, so untrusted text is safe wherever it ends up.

const PROPS = new Set(['value', 'checked', 'disabled', 'selected', 'hidden', 'indeterminate', 'readOnly', 'multiple', 'open']);

/**
 * h('div.card.wide', {onclick, title}, child, 'text', [more]) builds an
 * element. Strings become text nodes, so untrusted text is always safe.
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
  if (attrs) setAttrs(el, attrs);
  append(el, children);
  return el;
}

export function setAttrs(el, attrs) {
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
      el.addEventListener(k.slice(2).toLowerCase(), v);
    } else if (PROPS.has(k)) {
      el[k] = v;
    } else {
      el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  return el;
}

export function append(el, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false || c === true) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
  return el;
}

/** Replaces all children of el. */
export function mount(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}
