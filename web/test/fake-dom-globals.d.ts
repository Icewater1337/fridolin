// The DOM the tests run in (fake-dom.js), as tsc sees it where it differs
// from a browser's: querySelectorAll answers with an array, and every
// element has the properties of the inputs, buttons and dialogs the tests
// read (fake-dom.js gives each of them value, checked, disabled, hidden,
// open, click() and focus()). Only test/tsconfig.json reads this file:
// the modules are checked against the browser's DOM by tsconfig.json,
// where none of this holds, so a module that relied on it fails there.

/** An element of the fake DOM. */
type FakeElement = HTMLInputElement & HTMLDialogElement;

interface ParentNode {
  querySelector<E extends Element = FakeElement>(selectors: string): E | null;
  querySelectorAll<K extends keyof HTMLElementTagNameMap>(selectors: K): HTMLElementTagNameMap[K][];
  querySelectorAll<E extends Element = FakeElement>(selectors: string): E[];
}
