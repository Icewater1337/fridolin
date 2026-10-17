import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { routingEditor } from '../assets/components/routing-editor.js';
import { loadGroups, rememberName } from '../assets/app/store.js';

const GROUP = '120363000000000000@g.us';
const tick = () => new Promise((r) => setImmediate(r));
const names = (el) => el.querySelectorAll('.group-pick-name').map((n) => n.textContent);

test('an editor follows the groups while it is shown, and stops once replaced', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const groupsLoaded = loadGroups(true);
  net.answer(0, []); // the account's groups do not include it (yet)
  await groupsLoaded;

  const first = routingEditor({ routing: { groups: [GROUP] } });
  document.body.append(first);
  assert.deepEqual(names(first), ['Unnamed group']);

  rememberName(GROUP, 'Family');
  assert.deepEqual(names(first), ['Family'], 'repaints when the name arrives');

  // The form is rebuilt (after a save): the old editor leaves the page.
  first.remove();
  const second = routingEditor({ routing: { groups: [GROUP] } });
  document.body.append(second);
  const reloaded = loadGroups(true);
  net.answer(1, [{ jid: GROUP, name: 'Family and friends', allowed: true }]);
  await reloaded;
  await tick();
  assert.deepEqual(names(second), ['Family and friends']);
  assert.deepEqual(names(first), ['Family'], 'the replaced editor no longer paints');
});
