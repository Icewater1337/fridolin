import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { h } from '../assets/lib/dom.js';
import { liveForm } from '../assets/components/live-form.js';

/**
 * A form with one text field for data.name; data.other is context it shows
 * but does not edit.
 * @param {{edits?: (data: {name: string, other?: number}) => unknown}} [opts]
 */
function nameForm({ edits } = {}) {
  installDom();
  const states = [];
  let builds = 0;
  const f = liveForm({
    build: (data) => {
      builds++;
      const input = h('input', { value: data.name });
      return { el: h('form', input), value: () => ({ name: input.value }), input };
    },
    edits: edits || ((d) => d.name),
    onChange: (s) => states.push(s),
  });
  document.body.append(f.el);
  const type = (text) => {
    const { input } = f.form();
    input.value = text;
    input.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const note = () => f.el.querySelector('.callout');
  return { f, states, type, note, builds: () => builds };
}

test('a form without edits follows the data', () => {
  const { f, builds } = nameForm();
  f.set({ name: 'Work', other: 1 });
  assert.equal(f.form().input.value, 'Work');
  f.set({ name: 'Work', other: 1 });
  assert.equal(builds(), 1, 'the same data builds nothing');
  f.set({ name: 'Office', other: 2 });
  assert.equal(f.form().input.value, 'Office');
  assert.equal(builds(), 2);
  assert.equal(f.dirty(), false);
});

test('edits survive new data, and a change to what they edit is flagged', async (t) => {
  /** @type {[string, {name: string, other: number}, boolean][]} */
  const cases = [
    // name, newer data, stale?
    ['context changed', { name: 'Work', other: 2 }, false],
    ['the edited part changed', { name: 'Office', other: 1 }, true],
  ];
  for (const [name, newer, stale] of cases) {
    await t.test(name, () => {
      const { f, type, note, states, builds } = nameForm();
      f.set({ name: 'Work', other: 1 });
      const input = f.form().input;
      type('Home');
      assert.equal(f.dirty(), true);
      assert.deepEqual(states.at(-1), { dirty: true, stale: false });

      f.set(newer);
      assert.equal(builds(), 1, 'the edits are kept');
      assert.equal(f.form().input, input);
      assert.equal(input.value, 'Home');
      assert.equal(f.stale(), stale);
      assert.equal(note().hidden, !stale);
      assert.deepEqual(states.at(-1), { dirty: true, stale });

      f.discard();
      assert.equal(f.form().input.value, newer.name, 'discard builds the form from the newest data');
      assert.deepEqual([f.dirty(), f.stale(), note().hidden], [false, false, true]);
    });
  }
});

test('a form taken back to what it showed is clean, and not stale', () => {
  const { f, type, note } = nameForm();
  f.set({ name: 'Work' });
  type('Home');
  f.set({ name: 'Office' });
  assert.equal(f.stale(), true);
  type('Work');
  assert.deepEqual([f.dirty(), f.stale(), note().hidden], [false, false, true]);
  type('Home again');
  assert.equal(f.stale(), true, 'stale again with new edits');
});

test('a form taken back to what it showed catches up once the focus leaves it', () => {
  const { f, type, builds } = nameForm();
  f.set({ name: 'Work', other: 1 });
  const input = f.form().input;
  type('Home');
  f.set({ name: 'Work', other: 2 }); // context changed elsewhere meanwhile
  type('Work');
  assert.equal(f.dirty(), false);
  const leave = (to) => {
    const e = Object.assign(new Event('focusout', { bubbles: true }), { relatedTarget: to });
    f.form().input.dispatchEvent(e);
  };
  leave(f.form().el); // still in the form: typing goes on undisturbed
  assert.equal(f.form().input, input);
  leave(null);
  assert.equal(builds(), 2, 'built from the newest data');
  assert.notEqual(f.form().input, input);
  leave(null);
  assert.equal(builds(), 2, 'once');
});

test('saved edits wait for the data that holds them', () => {
  const { f, type, builds } = nameForm();
  f.set({ name: 'Work', other: 1 });
  type('Home');
  f.set({ name: 'Work', other: 2 });
  f.saved();
  const e = new Event('focusout', { bubbles: true });
  f.form().input.dispatchEvent(e);
  assert.equal(f.form().input.value, 'Home', 'not built from the data before the save');
  f.set({ name: 'Home', other: 2 });
  assert.equal(builds(), 2);
  assert.equal(f.form().input.value, 'Home');
});

test('the stale note loads the new data', () => {
  const { f, type, note } = nameForm();
  f.set({ name: 'Work' });
  type('Home');
  f.set({ name: 'Office' });
  note().querySelector('button').click();
  return new Promise((r) => setImmediate(r)).then(() => {
    assert.equal(f.form().input.value, 'Office');
    assert.equal(f.dirty(), false);
  });
});

test('after a save the next data builds the form again', () => {
  const { f, type, builds } = nameForm();
  f.set({ name: 'Work' });
  type('Home');
  f.saved();
  assert.equal(f.dirty(), false, 'saved edits are not unsaved changes');
  f.set({ name: 'Home' });
  assert.equal(builds(), 2);
  assert.equal(f.stale(), false, 'its own save is not a change elsewhere');
});

test('edits() can look at the form', async (t) => {
  // Only the fields that have edits count, as on the Settings page.
  /** @type {[string, {a: string, b: string}, boolean][]} */
  const cases = [
    // name, newer data, stale?
    ['a field without edits changed', { a: 'a', b: 'B' }, false],
    ['the edited field changed', { a: 'A', b: 'b' }, true],
  ];
  for (const [name, newer, stale] of cases) {
    await t.test(name, () => {
      installDom();
      const f = liveForm({
        build: (data) => {
          const inputs = { a: h('input', { value: data.a }), b: h('input', { value: data.b }) };
          const edited = () => Object.keys(inputs).filter((k) => inputs[k].value !== data[k]);
          return { el: h('form', inputs.a, inputs.b), value: () => ({ a: inputs.a.value, b: inputs.b.value }), inputs, edited };
        },
        edits: (d, form) => form.edited().map((k) => d[k]),
      });
      f.set({ a: 'a', b: 'b' });
      f.form().inputs.a.value = 'mine';
      f.set(newer);
      assert.equal(f.stale(), stale);
    });
  }
});
