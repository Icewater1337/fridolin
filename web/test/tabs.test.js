import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { tabs } from '../assets/components/tabs.js';

function setUp() {
  installDom();
  const shown = [];
  const t = tabs({
    label: 'Fruit',
    items: [['a', 'Apples'], ['b', 'Bananas'], ['c', 'Cherries']],
    selected: 'b',
    onSelect: (key, panel, { by }) => { shown.push(`${key}:${by}`); panel.textContent = key; },
  });
  document.body.append(t.list, t.panel);
  const tab = (text) => t.list.querySelectorAll('[role=tab]').find((b) => b.textContent === text);
  const press = (el, key) => {
    const e = Object.assign(new Event('keydown', { bubbles: true, cancelable: true }), { key });
    el.dispatchEvent(e);
    return e;
  };
  return { t, shown, tab, press };
}

const state = (t) => t.list.querySelectorAll('[role=tab]').map((b) => `${b.textContent}:${b.getAttribute('aria-selected')}:${b.getAttribute('tabindex')}`);

test('tabs name the panel they control, and the panel its tab', () => {
  const { t, tab, shown } = setUp();
  assert.equal(t.list.getAttribute('role'), 'tablist');
  assert.equal(t.list.getAttribute('aria-label'), 'Fruit');
  assert.equal(t.panel.getAttribute('role'), 'tabpanel');
  for (const b of t.list.querySelectorAll('[role=tab]')) {
    assert.equal(b.tagName, 'BUTTON');
    assert.equal(b.getAttribute('aria-controls'), t.panel.id);
  }
  assert.equal(t.panel.getAttribute('aria-labelledby'), tab('Bananas').id);
  assert.deepEqual(state(t), ['Apples:false:-1', 'Bananas:true:0', 'Cherries:false:-1'], 'only the chosen tab is in the Tab order');
  assert.deepEqual(shown, ['b:init']);
});

test('a click shows its tab', () => {
  const { t, tab, shown } = setUp();
  tab('Cherries').click();
  assert.deepEqual(state(t), ['Apples:false:-1', 'Bananas:false:-1', 'Cherries:true:0']);
  assert.equal(t.panel.getAttribute('aria-labelledby'), tab('Cherries').id);
  assert.equal(t.panel.textContent, 'c');
  tab('Cherries').click();
  assert.deepEqual(shown, ['b:init', 'c:click'], 'the shown tab is not built again');
});

test('arrow keys, Home and End move between the tabs and show them', () => {
  const { t, tab, press, shown } = setUp();
  const cases = [
    ['ArrowRight', 'Bananas', 'Cherries'],
    ['ArrowRight', 'Cherries', 'Apples'],
    ['ArrowLeft', 'Apples', 'Cherries'],
    ['Home', 'Cherries', 'Apples'],
    ['End', 'Apples', 'Cherries'],
  ];
  for (const [key, from, to] of cases) {
    const e = press(tab(from), key);
    assert.equal(e.defaultPrevented, true, `${key} does not scroll the page`);
    assert.equal(document.activeElement, tab(to), `${key} on ${from} focuses ${to}`);
    assert.equal(tab(to).getAttribute('aria-selected'), 'true');
    assert.equal(tab(to).getAttribute('tabindex'), '0');
  }
  assert.ok(shown.slice(1).every((s) => s.endsWith(':key')));
  const other = press(tab('Cherries'), 'Enter');
  assert.equal(other.defaultPrevented, false, 'other keys are left alone');
  assert.equal(state(t).filter((s) => s.includes(':true:')).length, 1);
});
