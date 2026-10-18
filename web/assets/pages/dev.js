// Dev mode (--no-whatsapp): inject messages and read what the bridge "sent".

import { get, post } from '../app/api.js';
import { store } from '../app/store.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { toast } from '../components/toast.js';
import { btn, runBusy } from '../components/buttons.js';
import { field, textInput, toggle } from '../components/fields.js';
import { timeAgo, empty, pageHeader, callout, longText } from '../components/common.js';
import { chatEl, chatName } from '../components/chat.js';

const DM = '15551234567@s.whatsapp.net';
const GROUP = '120363000000000000@g.us';

export default {
  title: 'Dev',
  render(root, ctx) {
    const notice = h('div');
    const outbox = h('div');
    const sent = h('div.outbox');

    const jidIn = textInput({ value: DM, class: 'mono', required: true });
    const senderIn = textInput({ placeholder: 'defaults to the number in the JID', class: 'mono' });
    const brainIn = textInput({ placeholder: 'the brain that owns the chat', class: 'mono' });
    const textIn = h('textarea', { rows: 4, placeholder: 'Message text — in a group, include a trigger such as @claude to get a reply' });
    const silentSw = toggle({ label: 'Silent — like an untagged group message: it goes into the brain’s history, gets a run only if the brain runs on untagged messages, and no reply' });
    const sendBtn = btn('Send to the bridge', { kind: 'primary', icon: 'send', type: 'submit' });
    const form = h('form.card.form',
      h('div.form-grid',
        field({ label: 'Chat JID', control: h('div.stack', jidIn, h('div.chip-row',
          h('button.chip', { type: 'button', onclick: () => { jidIn.value = DM; } }, 'Direct message'),
          h('button.chip', { type: 'button', onclick: () => { jidIn.value = GROUP; } }, 'Group'))),
        help: 'The chat the message appears to come from. Dev messages skip the allow lists and trigger words, so every one reaches the brain that owns the chat, or the Brain below.' }),
        field({ label: 'Sender', control: senderIn, help: 'Phone number of the person writing, digits only. In a group this is the member.' }),
        field({ label: 'Brain', control: brainIn, help: 'Optional brain id: the message runs there and the reply goes to the chat above, like a file handed in through /ingest. Commands such as !stop are then passed on as text.' })),
      field({ label: 'Message', control: textIn, wide: true }),
      h('div.field', silentSw),
      h('div.actions', sendBtn, h('span.muted.small', 'Ctrl/⌘ + Enter sends')));

    const send = () => runBusy(sendBtn, async () => {
      const jid = jidIn.value.trim();
      if (!jid) { jidIn.focus(); return; }
      await post('/api/dev/inbound', { jid, sender: senderIn.value.trim(), brain: brainIn.value.trim(), text: textIn.value, silent: silentSw.input.checked });
      toast('Message injected', 'ok', { timeout: 2000 });
      textIn.value = '';
      textIn.focus();
      outboxPoll.kick();
    });
    form.addEventListener('submit', (e) => { e.preventDefault(); send(); });
    textIn.addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); send(); } });

    mount(root,
      pageHeader('Dev', 'Simulate WhatsApp: messages you send here go through the same pipeline as real ones, and replies land in the outbox below.'),
      notice,
      h('section.section', h('h2.section-title', 'Send a message'), form),
      h('section.section', h('div.section-head', h('h2.section-title', 'Outbox'), h('span.muted.small', 'What the bridge would have sent, newest first')), outbox));

    ctx.watch(store, (s) => s.status, (s) => {
      if (!s) return;
      paint(notice, !!s.dev_mode, (dev) => (dev ? null : callout('warn', 'Dev mode is off', 'This page only works when the bridge was started with --no-whatsapp.')));
    });

    let items = null;
    const paintOutbox = () => {
      if (!items) return;
      if (!items.length) { paint(outbox, 'none', () => h('div.card', empty('Nothing sent yet', 'Replies and polls the bridge sends appear here.'))); return; }
      paint(outbox, 'list', () => sent);
      // A message keeps its card, so one that was unfolded stays unfolded.
      reconcile(sent, items.map((m) => ({ ...m, chat_name: chatName(m.jid) })), (m) => `${m.ts}|${m.jid}|${(m.text || '').length}`, (m) => h('article.card.outbox-item',
        h('div.act-meta', h('span.act-who', m.poll ? 'Poll' : 'Message'), h('span.act-sep', 'to'), chatEl(m.jid), h('span.act-time', timeAgo(m.ts))),
        longText(m.text, { limit: 600, lines: 10 }),
        m.poll ? h('ul.poll-options', m.poll.map((o) => h('li', o))) : null));
    };
    const outboxPoll = ctx.poll(async (signal) => {
      try {
        items = await get('/api/dev/outbox', { signal });
      } catch (e) {
        if (e.status === 404) { paint(outbox, 'unavailable', () => empty('Not available', 'Only in dev mode: start the bridge with --no-whatsapp.')); return; }
        throw e;
      }
      paintOutbox();
    }, 2000);
    // Chat labels show group names once the groups have loaded.
    ctx.watch(store, (s) => s.names, paintOutbox);
  },
};
