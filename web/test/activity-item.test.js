import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { h } from '../assets/lib/dom.js';
import { activityItem, showActivity } from '../assets/components/activity-item.js';
import { setChatNames } from '../assets/components/chat.js';

/** @import { ActivityEntry } from '../types/api.js' */

const GROUP = '120363000000000000@g.us';
/** @type {(ts: string, text: string) => ActivityEntry} */
const msg = (ts, text) => ({ ts, chat: GROUP, type: 'in', from: '15551234567', text, brain: 'work' });
const long = 'line\n'.repeat(20);

test('a refresh keeps the rows of messages already shown', () => {
  installDom();
  const names = new Map();
  setChatNames((jid) => names.get(jid) || '');
  const feed = h('div');
  document.body.append(feed);
  const older = msg('2026-10-18T10:00:00Z', long);
  showActivity(feed, [older]);
  const row = /** @type {Element} */ (feed.firstChild);
  row.querySelector('button').click(); // unfold it
  assert.equal(row.querySelector('.msg-wrap').classList.contains('collapsed'), false);

  showActivity(feed, [msg('2026-10-18T10:05:00Z', 'new'), older]);
  assert.equal(feed.children.length, 2);
  assert.equal(feed.children[1], row, 'the same row, not a new one');
  assert.equal(row.querySelector('.msg-wrap').classList.contains('collapsed'), false, 'still unfolded');

  names.set(GROUP, 'Family');
  showActivity(feed, [older]);
  assert.notEqual(feed.firstChild, row, 'a learnt chat name draws the row again');
  assert.match(feed.firstChild.textContent, /Family/);

  showActivity(feed, [older], { showBrain: false });
  assert.doesNotMatch(feed.firstChild.textContent, /work/, 'other options draw it again too');
});

test('an event reads as what its person did, not as a message they sent', () => {
  installDom();
  setChatNames(() => '');
  const row = activityItem({ ts: '2026-10-18T10:00:00Z', chat: GROUP, type: 'event', from: '15551234567', text: 'deleted a message they had sent', brain: 'work' });
  assert.ok(row.classList.contains('event'), 'an event row');
  assert.ok(!row.classList.contains('in'), 'not a received message');
  assert.equal(row.querySelector('.act-icon').getAttribute('title'), 'Event');
  assert.equal(row.querySelector('.act-event').textContent, '+15551234567 deleted a message they had sent');

  const msgRow = activityItem(msg('2026-10-18T10:00:00Z', 'hello'));
  assert.equal(msgRow.querySelector('.act-icon').getAttribute('title'), 'Received');
});
