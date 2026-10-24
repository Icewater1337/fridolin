import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { brainSettingsFields } from '../assets/components/brain-fields.js';
import { DEFAULT_BACKUP_EXCLUDES } from '../assets/lib/brain.js';
import page from '../assets/pages/add-brain.js';

test('the wizard asks before leaving only once something was edited', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  Object.assign(globalThis, { window: { scrollTo() {} } });
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
  Object.assign(globalThis, { window: { scrollTo() {} } });
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

test('a folder that has a brain.json shows what it asks for', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  Object.assign(globalThis, { window: { scrollTo() {} } });
  t.after(() => { delete globalThis.window; });
  const manifest = { version: 1, id: 'notes', name: 'Notes', routing: { handles_dms: false, default: false, groups: ['120363000000000000@g.us'] }, model: 'opus' };
  serve(t, (path) => {
    if (path.startsWith('/api/fs/capabilities')) return { picker: '', home: '/Users/you' };
    if (path.startsWith('/api/fs/list')) return { path: '/Users/you', home: '/Users/you', parent: '', entries: [] };
    if (path.startsWith('/api/brains/inspect')) return { exists: true, is_dir: true, entries: 2, abs_path: '/Users/you/notes', manifest };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  root.querySelectorAll('button').find((b) => b.textContent === 'Type a path').click();
  const typed = root.querySelector('form.inline-add');
  type(typed.querySelector('input'), '~/notes');
  typed.dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();

  const note = root.querySelector('.callout.info');
  assert.ok(note.querySelector('.callout-icon'));
  assert.equal(note.querySelector('.callout-body strong').textContent, 'This folder already has a brain.json');
  assert.match(note.querySelector('dl.kv').textContent, /NameNotes.*Groups120363000000000000@g\.us.*Modelopus/);
  assert.equal(note.querySelector('details summary').textContent, 'Show brain.json');
});

const helps = (els) => [els].flat().flatMap((el) => el.querySelectorAll('.help').map((x) => x.textContent));
const fieldset = (root, legend) => root.querySelectorAll('fieldset').find((f) => f.querySelector('legend').textContent === legend);

test('the configure step adds the brain with the model, effort and backup the brain page shows', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  Object.assign(globalThis, { window: { scrollTo() {} } });
  t.after(() => { delete globalThis.window; });
  const calls = serve(t, (path, init) => {
    if (path.startsWith('/api/fs/capabilities')) return { picker: '', home: '/Users/you' };
    if (path.startsWith('/api/fs/list')) return { path: '/Users/you', home: '/Users/you', parent: '', entries: [] };
    if (path.startsWith('/api/brains/inspect')) return { exists: false, abs_path: '/Users/you/notes', suggested: { name: 'Notes', id: 'notes' } };
    if (path === '/api/brains' && init.method === 'POST') return { id: 'notes', path: '/Users/you/notes', created: [] };
    if (path === '/api/brains') return [];
    if (path.startsWith('/api/config')) return { config: {}, effective: { brain_model: 'sonnet', brain_effort: 'high' } };
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
  button('Continue').click();
  await settle();

  const shared = brainSettingsFields();
  const model = fieldset(root, 'Model');
  assert.deepEqual(helps(model), helps(shared.model), 'the model fields are the shared ones');
  assert.deepEqual(helps(fieldset(root, 'Backup')), helps(shared.backup), 'and so are the backup fields');
  const modelIn = model.querySelector('input');
  assert.equal(modelIn.getAttribute('placeholder'), 'inherit (sonnet)');
  const effort = model.querySelector('select');
  assert.equal(effort.querySelector('option').textContent, 'Inherit (high)');
  const backup = fieldset(root, 'Backup').querySelector('input[type=checkbox]');
  assert.equal(backup.checked, true, 'a new brain is backed up');

  type(modelIn, 'opus');
  effort.value = 'max';
  effort.dispatchEvent(new Event('change', { bubbles: true }));
  backup.checked = false;
  backup.dispatchEvent(new Event('change', { bubbles: true }));
  root.querySelector('form.wizard-card').dispatchEvent(new Event('submit', { cancelable: true }));
  await settle();
  const added = calls.find((c) => c.path === '/api/brains' && c.method === 'POST');
  assert.ok(added, 'the brain was added');
  const { model: m, effort: e, backup: b } = added.body.manifest;
  assert.deepEqual({ m, e, b }, { m: 'opus', e: 'max', b: { enabled: false, exclude: DEFAULT_BACKUP_EXCLUDES } });
});
