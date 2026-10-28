// FrIDE: the coding work handed to FrIDE on the nodes, each thread with
// all FrIDE said about it. The chat gets a few lines of each piece of
// news, or none (fride.news); this page keeps it whole. Above it, the
// FrIDE instances of fride.instances (each a whole FrIDE in a Docker
// Sandbox on a node, for one trust domain): where each runs, and Start
// and Stop for them, which the core brain otherwise does by itself.

import { resource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { plural } from '../lib/format.js';
import { INSTANCE_SIZES, idleStopText, memText } from '../lib/fride.js';
import { btn } from '../components/buttons.js';
import { select } from '../components/fields.js';
import { instanceBadge, startInstance, stopInstance } from '../components/fride-instance.js';
import { empty, pageHeader, spinner, timeAgo } from '../components/common.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { Child } from '../lib/dom.js' */
/** @import { FrideInstanceView, FrideNewsItem, FrideThread } from '../../types/api.js' */

const frideResource = resource('/api/fride');

/** The sizes to start an instance at; "" leaves it to the core. */
const SIZE_OPTIONS = /** @type {[string, string][]} */ ([
  ['', 'Default size'],
  ...INSTANCE_SIZES.map(([size, mb]) => [size, `${size[0].toUpperCase()}${size.slice(1)}, ${memText(mb)}`]),
]);

/** @param {string} name */
async function stop(name) {
  if (await stopInstance(name)) await frideResource.refresh();
}

/**
 * @param {string} name
 * @param {string} size
 */
async function start(name, size) {
  await startInstance(name, size);
  await frideResource.refresh();
}

/**
 * Where an instance runs or may run, its memory, open threads and when
 * it stops by itself.
 * @param {FrideInstanceView} i
 */
function instanceFacts(i) {
  const nodes = i.nodes || [];
  const where = i.node ? `on ${i.node}` : `may run on ${nodes.length ? nodes.join(', ') : 'any node that offers fride_host'}`;
  const memory = i.node && i.running_memory_mb ? memText(i.running_memory_mb)
    : i.memory_mb ? `${memText(i.memory_mb)}, fixed` : 'sized at each start';
  return [where, memory, plural(i.open_threads || 0, 'open thread'), idleStopText(i.idle_stop_minutes)].join(' · ');
}

/** @param {FrideInstanceView} i */
function instanceRow(i) {
  /** @type {Child} */
  let actions = null;
  if (i.state === 'running') {
    actions = btn('Stop', { small: true, kind: 'danger', icon: 'stop', title: `Stop ${i.name}`, onClick: () => stop(i.name) });
  } else if (i.state !== 'starting') {
    // A fixed memory_mb is the size: the core takes it over any asked for.
    const size = i.memory_mb ? null : select(SIZE_OPTIONS, '', { 'aria-label': `Size to start ${i.name} at` });
    actions = [size, btn('Start', { small: true, kind: 'primary', icon: 'play', title: `Start ${i.name}`, onClick: () => start(i.name, size ? size.value : '') })];
  }
  return h('li.fride-instance',
    h('div.fride-instance-main',
      h('div.act-meta', h('strong', h('code.mono', i.name)), instanceBadge(i.state)),
      i.description ? h('div', i.description) : null,
      h('div.muted.small', instanceFacts(i)),
      i.problem ? h('div.warn-text.small', i.problem) : null),
    actions ? h('div.actions', actions) : null);
}

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
  const where = [t.node, t.instance && `instance ${t.instance}`, t.task && `task ${t.task}`, t.branch && `branch ${t.branch}`].filter(Boolean).join(' · ');
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
    const instancesBox = h('div');
    const instances = h('ul.plain-list.fride-instances');
    const box = h('div', spinner());
    const list = h('div.stack');
    mount(root,
      pageHeader('FrIDE', 'Coding work handed to FrIDE on the nodes, and all FrIDE said about each thread. The chat gets only a few lines of it.'),
      instancesBox,
      box);
    ctx.follow(frideResource, (data) => {
      const configured = data?.instances || [];
      if (configured.length) {
        paint(instancesBox, 'list', () => h('section.card.fride-instances-card',
          h('h2', 'Instances'),
          h('p.muted.small', 'Each a whole FrIDE in a Docker Sandbox on a node, for one trust domain. The core brain starts one when a job needs it and stops it once it idles; they are configured under ', h('a', { href: '#/settings/fride' }, 'Settings'), '.'),
          instances));
        reconcile(instances, configured, (i) => i.name, instanceRow);
      } else {
        paint(instancesBox, 'none', () => null);
      }
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
