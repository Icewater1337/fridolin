// Build: chat with a builder brain about the site, next to its preview.
// The chat is a brain run like any other (sandbox, skills, tools), in a chat
// of its own; what it proposes waits under Site → Proposals.

import { get, post } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
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
    let shownCount = -1;
    let shownPreview = null; // the preview link shown, '' for none
    let draft = '';

    const messagesEl = h('div.build-messages', { role: 'log', 'aria-live': 'polite' });
    const statusEl = h('div.build-status');
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

    function renderMessages() {
      if (chat.messages.length === shownCount) return;
      shownCount = chat.messages.length;
      const atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 40;
      mount(messagesEl, chat.messages.length ? chat.messages.map((m) => h(`div.build-msg.${m.role}`,
        h('div.build-text', m.text), h('div.build-time', timeAgo(m.at))))
        : h('p.muted.build-empty', 'Nothing yet. Say what the site should have — the brain asks if it needs to know more.'));
      if (atBottom || shownCount <= 1) messagesEl.scrollTop = messagesEl.scrollHeight;
    }

    function renderStatus() {
      if (chat.running || chat.queued) {
        const last = [...chat.activity].reverse().find((l) => l.includes('🔧') || l.includes('💬')) || '';
        mount(statusEl,
          h('div.build-working', h('span.spinner'), h('span', chat.running ? 'Working' : 'Waiting its turn'),
            last ? h('span.muted.small.build-last', last.replace(/^\[[\d:]+\]\s*/, '')) : null),
          chat.running ? btn('Stop', { small: true, icon: 'x', onClick: async () => { await post(`/api/site/build/${encodeURIComponent(brainID)}/stop`); poller.kick(); } }) : null);
      } else {
        mount(statusEl);
      }
      mount(proposalEl, chat.proposal ? callout('info', `Proposed: ${chat.proposal.title}`,
        h('div', 'Waiting for you under ', h('a', { href: '#/site' }, 'Site → Proposals'), ' — ', timeAgo(chat.proposal.at))) : null);
    }

    function renderPreview() {
      const key = chat.preview ? chat.preview.local : '';
      if (key === shownPreview) return;
      shownPreview = key;
      if (!chat.preview) {
        mount(previewEl, h('div.build-preview-empty',
          h('p.muted', 'No preview yet. The brain starts one when it has something to show — or start it now.'),
          btn('Start a preview', { icon: 'play', onClick: startPreview })));
        return;
      }
      // A new frame each time: one whose first load failed (the preview was
      // restarting) can stay blank however often its src is set again.
      const makeFrame = () => h('iframe.build-frame', { src: chat.preview.local, title: 'Preview of the site' });
      let frame = makeFrame();
      mount(previewEl,
        h('div.build-preview-bar',
          pill('Preview', 'warn'),
          h('div.actions',
            btn('Reload', { small: true, icon: 'refresh', onClick: () => { const next = makeFrame(); frame.replaceWith(next); frame = next; } }),
            btn('Restart', { small: true, icon: 'play', onClick: startPreview }),
            h('a.btn.small', { href: chat.preview.local, target: '_blank', rel: 'noopener noreferrer' }, icon('external'), h('span.btn-label', 'Open')))),
        frame);
    }

    async function startPreview() {
      try {
        await post(`/api/site/previews/${encodeURIComponent(brainID)}`);
        shownPreview = null;
        poller.kick();
      } catch (e) { toast(e.message, 'bad'); }
    }

    function layout() {
      const picker = brains.length > 1 ? select(brains.map((b) => [b.id, b.name || b.id]), brainID) : null;
      if (picker) {
        picker.addEventListener('change', () => {
          brainID = picker.value;
          try { localStorage.setItem(STORE_KEY, brainID); } catch { /* ignore */ }
          shownCount = -1; shownPreview = null;
          poller.kick();
        });
      }
      mount(body, h('div.build-layout',
        h('section.card.build-chat',
          h('div.card-head', h('div.card-title', icon('chats'), h('h2', brains.length > 1 ? 'Chat with' : `Chat with ${brains[0].name || brains[0].id}`)), picker,
            btn('New conversation', {
              small: true, onClick: async () => {
                await post(`/api/site/build/${encodeURIComponent(brainID)}/new`);
                shownCount = -1; poller.kick();
              },
            })),
          messagesEl, statusEl, proposalEl,
          h('div.build-compose', input, h('div.actions', h('span.muted.small', '⌘↩ to send'), sendBtn))),
        h('section.card.build-side', previewEl)));
    }

    const poller = ctx.poll(async (signal) => {
      if (!brains.length) {
        const r = await get('/api/site/builders', { signal });
        brains = r.brains.filter((b) => b.builder);
        if (!brains.length) {
          mount(body, callout('info', 'No brain builds the site yet',
            h('div', 'Allow one under ', h('a', { href: '#/site' }, 'Site → Builders'), ' (connect the site’s repository first).')));
          return;
        }
        let saved = '';
        try { saved = localStorage.getItem(STORE_KEY) || ''; } catch { /* ignore */ }
        brainID = brains.some((b) => b.id === saved) ? saved : brains[0].id;
        layout();
      }
      chat = await get(`/api/site/build/${encodeURIComponent(brainID)}`, { signal });
      renderMessages(); renderStatus(); renderPreview();
    }, () => (chat && (chat.running || chat.queued) ? 1500 : 5000));
  },
};
