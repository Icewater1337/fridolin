import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import page from '../assets/pages/dev.js';

const toasts = (kind) => document.querySelectorAll(`.toast.${kind}`).map((el) => el.textContent);

// The Dev page with the bridge answering a message sent with inbound, as
// /api/dev/inbound does: 202 also for a message it ignores.
async function openDev(t, inbound = { status: 'accepted' }) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, (path) => {
    if (path === '/api/dev/inbound') return { status: 202, body: inbound };
    if (path.startsWith('/api/dev/outbox')) return [];
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage();
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const chip = (name) => root.querySelectorAll('button.chip').find((b) => b.textContent === name);
  const [jidIn, senderIn] = root.querySelectorAll('form input');
  const textIn = root.querySelector('textarea');
  const send = async (text) => {
    type(textIn, text);
    root.querySelector('form').dispatchEvent(new Event('submit', { cancelable: true }));
    await settle();
  };
  return { calls, chip, jidIn, senderIn, textIn, send };
}

// The bridge answers 202 to a message it turns away, as it would the same
// message from WhatsApp: the page says so, and why, and keeps the text.
test('a message the bridge ignores is not reported as sent', async (t) => {
  const cases = [
    { name: 'accepted', inbound: { status: 'accepted' }, ok: ['Message injected'], warn: [], text: '' },
    { name: 'not admitted by group_members', inbound: { status: 'ignored', reason: 'sender not admitted by group_members' },
      ok: [], warn: ['Ignored: sender not admitted by group_members'], text: '@claude hello' },
    { name: 'a group not allowed', inbound: { status: 'ignored', reason: 'group not allowed' },
      ok: [], warn: ['Ignored: group not allowed'], text: '@claude hello' },
  ];
  for (const c of cases) {
    await t.test(c.name, async (t) => {
      const dev = await openDev(t, c.inbound);
      dev.chip('Group').click();
      await dev.send('@claude hello');
      const sent = dev.calls.filter((r) => r.path === '/api/dev/inbound');
      assert.equal(sent.length, 1);
      assert.equal(sent[0].body.jid, '120363000000000000@g.us');
      assert.deepEqual(toasts('ok'), c.ok);
      assert.deepEqual(toasts('warn'), c.warn);
      assert.equal(dev.textIn.value, c.text);
    });
  }
});

// Left empty, the sender of a group message is no number, which only a
// group open to every member hears: in a group the hint asks for a member.
test('the sender hint follows the chat', async (t) => {
  const dev = await openDev(t);
  const hint = () => dev.senderIn.getAttribute('placeholder');
  assert.equal(hint(), 'defaults to the number in the JID');
  dev.chip('Group').click();
  assert.equal(hint(), 'the member’s number, one the group admits');
  dev.chip('Direct message').click();
  assert.equal(hint(), 'defaults to the number in the JID');
  type(dev.jidIn, '120363000000000001@g.us');
  assert.equal(hint(), 'the member’s number, one the group admits', 'a group typed in');
});
