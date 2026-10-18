import { test } from 'node:test';
import assert from 'node:assert/strict';
import { guardNavigation } from '../assets/app/nav-guard.js';
import { fakeWindow } from './fake-window.js';

const settle = async () => { for (let i = 0; i < 6; i++) await new Promise((r) => setImmediate(r)); };

// The tab loads the first address and visits the others by links, the way
// its history comes about: every entry of a page's history was visited in
// it. (An entry from before a reload belongs to the old page, and going
// back to it loads the dashboard again.)
async function setup(start = ['#/overview']) {
  const w = fakeWindow([start[0]]);
  const page = { dirty: false, rendered: [], asked: 0, answer: null };
  const nav = guardNavigation({
    win: w.win,
    isDirty: () => page.dirty,
    confirm: () => { page.asked++; return new Promise((resolve) => { page.answer = resolve; }); },
    onDiscard: () => { page.dirty = false; },
    onNavigate: () => page.rendered.push(w.win.location.hash),
  });
  for (const hash of start.slice(1)) {
    w.click(hash);
    await settle();
  }
  page.rendered.length = 0;
  return { w, page, nav };
}

test('without unsaved changes, links and Back just navigate', async () => {
  const { w, page } = await setup();
  w.click('#/brains');
  await settle();
  w.click('#/chats');
  await settle();
  w.win.history.go(-1);
  await settle();
  assert.deepEqual(page.rendered, ['#/brains', '#/chats', '#/brains']);
  assert.deepEqual([w.hashes(), w.at()], [['#/overview', '#/brains', '#/chats'], 1]);
  assert.equal(page.asked, 0);
});

test('Back from unsaved changes asks, and either way the history stays intact', async (t) => {
  const cases = [
    // name, answer, rendered, position afterwards
    ['cancelled', false, [], 2],
    ['confirmed', true, ['#/brains'], 1],
  ];
  for (const [name, answer, rendered, at] of cases) {
    await t.test(name, async () => {
      const { w, page } = await setup(['#/overview', '#/brains', '#/brains/work']);
      page.dirty = true;
      w.win.history.go(-1);
      await settle();
      assert.equal(page.asked, 1);
      assert.equal(w.win.location.hash, '#/brains/work', 'the address is put back while it asks');
      page.answer(answer);
      await settle();
      assert.deepEqual(page.rendered, rendered);
      assert.deepEqual([w.hashes(), w.at()], [['#/overview', '#/brains', '#/brains/work'], at], 'no entry added, none overwritten');

      // Back keeps working afterwards.
      page.dirty = false;
      page.rendered.length = 0;
      w.win.history.go(-1);
      await settle();
      assert.deepEqual(page.rendered, [at === 2 ? '#/brains' : '#/overview']);
    });
  }
});

test('Forward and a jump of several entries are undone and redone as a whole', async () => {
  const { w, page } = await setup(['#/overview', '#/brains', '#/chats', '#/logs']);
  page.dirty = true;
  w.win.history.go(-3);
  await settle();
  assert.equal(w.win.location.hash, '#/logs');
  page.answer(true);
  await settle();
  assert.deepEqual(page.rendered, ['#/overview']);
  assert.equal(w.at(), 0);

  page.dirty = true;
  w.win.history.go(2);
  await settle();
  assert.equal(w.win.location.hash, '#/overview');
  page.answer(false);
  await settle();
  assert.deepEqual([w.at(), page.rendered], [0, ['#/overview']], 'Forward is undone too');
});

test('a link from unsaved changes adds an entry only once confirmed', async (t) => {
  const cases = [
    ['cancelled', false, ['#/overview', '#/settings'], []],
    ['confirmed', true, ['#/overview', '#/settings', '#/brains'], ['#/brains']],
  ];
  for (const [name, answer, hashes, rendered] of cases) {
    await t.test(name, async () => {
      const { w, page } = await setup(['#/overview', '#/settings']);
      page.dirty = true;
      w.click('#/brains');
      assert.deepEqual(w.hashes(), ['#/overview', '#/settings'], 'held before the browser follows it');
      page.answer(answer);
      await settle();
      assert.deepEqual(w.hashes(), hashes);
      assert.deepEqual(page.rendered, rendered);
    });
  }
});

test('a link opened in a new tab is not held', async () => {
  const { w, page } = await setup();
  page.dirty = true;
  w.click('#/brains', { metaKey: true });
  assert.equal(page.asked, 0);
});

test('a link within the page is no navigation', async () => {
  const { w, page } = await setup();
  page.dirty = true;
  w.click('#main', { handled: true }); // the skip link, which moves the focus itself
  await settle();
  assert.equal(page.asked, 0, 'nothing to confirm');
  assert.deepEqual(w.hashes(), ['#/overview']);
  assert.deepEqual(page.rendered, []);
});

test('an address typed while there are unsaved changes is put back unless confirmed', async () => {
  const { w, page } = await setup(['#/overview', '#/settings']);
  page.dirty = true;
  w.win.location.hash = '#/logs';
  await settle();
  assert.equal(w.win.location.hash, '#/settings');
  page.answer(false);
  await settle();
  assert.deepEqual(page.rendered, []);
  assert.equal(w.win.location.hash, '#/settings');
});

test('one question at a time', async () => {
  const { w, page } = await setup(['#/overview', '#/brains', '#/settings']);
  page.dirty = true;
  w.win.history.go(-1);
  await settle();
  w.click('#/chats');
  w.win.history.go(-1);
  await settle();
  assert.equal(page.asked, 1, 'a second move while the dialog is open asks nothing more');
  assert.equal(w.win.location.hash, '#/settings');
  page.answer(false);
  await settle();
  assert.deepEqual(w.hashes(), ['#/overview', '#/brains', '#/settings']);
  assert.deepEqual(page.rendered, []);
});

test('go() asks like a link, replace() keeps the entry and its place', async () => {
  const { w, page, nav } = await setup(['#/overview', '#/settings']);
  nav.replace('#/settings/claude');
  assert.deepEqual(w.hashes(), ['#/overview', '#/settings/claude']);
  page.dirty = true;
  const going = nav.go('#/brains');
  assert.equal(page.asked, 1);
  page.answer(true);
  await going;
  await settle();
  assert.deepEqual(page.rendered, ['#/brains']);
  page.rendered.length = 0;
  w.win.history.go(-1);
  await settle();
  assert.deepEqual(page.rendered, ['#/settings/claude'], 'Back lands on the replaced address');
});

test('moving between entries with the same address keeps count', async () => {
  const { w, page, nav } = await setup(['#/overview']);
  w.click('#/settings/claude');
  await settle();
  w.click('#/settings');
  await settle();
  nav.replace('#/settings/claude'); // a section chip
  w.win.history.go(-1); // the same address: no hashchange
  await settle();
  page.rendered.length = 0;
  page.dirty = true;
  w.win.history.go(-1);
  await settle();
  page.answer(true);
  await settle();
  assert.deepEqual(page.rendered, ['#/overview']);
  assert.equal(w.at(), 0);
});
