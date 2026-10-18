import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { setStatus } from '../assets/app/store.js';
import page from '../assets/pages/overview.js';

async function openOverview(t, status) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  serve(t, (path) => (path.startsWith('/api/activity') ? [] : {}));
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => { ctx.destroy(); setStatus(null); });
  setStatus({ whatsapp: { state: 'connected' }, brains: [], issues: [], warnings: [], claude: {}, sandbox: {}, ...status });
  page.render(root, ctx);
  await settle();
  return root;
}

test('daily backups without a Drive link say so instead of "not set up"', async (t) => {
  const root = await openOverview(t, { backup: { connected: false, enabled: true } });
  const text = root.textContent;
  assert.ok(text.includes('Not connected'), text);
  assert.ok(text.includes('Daily backups are on, but Google Drive is not connected'), text);
  assert.ok(!text.includes('Not set up'), text);
});

test('backups that were never set up are offered', async (t) => {
  const root = await openOverview(t, { backup: { connected: false, enabled: false } });
  const text = root.textContent;
  assert.ok(text.includes('Not set up'), text);
  assert.ok(!text.includes('Daily backups are on'), text);
});

for (const [job, want] of [[{}, 'Backing up…'], [{ restoring: true }, 'Restoring…']]) {
  test(`a running job reads "${want}"`, async (t) => {
    const root = await openOverview(t, { backup: { connected: true, enabled: true, running: true, ...job } });
    assert.ok(root.textContent.includes(want), root.textContent);
  });
}

test('a failed backup says whose it was', async (t) => {
  const root = await openOverview(t, {
    brains: [{ id: 'notes', name: 'Notes', routing: {} }],
    backup: { connected: true, enabled: true, last_error: 'quota exceeded', last_error_brain: 'notes' },
  });
  assert.ok(root.textContent.includes('Notes: quota exceeded'), root.textContent);
});
