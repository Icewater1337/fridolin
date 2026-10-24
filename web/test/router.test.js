import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ROUTES } from '../assets/app/routes.js';
import { matchRoute, routeHref, activeNav, docTitle, credentialFromHash } from '../assets/app/router.js';

test('matchRoute finds the page and its parameters', async (t) => {
  /** @type {[string, string, string | null, Record<string, string> | null][]} */
  const cases = [
    ['no hash', '', 'overview', {}],
    ['bare #/', '#/', 'overview', {}],
    ['overview', '#/overview', 'overview', {}],
    ['activity', '#/activity', 'activity', {}],
    ['activity of one brain', '#/activity/work', 'activity', { brain: 'work' }],
    ['brains', '#/brains', 'brains', {}],
    ['one brain', '#/brains/work', 'brain', { id: 'work' }],
    ['encoded id', '#/brains/a%20b', 'brain', { id: 'a b' }],
    ['malformed escape kept as typed', '#/brains/%E0%A4%A', 'brain', { id: '%E0%A4%A' }],
    ['trailing slash', '#/chats/', 'chats', {}],
    ['add brain', '#/add-brain', 'add-brain', {}],
    ['settings', '#/settings', 'settings', {}],
    ['a settings section', '#/settings/sandbox', 'settings', { section: 'sandbox' }],
    ['dev', '#/dev', 'dev', {}],
    ['unknown page', '#/nope', null, null],
    ['extra segment', '#/overview/more', null, null],
    ['brain with extra segment', '#/brains/work/more', null, null],
  ];
  for (const [name, hash, id, params] of cases) {
    await t.test(name, () => {
      const m = matchRoute(ROUTES, hash);
      assert.equal(m ? m.route.id : null, id);
      if (m) assert.deepEqual(m.params, params);
    });
  }
});

test('the sidebar highlights the entry a page belongs to', async (t) => {
  const cases = [
    ['#/brains', 'brains'],
    ['#/brains/work', 'brains'],
    ['#/add-brain', 'brains'],
    ['#/activity/work', 'activity'],
    ['#/logs', 'logs'],
  ];
  for (const [hash, want] of cases) {
    await t.test(hash, () => assert.equal(activeNav(matchRoute(ROUTES, hash).route), want));
  }
  await t.test('no page, no entry', () => assert.equal(activeNav(null), ''));
});

test('the route table is consistent', async (t) => {
  const ids = new Set(ROUTES.map((r) => r.id));
  await t.test('ids are unique', () => assert.equal(ids.size, ROUTES.length));
  for (const r of ROUTES) {
    await t.test(r.id, () => {
      assert.equal(typeof r.load, 'function', 'every route loads a page');
      if (r.nav) {
        assert.ok(r.nav.label && r.nav.icon, 'a sidebar entry needs a label and an icon');
        assert.equal(matchRoute(ROUTES, routeHref(r)).route, r, 'its sidebar link leads back to it');
      } else {
        assert.ok(ROUTES.some((x) => x.nav && x.id === r.navId), `navId ${r.navId} is no sidebar entry`);
      }
    });
  }
});

test('routeHref drops parameters', () => {
  assert.equal(routeHref({ path: 'activity/:brain?' }), '#/activity');
  assert.equal(routeHref({ path: 'brains' }), '#/brains');
});

test('every title names the product the same way', async (t) => {
  await t.test('page', () => assert.equal(docTitle('Brains'), 'Brains · Fridolin'));
  await t.test('not found', () => assert.equal(docTitle('Not found'), 'Not found · Fridolin'));
  await t.test('no title', () => assert.equal(docTitle(''), 'Fridolin'));
});

test('credentialFromHash', async (t) => {
  /** @type {[string, string, ReturnType<typeof credentialFromHash>][]} */
  const cases = [
    ['no hash', '', null],
    ['a page', '#/overview', null],
    ['a login code', '#login=abc123', { code: 'abc123' }],
    ['an escaped code', '#login=a%2Bb', { code: 'a+b' }],
    ['an older link with the token', '#token=abc123', { token: 'abc123' }],
    ['a malformed escape is kept, not thrown', '#token=abc%E0%A4%A', { token: 'abc%E0%A4%A' }],
  ];
  for (const [name, hash, want] of cases) {
    await t.test(name, () => assert.deepEqual(credentialFromHash(hash), want));
  }
});
