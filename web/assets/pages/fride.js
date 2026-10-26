// FrIDE: the coding work handed to FrIDE on the nodes, each thread with
// all FrIDE said about it. The chat gets a few lines of each piece of
// news, or none (fride.news); this page keeps it whole.

import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { plural } from '../lib/format.js';
import { empty, pageHeader, spinner, timeAgo } from '../components/common.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { FrideNewsItem, FrideThread } from '../../types/api.js' */

/** @param {FrideNewsItem} n */
function newsEl(n) {
  return h('li.fride-news',
    h('div.act-meta', h('span.badge', n.kind), h('span.act-time', timeAgo(n.at))),
    h('pre.fride-text', n.text));
}

/** @param {string} chat */
function chatName(chat) {
  return chat.startsWith('schedule:') ? `scheduled agent ${chat.slice('schedule:'.length)}` : chat;
}

/** @param {FrideThread} t */
function threadCard(t) {
  const where = [t.node, t.task && `task ${t.task}`, t.branch && `branch ${t.branch}`].filter(Boolean).join(' · ');
  return h('article.card', { id: `fride-${t.thread}` },
    h('div.act-meta.thread-head',
      h('strong', `FrIDE #${t.thread}`),
      h('span.act-sep', '·'),
      h('span.badge', t.fride_status || t.state),
      h('span.act-sep', '·'),
      h('span.muted', `${t.brain} → ${chatName(t.chat)}`),
      h('span.act-time', timeAgo(t.updated))),
    where ? h('div.muted.small', where) : null,
    h('details',
      h('summary', 'The work handed over'),
      h('pre.fride-text', t.message)),
    t.news.length
      ? h('details', { open: true },
        h('summary', plural(t.news.length, 'piece of news', 'pieces of news')),
        h('ol.fride-news-list', t.news.map(newsEl)))
      : h('p.muted.small', 'No news from FrIDE yet.'));
}

export default /** @satisfies {PageModule} */ ({
  title: 'FrIDE',
  render(root, ctx) {
    const box = h('div', spinner());
    const list = h('div.stack');
    mount(root,
      pageHeader('FrIDE', 'Coding work handed to FrIDE on the nodes, and all FrIDE said about each thread. The chat gets only a few lines of it.'),
      box);
    ctx.follow(resource('/api/fride'), (data) => {
      const threads = data?.threads || [];
      if (!threads.length) {
        paint(box, 'none', () => h('div.card', empty('No FrIDE work yet', 'Work a brain hands to FrIDE with fride_task is listed here, with FrIDE’s news.')));
        return;
      }
      paint(box, 'list', () => list);
      reconcile(list, threads, (t) => `${t.thread}|${t.updated}|${t.news.length}`, threadCard);
    }, 5000);
  },
});
