// Just enough of the DOM for the dashboard's components to run under node
// --test: elements, text, attributes, classes, events and simple selectors
// (tag, #id, .class, [attr], [attr=value], descendants, commas). It has no
// layout and no HTML parser, which the dashboard never uses.

class FakeNode {
  constructor() {
    this.childNodes = [];
    this.parentNode = null;
  }

  get children() { return this.childNodes.filter((n) => n instanceof FakeElement); }
  get firstChild() { return this.childNodes[0] || null; }
  get lastChild() { return this.childNodes[this.childNodes.length - 1] || null; }
  get isConnected() {
    /** @type {FakeNode} */
    let n = this;
    while (n.parentNode) n = n.parentNode;
    return n === fakeDocument();
  }

  get textContent() { return this.childNodes.map((n) => n.textContent).join(''); }
  set textContent(v) { this.replaceChildren(String(v)); }

  appendChild(child) {
    if (child instanceof FakeFragment) {
      [...child.childNodes].forEach((c) => this.appendChild(c));
      return child;
    }
    child.remove();
    child.parentNode = this;
    this.childNodes.push(child);
    return child;
  }

  // Strings become text nodes; the typeof test is what CodeQL takes as
  // proof that no string reaches appendChild (as in lib/dom.js).
  append(...nodes) { nodes.forEach((n) => this.appendChild(typeof n === 'object' && n instanceof FakeNode ? n : new FakeText(String(n)))); }

  insertBefore(child, ref) {
    if (!ref) return this.appendChild(child);
    child.remove();
    child.parentNode = this;
    this.childNodes.splice(this.childNodes.indexOf(ref), 0, child);
    return child;
  }

  prepend(...nodes) {
    const rest = this.childNodes.splice(0);
    rest.forEach((n) => { n.parentNode = null; });
    this.append(...nodes, ...rest);
  }

  replaceChildren(...nodes) {
    this.childNodes.splice(0).forEach((n) => { n.parentNode = null; });
    this.append(...nodes);
  }

  remove() {
    if (!this.parentNode) return;
    const siblings = this.parentNode.childNodes;
    siblings.splice(siblings.indexOf(this), 1);
    this.parentNode = null;
  }

  contains(n) {
    for (; n; n = n.parentNode) if (n === this) return true;
    return false;
  }

  querySelectorAll(selector) {
    const out = [];
    const visit = (n) => n.children.forEach((c) => { if (matches(c, selector)) out.push(c); visit(c); });
    visit(this);
    return out;
  }

  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}

class FakeText extends FakeNode {
  constructor(data) { super(); this.data = data; }
  /** @override */
  get textContent() { return this.data; }
  /** @override */
  set textContent(v) { this.data = String(v); }
}

class FakeFragment extends FakeNode {}

class FakeElement extends FakeNode {
  constructor(tag) {
    super();
    this.tagName = tag.toUpperCase();
    this.attributes = new Map();
    this.style = {};
    this.dataset = {};
    this.listeners = new Map();
    this.hidden = false;
    this.disabled = false;
    this.value = '';
    this.checked = false;
    const el = this;
    this.classList = {
      add: (...c) => { el.className = [...new Set([...el.classes(), ...c])].join(' '); },
      remove: (...c) => { el.className = el.classes().filter((x) => !c.includes(x)).join(' '); },
      contains: (c) => el.classes().includes(c),
      toggle: (c, on = !el.classList.contains(c)) => { if (on) el.classList.add(c); else el.classList.remove(c); return on; },
    };
  }

  classes() { return (this.getAttribute('class') || '').split(/\s+/).filter(Boolean); }
  get className() { return this.getAttribute('class') || ''; }
  set className(v) { this.setAttribute('class', v); }
  get id() { return this.getAttribute('id') || ''; }
  set id(v) { this.setAttribute('id', v); }
  get title() { return this.getAttribute('title') || ''; }
  set title(v) { this.setAttribute('title', v); }

  setAttribute(k, v) { this.attributes.set(k, String(v)); }
  getAttribute(k) { return this.attributes.has(k) ? this.attributes.get(k) : null; }
  hasAttribute(k) { return this.attributes.has(k); }
  removeAttribute(k) { this.attributes.delete(k); }

  addEventListener(type, fn) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type).add(fn);
  }

  removeEventListener(type, fn) { if (this.listeners.has(type)) this.listeners.get(type).delete(fn); }

  dispatchEvent(event) {
    if (!event.target) Object.defineProperty(event, 'target', { value: this });
    for (const fn of [...(this.listeners.get(event.type) || [])]) fn.call(this, event);
    if (event.bubbles && this.parentNode && this.parentNode.dispatchEvent) this.parentNode.dispatchEvent(event);
    return !event.defaultPrevented;
  }

  closest(selector) {
    for (let n = this; n instanceof FakeElement; n = n.parentNode) if (matches(n, selector)) return n;
    return null;
  }

  click() { if (!this.disabled) this.dispatchEvent(new Event('click', { bubbles: true, cancelable: true })); }
  focus() { fakeDocument().activeElement = this; }
  scrollIntoView() {}
  showModal() { this.open = true; }
  close() { this.open = false; }
}


// One compound selector part, e.g. button.crumb[type=button]#x
function matchesCompound(el, sel) {
  const re = /([#.]?)([\w-]+)|\[([\w-]+)(?:="?([^"\]]*)"?)?\]/g;
  for (const m of sel.matchAll(re)) {
    if (m[3]) {
      if (!el.hasAttribute(m[3])) return false;
      if (m[4] !== undefined && el.getAttribute(m[3]) !== m[4]) return false;
    } else if (m[1] === '#') {
      if (el.id !== m[2]) return false;
    } else if (m[1] === '.') {
      if (!el.classList.contains(m[2])) return false;
    } else if (el.tagName !== m[2].toUpperCase()) {
      return false;
    }
  }
  return true;
}

function matches(el, selector) {
  return selector.split(',').some((group) => {
    const parts = group.trim().split(/\s+/);
    if (!matchesCompound(el, parts.pop())) return false;
    let n = el.parentNode;
    for (let i = parts.length - 1; i >= 0; i--) {
      while (n && !(n instanceof FakeElement && matchesCompound(n, parts[i]))) n = n.parentNode;
      if (!n) return false;
      n = n.parentNode;
    }
    return true;
  });
}

class FakeDocument extends FakeNode {
  constructor() {
    super();
    this.hidden = false;
    this.documentElement = this.appendChild(new FakeElement('html'));
    this.body = this.documentElement.appendChild(new FakeElement('body'));
    this.activeElement = this.body;
  }

  createElement(tag) { return new FakeElement(tag); }
  createElementNS(ns, tag) { return new FakeElement(tag); }
  createTextNode(text) { return new FakeText(text); }
  createDocumentFragment() { return new FakeFragment(); }
  getElementById(id) { return this.querySelector(`#${id}`); }
}

// The document installDom installed, which tsc knows as the DOM's Document.
const fakeDocument = () => /** @type {FakeDocument} */ (/** @type {unknown} */ (globalThis.document));

/** Installs a fresh document (and Node) as globals; returns it. */
export function installDom() {
  Object.assign(globalThis, { Node: FakeNode, document: new FakeDocument() });
  return globalThis.document;
}
