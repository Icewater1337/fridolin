// The router moves the keyboard focus to a page the person went to, so a
// screen reader reads it from its top and Tab goes on from there, but
// leaves it alone when the dashboard opens.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { fakeWindow } from './fake-window.js';
import { runThemeScript } from './theme-script.js';
import { settle } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createShell } from '../assets/app/shell.js';
import { createNavigation } from '../assets/app/navigation.js';

// Pages with a heading; settings names the section its address points
// to, as the Settings page does.
const page = (title, extra = () => []) => async () => ({
  default: {
    title,
    render(root, ctx) {
      root.append(h('h1', title), ...extra(ctx));
      if (ctx.params.section) ctx.focusAt(root.querySelector(`#sec-${ctx.params.section} h2`));
    },
  },
});
const ROUTES = [
  { id: 'overview', path: 'overview', load: page('Overview') },
  { id: 'settings', path: 'settings/:section?', load: page('Settings', () => [h('section', { id: 'sec-access' }, h('h2', 'Access'))]) },
];

function open(t, start = '#/overview', { onAddress } = {}) {
  installDom();
  runThemeScript(t);
  const w = fakeWindow([start]);
  const shell = createShell({ onRestart: () => {} });
  document.body.append(...shell.nodes);
  const view = createNavigation({ win: w.win, shell, routes: ROUTES, confirm: async () => true, onAddress });
  const heading = () => shell.page.querySelector('h1');
  return { w, shell, view, heading };
}

test('opening the dashboard leaves the focus where the browser put it', async (t) => {
  const { view, heading } = open(t, '#/settings/access');
  view.start();
  await settle();
  assert.equal(heading().textContent, 'Settings');
  assert.equal(document.activeElement, document.body);
});

test('following a link moves the focus to the new page', async (t) => {
  for (const [href, want] of [
    ['#/settings', (s) => s.page.querySelector('h1')],
    ['#/settings/access', (s) => s.page.querySelector('#sec-access h2')],
    ['#/nope', (s) => s.page], // no page, so no heading: the page area
  ]) {
    await t.test(href, async (t) => {
      const { w, shell, view } = open(t);
      view.start();
      await settle();
      w.click(href);
      await settle();
      assert.equal(document.activeElement, want(shell));
      assert.equal(document.activeElement.getAttribute('tabindex'), '-1', 'focusable, but not in the Tab order');
    });
  }
});

test('a part named late takes the focus only while it is where it was put', async (t) => {
  let later = null;
  ROUTES.push({ id: 'slow', path: 'slow', load: page('Slow', (ctx) => { later = ctx; return [h('h2', 'Part')]; }) });
  try {
    const { w, shell, view, heading } = open(t);
    view.start();
    await settle();
    w.click('#/slow');
    await settle();
    assert.equal(document.activeElement, heading());
    later.focusAt(shell.page.querySelector('h2'));
    assert.equal(document.activeElement, shell.page.querySelector('h2'), 'moved from the page heading');
    const elsewhere = h('button');
    document.body.append(elsewhere);
    elsewhere.focus();
    later.focusAt(heading());
    assert.equal(document.activeElement, elsewhere, 'the person moved on');
  } finally {
    ROUTES.pop();
  }
});

test('coming back from the connect screen moves the focus to the page', async (t) => {
  const { w, view, heading } = open(t);
  view.start();
  await settle();
  view.pause();
  w.click('#/settings');
  await settle();
  assert.equal(heading().textContent, 'Overview', 'nothing renders behind the connect screen');
  view.resume();
  await settle();
  assert.equal(heading().textContent, 'Settings');
  assert.equal(document.activeElement, heading());
});

test('an address the app takes care of itself renders nothing', async (t) => {
  const { w, view, heading } = open(t, '#/overview', { onAddress: () => w.win.location.hash.startsWith('#token=') });
  view.start();
  await settle();
  w.click('#token=abc');
  await settle();
  assert.equal(heading().textContent, 'Overview');
  assert.equal(document.activeElement, document.body);
});
