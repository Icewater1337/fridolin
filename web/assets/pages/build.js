// Build: chat with a builder brain about the site, next to its preview.
// The chat is a brain run like any other (sandbox, skills, tools), in a chat
// of its own; what it proposes waits under Site → Proposals.

import { get, post } from '../app/api.js';
import { h, mount, paint } from '../lib/dom.js';
import { brainLabel } from '../lib/brain.js';
import { icon } from '../components/icons.js';
import { toast } from '../components/toast.js';
import { btn } from '../components/buttons.js';
import { select } from '../components/fields.js';
import { pill, callout, pageHeader, loading, timeAgo } from '../components/common.js';

const STORE_KEY = 'bridgeBuildBrain';

export default {
  title: 'Build',
  render(root, ctx) {
    const body = h('div', loading('Loading…'));
    mount(root, pageHeader('Build', 'Ask a builder brain for a page or a change. It builds and previews it in its sandbox and proposes it; you approve it under Site.'), body);

    let brains = [];
    let brainID = '';
    let chat = null;
    let draft = '';

    // Each part repaints only when what it shows changed, so the Stop and
    // Reload buttons are not replaced under the pointer, and a preview
    // keeps its frame until it is another one (a restart gets a new
    // address).
    const messagesEl = h('div.build-messages', { role: 'log', 'aria-live': 'polite' });
    // The working line changes with every tool the brain uses; the Stop
    // button next to it only when a run starts or ends.
    const workingEl = h('div.region');
    const stopEl = h('div.region');
    const statusEl = h('div.build-status', { hidden: true }, workingEl, stopEl);
    const input = h('textarea.build-input', { rows: 3, placeholder: 'e.g. “Add a page where members see this month’s events”' });
    input.addEventListener('input', () => { draft = input.value; });
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); send(); }
    });
    const sendBtn = btn('Send', { kind: 'primary', icon: 'send', onClick: () => send() });
    const previewEl = h('div.build-preview');
    const proposalEl = h('div');
    ctx.dirty = () => draft.trim() !== '';

    async function send() {
      const text = input.value.trim();
      if (!text) return;
      sendBtn.disabled = true;
      try {
        await post(`/api/site/build/${encodeURIComponent(brainID)}`, { text }, { signal: ctx.signal });
        input.value = ''; draft = '';
        poller.kick();
      } catch (e) {
        toast(e.message, 'bad');
      } finally {
        sendBtn.disabled = false;
      }
    }

    function paintChat() {
      // The newest message comes into view, unless you scrolled up to read.
      const atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 40;
      if (paint(messagesEl, chat.messages, renderMessages) && (atBottom || chat.messages.length <= 1)) messagesEl.scrollTop = messagesEl.scrollHeight;
      const busy = !!(chat.running || chat.queued);
      const last = busy ? [...chat.activity].reverse().find((l) => l.includes('🔧') || l.includes('💬')) || '' : '';
      statusEl.hidden = !busy;
      paint(workingEl, { busy, running: !!chat.running, last: last.replace(/^\[[\d:]+\]\s*/, '') }, renderWorking);
      paint(stopEl, !!chat.running, renderStop);
      paint(proposalEl, chat.proposal || null, renderProposal);
      paint(previewEl, chat.preview || null, renderPreview);
    }

    function renderMessages(messages) {
      return messages.length ? messages.map((m) => h(`div.build-msg.${m.role}`,
        h('div.build-text', m.text), h('div.build-time', timeAgo(m.at))))
        : h('p.muted.build-empty', 'Nothing yet. Say what the site should have — the brain asks if it needs to know more.');
    }

    function renderWorking({ busy, running, last }) {
      return busy ? h('div.build-working', h('span.spinner'), h('span', running ? 'Working' : 'Waiting its turn'),
        last ? h('span.muted.small.build-last', last) : null) : null;
    }

    function renderStop(running) {
      return running ? btn('Stop', { small: true, icon: 'x', onClick: async () => { await post(`/api/site/build/${encodeURIComponent(brainID)}/stop`); poller.kick(); } }) : null;
    }

    function renderProposal(p) {
      return p ? callout('info', `Proposed: ${p.title}`,
        h('div', 'Waiting for you under ', h('a', { href: '#/site' }, 'Site → Proposals'), ' — ', timeAgo(p.at))) : null;
    }

    function renderPreview(preview) {
      if (!preview) {
        return h('div.build-preview-empty',
          h('p.muted', 'No preview yet. The brain starts one when it has something to show — or start it now.'),
          btn('Start a preview', { icon: 'play', onClick: startPreview }));
      }
      // A new frame each time: one whose first load failed (the preview was
      // restarting) can stay blank however often its src is set again.
      const makeFrame = () => h('iframe.build-frame', { src: preview.local, title: 'Preview of the site' });
      let frame = makeFrame();
      return [
        h('div.build-preview-bar',
          pill('Preview', 'warn'),
          h('div.actions',
            btn('Reload', { small: true, icon: 'refresh', onClick: () => { const next = makeFrame(); frame.replaceWith(next); frame = next; } }),
            btn('Restart', { small: true, icon: 'play', onClick: startPreview }),
            h('a.btn.small', { href: preview.local, target: '_blank', rel: 'noopener noreferrer' }, icon('external'), h('span.btn-label', 'Open')))),
        frame,
      ];
    }

    async function startPreview() {
      try {
        await post(`/api/site/previews/${encodeURIComponent(brainID)}`);
        poller.kick();
      } catch (e) { toast(e.message, 'bad'); }
    }

    function renderLayout() {
      const picker = brains.length > 1 ? select(brains.map((b) => [b.id, brainLabel(b)]), brainID) : null;
      if (picker) {
        picker.addEventListener('change', () => {
          brainID = picker.value;
          try { localStorage.setItem(STORE_KEY, brainID); } catch { /* ignore */ }
          poller.kick();
        });
      }
      return h('div.build-layout',
        h('section.card.build-chat',
          h('div.card-head', h('div.card-title', icon('chats'), h('h2', brains.length > 1 ? 'Chat with' : `Chat with ${brains[0].name || brains[0].id}`)), picker,
            btn('New conversation', {
              small: true, onClick: async () => {
                await post(`/api/site/build/${encodeURIComponent(brainID)}/new`);
                poller.kick();
              },
            })),
          messagesEl, statusEl, proposalEl,
          h('div.build-compose', input, h('div.actions', h('span.muted.small', '⌘↩ to send'), sendBtn))),
        h('section.card.build-side', previewEl));
    }

    const poller = ctx.poll(async (signal) => {
      if (!brains.length) {
        const r = await get('/api/site/builders', { signal });
        brains = r.brains.filter((b) => b.builder);
        if (!brains.length) {
          paint(body, 'no builder', () => callout('info', 'No brain builds the site yet',
            h('div', 'Allow one under ', h('a', { href: '#/site' }, 'Site → Builders'), ' (connect the site’s repository first).')));
          return;
        }
        let saved = '';
        try { saved = localStorage.getItem(STORE_KEY) || ''; } catch { /* ignore */ }
        brainID = brains.some((b) => b.id === saved) ? saved : brains[0].id;
        paint(body, 'chat', renderLayout);
      }
      chat = await get(`/api/site/build/${encodeURIComponent(brainID)}`, { signal });
      paintChat();
    }, () => (chat && (chat.running || chat.queued) ? 1500 : 5000));
  },
};
