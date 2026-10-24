import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/logs.js';

// A bridge's log ring: lines numbered from 1 per process, as LogRing.Since
// answers (next is the cursor when nothing is newer).
function ring(boot, texts) {
  const lines = texts.map((text, i) => ({ seq: i + 1, ts: '2026-10-18T10:00:00Z', text }));
  return {
    boot,
    since(after) {
      const out = lines.filter((l) => l.seq > after);
      return { lines: out, next: out.length ? out.at(-1).seq : after, boot };
    },
  };
}

async function openPage(t) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const bridge = { ring: ring(100, ['started', 'brain work: error for 15551234567@s.whatsapp.net: exit status 1', 'third']) };
  const calls = serve(t, (path) => {
    const after = Number(new URL(path, 'http://x').searchParams.get('after') || 0);
    return bridge.ring.since(after);
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const texts = () => [...root.querySelector('.log-view').children].map((el) => (el.classList.contains('log-sep') ? '---' : el.lastChild.textContent));
  const poll = async () => { t.mock.timers.tick(2000); await settle(); };
  return { root, bridge, calls, texts, poll, count: () => root.querySelector('.toolbar span.muted').textContent };
}

test('a restart is noticed from the log answer itself', async (t) => {
  /** @type {[string, string[]][]} */
  const cases = [
    // The new process has logged more lines than the old cursor, or fewer.
    ['more lines than before', ['new 1', 'new 2', 'new 3', 'new 4', 'new 5']],
    ['fewer lines than before', ['new 1']],
  ];
  for (const [name, fresh] of cases) {
    await t.test(name, async (t) => {
      const { bridge, calls, texts, poll, count } = await openPage(t);
      assert.equal(texts().length, 3);
      bridge.ring = ring(200, fresh);
      await poll();
      assert.deepEqual(texts(), ['started', 'brain work: error for 15551234567@s.whatsapp.net: exit status 1', 'third', '---', ...fresh], 'every new line once, after one note');
      assert.equal(new URL(calls.at(-1).path, 'http://x').searchParams.get('after'), '0', 'the new process is read from its first line');
      assert.equal(count(), `${3 + fresh.length} lines`, 'the note is not a line');
      await poll();
      assert.equal(texts().length, 4 + fresh.length, 'and nothing twice afterwards');
    });
  }
});

test('the filter looks at the text shown, and the level at the line itself', async (t) => {
  const { root, count } = await openPage(t);
  const lines = root.querySelectorAll('.log-line');
  assert.deepEqual(lines.map((l) => l.classList.contains('err')), [false, true, false]);
  type(root.querySelector('input'), 'THIRD');
  t.mock.timers.tick(150);
  assert.deepEqual(lines.map((l) => l.hidden), [true, true, false]);
  assert.equal(count(), '1 of 3 lines');
});
