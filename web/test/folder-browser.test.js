import { test } from 'node:test';
import assert from 'node:assert/strict';
import { getEventListeners } from 'node:events';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { folderBrowser } from '../assets/components/folder-browser.js';

const HOME = '/Users/you';
const listing = (path, names = []) => ({
  path, home: HOME, parent: path === HOME ? '' : HOME,
  entries: names.map((n) => ({ name: n, path: `${path}/${n}` })),
});
const tick = () => new Promise((r) => setTimeout(r, 0));
const current = (el) => el.querySelector('.fb-current').textContent;
const entry = (el, name) => el.querySelectorAll('.fb-item').find((b) => b.textContent.includes(name));

test('a slow folder cannot replace the one opened after it', async (t) => {
  installDom();
  const api = stubFetch(t);
  const el = folderBrowser({ start: `${HOME}/projects`, onPick: () => {} });
  api.answer(0, listing(`${HOME}/projects`, ['slow']));
  await tick();
  assert.equal(current(el), '~/projects');

  entry(el, 'slow').click();
  el.querySelector('button.crumb').click(); // back to Home while "slow" still loads
  assert.equal(api.calls[1].signal.aborted, true, 'opening another folder stops the slower load');
  api.answer(2, listing(HOME, ['inner']));
  await tick();
  api.answer(1, listing(`${HOME}/projects/slow`));
  await tick();
  assert.equal(current(el), '~');
  assert.ok(entry(el, 'inner'), 'the newer folder stays listed');
});

test('it stops loading when the page goes', async (t) => {
  installDom();
  const api = stubFetch(t);
  const page = new AbortController();
  const el = folderBrowser({ onPick: () => {}, signal: page.signal });
  page.abort();
  assert.equal(api.calls[0].signal.aborted, true);
  api.answer(0, listing(HOME, ['late']));
  await tick();
  assert.equal(entry(el, 'late'), undefined, 'nothing is painted into the page that is gone');
  assert.equal(el.querySelector('.form-error').hidden, true, 'and no error is shown for it');
});

test('opening folders adds no listeners to the page', async (t) => {
  installDom();
  const api = stubFetch(t);
  const page = new AbortController();
  const el = folderBrowser({ start: HOME, onPick: () => {}, signal: page.signal });
  const before = getEventListeners(page.signal, 'abort').length;
  for (let i = 0; i < 5; i++) {
    api.answer(i, listing(HOME, ['a']));
    await tick();
    entry(el, 'a').click();
  }
  assert.equal(getEventListeners(page.signal, 'abort').length, before);
  page.abort();
  assert.equal(api.calls[5].signal.aborted, true, 'leaving still stops the folder in progress');
  el.reload();
  assert.equal(api.calls.length, 6, 'and nothing loads after');
});
