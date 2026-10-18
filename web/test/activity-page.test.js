import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/activity.js';

const msg = (text) => ({ ts: '2026-10-18T10:00:00Z', brain: 'work', chat: '15551234567@s.whatsapp.net', from: '15551234567', type: 'in', text });

// The Activity page polls the feed every 3 s. The stub answers as the
// bridge does: the feed with its ETag, or a 304 when the page sends that
// ETag back.
test('the Activity page downloads the feed again only when it changed', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { feed: [msg('hello')], etag: '"a1"' };
  const calls = serve(t, (path, init) => {
    if (!path.startsWith('/api/activity')) return {};
    if (init.headers['If-None-Match'] === bridge.etag) return { status: 304, headers: { ETag: bridge.etag } };
    return { status: 200, body: bridge.feed, headers: { ETag: bridge.etag } };
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage({ params: {} });
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const feed = () => calls.filter((c) => c.path.startsWith('/api/activity'));
  const poll = async () => { t.mock.timers.tick(3000); await settle(); };

  const row = root.querySelector('.act-item');
  assert.match(root.textContent, /hello/);
  await poll();
  await poll();
  assert.deepEqual(feed().map((c) => c.headers['If-None-Match']), [undefined, '"a1"', '"a1"'], 'each poll sends the ETag back');
  assert.equal(root.querySelector('.act-item'), row, 'and the 304s leave the list as it was');

  bridge.feed = [msg('again'), msg('hello')];
  bridge.etag = '"a2"';
  await poll();
  assert.match(root.textContent, /again/, 'a changed feed comes in');
  await poll();
  assert.equal(feed().at(-1).headers['If-None-Match'], '"a2"');
});
