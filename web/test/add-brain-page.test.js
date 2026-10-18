import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/add-brain.js';

test('the wizard asks before leaving only once something was edited', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  globalThis.window = { scrollTo() {} };
  t.after(() => { delete globalThis.window; });
  serve(t, (path) => {
    if (path.startsWith('/api/fs/capabilities')) return { picker: '', home: '/Users/you' };
    if (path.startsWith('/api/fs/list')) return { path: '/Users/you', home: '/Users/you', parent: '', entries: [] };
    if (path.startsWith('/api/brains/inspect')) return { exists: false, abs_path: '/Users/you/notes', suggested: { name: 'Notes', id: 'notes' } };
    if (path === '/api/brains') return [];
    if (path.startsWith('/api/config')) return { config: {}, effective: {} };
    if (path.startsWith('/api/whatsapp/groups')) return [];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const button = (label) => root.querySelectorAll('button').find((b) => b.textContent === label);

  button('Type a path').click();
  const typed = root.querySelector('form.inline-add');
  type(typed.querySelector('input'), '~/notes');
  typed.dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  assert.equal(ctx.dirty(), false, 'a folder picked is nothing to lose');
  button('Continue').click();
  await settle();

  const nameIn = root.querySelector('form.wizard-card input');
  assert.equal(nameIn.value, 'Notes');
  assert.equal(ctx.dirty(), false, 'the configure step as suggested is not dirty');
  type(nameIn, 'Journal');
  assert.equal(ctx.dirty(), true);
  type(nameIn, 'Notes');
  assert.equal(ctx.dirty(), false, 'typed back to the suggestion');
  type(nameIn, 'Journal');
  button('Back').click();
  await settle();
  assert.equal(ctx.dirty(), false, 'the step that had the edits is gone');
});

test('the folder step chooses between browsing and typing with real tabs', async (t) => {
  installDom();
  noConsoleErrors(t);
  globalThis.window = { scrollTo() {} };
  t.after(() => { delete globalThis.window; });
  serve(t, (path) => {
    if (path.startsWith('/api/fs/capabilities')) return { picker: '', home: '/Users/you' };
    if (path.startsWith('/api/fs/list')) return { path: '/Users/you', home: '/Users/you', parent: '', entries: [] };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const list = root.querySelector('[role=tablist]');
  const panel = root.querySelector('[role=tabpanel]');
  assert.ok(list && panel);
  const [browse, typeTab] = list.querySelectorAll('[role=tab]');
  assert.equal(browse.getAttribute('aria-selected'), 'true');
  assert.equal(typeTab.getAttribute('aria-controls'), panel.id);
  assert.ok(panel.querySelector('.folder-browser'), 'the browser is the panel');
  browse.dispatchEvent(Object.assign(new Event('keydown', { cancelable: true }), { key: 'ArrowRight' }));
  assert.equal(document.activeElement, typeTab);
  assert.equal(panel.getAttribute('aria-labelledby'), typeTab.id);
  assert.ok(panel.querySelector('form.inline-add input'), 'the path field is the panel now');
});
