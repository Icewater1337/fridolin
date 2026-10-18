import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { h } from '../assets/lib/dom.js';
import { callout } from '../assets/components/common.js';

test('callout', async (t) => {
  const cases = [
    {
      name: 'a title and a text',
      make: () => callout('warn', 'Careful', 'It changes routing.'),
      cls: 'callout warn', title: 'Careful', text: 'It changes routing.',
    },
    {
      name: 'no title',
      make: () => callout('info', null, 'Just so you know.'),
      cls: 'callout info', title: null, text: 'Just so you know.',
    },
    {
      name: 'a bad one is an alert',
      make: () => callout('bad', 'It failed', 'exit status 1'),
      cls: 'callout bad', role: 'alert', title: 'It failed', text: 'exit status 1',
    },
    {
      name: 'attributes before the body',
      make: () => callout('warn', null, { class: 'inline-callout', id: 'note' }, 'Tools run without asking.'),
      cls: 'callout warn inline-callout', id: 'note', title: null, text: 'Tools run without asking.',
    },
    {
      name: 'hidden at first',
      make: () => callout('warn', 'Changed elsewhere', { hidden: true }, 'Reload to see it.'),
      cls: 'callout warn', hidden: true, title: 'Changed elsewhere', text: 'Reload to see it.',
    },
    {
      name: 'a node first is body, not attributes',
      make: () => callout('info', 'Steps', h('ol', h('li', 'one')), 'two'),
      cls: 'callout info', title: 'Steps', text: 'onetwo',
    },
    {
      name: 'a list first is body, not attributes',
      make: () => callout('info', 'Steps', ['one', 'two']),
      cls: 'callout info', title: 'Steps', text: 'onetwo',
    },
    {
      name: 'null first is body that shows nothing',
      make: () => callout('ok', 'Done', null, 'saved'),
      cls: 'callout ok', title: 'Done', text: 'saved',
    },
    {
      name: 'a title alone',
      make: () => callout('ok', 'Done'),
      cls: 'callout ok', title: 'Done', text: null,
    },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      installDom();
      const el = c.make();
      assert.equal(el.className, c.cls);
      assert.equal(el.getAttribute('role'), c.role || null);
      assert.equal(el.id, c.id || '');
      assert.equal(el.hidden, !!c.hidden);
      assert.ok(el.querySelector('.callout-icon'), 'it has its icon');
      const strong = el.querySelector('.callout-body strong');
      assert.equal(strong ? strong.textContent : null, c.title);
      const text = el.querySelector('.callout-text');
      assert.equal(text ? text.textContent : null, c.text);
    });
  }
});
