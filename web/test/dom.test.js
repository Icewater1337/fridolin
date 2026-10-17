import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { h, reconcile, paint, syncOptions } from '../assets/lib/dom.js';

const row = (item) => h('li', { dataset: { id: item.id } }, item.text);
const texts = (el) => el.children.map((c) => c.textContent);

test('reconcile keeps the nodes of unchanged items', () => {
  installDom();
  const ul = h('ul', h('span.spinner'));
  reconcile(ul, [{ id: 'a', text: 'A' }, { id: 'b', text: 'B' }], (x) => x.id, row);
  assert.deepEqual(texts(ul), ['A', 'B'], 'the spinner made way');
  const [a, b] = ul.children;
  a.value = 'typed'; // what a person typed, or any other state of the node

  const cases = [
    ['the same items', [{ id: 'a', text: 'A' }, { id: 'b', text: 'B' }], ['A', 'B']],
    ['one more at the top', [{ id: 'c', text: 'C' }, { id: 'a', text: 'A' }, { id: 'b', text: 'B' }], ['C', 'A', 'B']],
    ['in another order', [{ id: 'b', text: 'B' }, { id: 'c', text: 'C' }, { id: 'a', text: 'A' }], ['B', 'C', 'A']],
    ['one gone', [{ id: 'b', text: 'B' }, { id: 'a', text: 'A' }], ['B', 'A']],
  ];
  for (const [name, list, want] of cases) {
    reconcile(ul, list, (x) => x.id, row);
    assert.deepEqual(texts(ul), want, name);
    assert.equal(ul.children.find((n) => n.dataset.id === 'a'), a, `${name}: a is the same node`);
    assert.equal(ul.children.find((n) => n.dataset.id === 'b'), b, `${name}: b is the same node`);
  }
  assert.equal(a.value, 'typed');
});

test('reconcile moves nodes only when the order changed', () => {
  installDom();
  const ul = h('ul');
  const list = ['a', 'b', 'c'].map((id) => ({ id, text: id }));
  reconcile(ul, list, (x) => x.id, row);
  let moves = 0;
  const insert = ul.insertBefore.bind(ul);
  ul.insertBefore = (n, ref) => { if (n.parentNode === ul) moves++; return insert(n, ref); };
  reconcile(ul, [{ id: 'new', text: 'new' }, ...list], (x) => x.id, row);
  reconcile(ul, list.slice(1), (x) => x.id, row);
  reconcile(ul, [...list.slice(1), { id: 'd', text: 'd' }], (x) => x.id, row);
  assert.equal(moves, 0, 'adding and removing items moves none of the others');
  reconcile(ul, [{ id: 'd', text: 'd' }, ...list.slice(1)], (x) => x.id, row);
  assert.equal(moves, 1);
});

test('a changed item is updated in place, or rebuilt', () => {
  installDom();
  const cases = [
    ['without update(), the node is rebuilt', false],
    ['with update(), the node refreshes itself', true],
  ];
  for (const [name, withUpdate] of cases) {
    const ul = h('ul');
    let built = 0;
    const render = (item) => {
      built++;
      const li = row(item);
      if (withUpdate) li.update = (next) => { li.textContent = next.text; };
      return li;
    };
    reconcile(ul, [{ id: 'a', text: 'old' }], (x) => x.id, render);
    const first = ul.firstChild;
    reconcile(ul, [{ id: 'a', text: 'new' }], (x) => x.id, render);
    assert.deepEqual(texts(ul), ['new'], name);
    assert.equal(ul.firstChild === first, withUpdate, name);
    assert.equal(built, withUpdate ? 1 : 2, name);
    reconcile(ul, [{ id: 'a', text: 'new' }], (x) => x.id, render);
    assert.equal(built, withUpdate ? 1 : 2, `${name}: and then kept`);
  }
});

test('items with the same key get a node each', () => {
  installDom();
  const ul = h('ul');
  reconcile(ul, [{ id: 'a', text: '1' }, { id: 'a', text: '2' }], (x) => x.id, row);
  assert.deepEqual(texts(ul), ['1', '2']);
  reconcile(ul, [{ id: 'a', text: '1' }], (x) => x.id, row);
  assert.deepEqual(texts(ul), ['1']);
});

test('paint builds a region again only when its data changed', () => {
  installDom();
  const box = h('div');
  const other = h('div');
  let built = 0;
  const render = (d) => { built++; return h('p', d.text); };
  assert.equal(paint(box, { text: 'a' }, render), true);
  const p = box.firstChild;
  assert.equal(paint(box, { text: 'a' }, render), false);
  assert.equal(box.firstChild, p, 'unchanged data keeps the nodes');
  assert.equal(paint(other, { text: 'a' }, render), true, 'each region remembers its own data');
  assert.equal(paint(box, { text: 'b' }, render), true);
  assert.equal(box.textContent, 'b');
  assert.equal(built, 3);
});

test('syncOptions keeps the choice while it is offered', () => {
  installDom();
  const cases = [
    // name, chosen before, new options, fallback, chosen after
    ['still offered', 'b', [['a', 'A'], ['b', 'B'], ['c', 'C']], undefined, 'b'],
    ['gone, no fallback', 'b', [['a', 'A'], ['c', 'C']], undefined, 'a'],
    ['gone, with a fallback', 'b', [['a', 'A'], ['c', 'C']], 'c', 'c'],
    ['gone, fallback not offered', 'b', [['a', 'A']], 'x', 'a'],
    ['no options', 'b', [], undefined, ''],
  ];
  for (const [name, before, options, fallback, want] of cases) {
    const sel = h('select', h('option', { value: 'a' }, 'A'), h('option', { value: 'b' }, 'B'));
    sel.value = before;
    syncOptions(sel, options, fallback);
    assert.equal(sel.value, want, name);
    assert.deepEqual(sel.children.map((o) => [o.value, o.textContent]), options, name);
  }
  const sel = h('select', h('option', { value: 'a' }, 'A'));
  const opt = sel.firstChild;
  syncOptions(sel, [['a', 'A']]);
  assert.equal(sel.firstChild, opt, 'the same options leave the select alone');
});
