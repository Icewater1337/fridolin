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

test('files and forwards answer in groups until turned off, and only off is written', () => {
  installDom();
  const switches = (el) => el.querySelectorAll('input').filter((i) => i.getAttribute('role') === 'switch');
  const on = routingEditor({ routing: { groups: [GROUP] } });
  assert.deepEqual(on.getValue(), { handles_dms: false, default: false, groups: [GROUP] });

  const off = routingEditor({ routing: { groups: [GROUP], answer_files: false, answer_forwards: false } });
  const [, , files, forwards] = switches(off);
  assert.equal(files.checked, false);
  assert.equal(forwards.checked, false);
  assert.deepEqual(off.getValue(), { handles_dms: false, default: false, groups: [GROUP], answer_files: false, answer_forwards: false });

  files.checked = true;
  assert.deepEqual(off.getValue(), { handles_dms: false, default: false, groups: [GROUP], answer_forwards: false });
});

// Another brain is named as in every list of brains: its name, with its id
// when that differs.
test('the editor names the brains that hold DMs, the default and a group', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  const groupsLoaded = loadGroups(true);
  net.answer(0, [{ jid: GROUP, name: 'Family', allowed: true }]);
  await groupsLoaded;
  const brains = [
    { id: 'work', name: 'Office', manifest: { routing: { handles_dms: true, groups: [GROUP] } } },
    { id: 'home', name: 'home', manifest: { routing: { default: true } } },
  ];
  const el = routingEditor({ routing: { handles_dms: true, default: true }, selfId: 'new', brains, allowedGroups: [GROUP] });
  document.body.append(el);
  const warnings = el.querySelectorAll('.warn-text').filter((w) => !w.hidden).map((w) => w.textContent);
  assert.deepEqual(warnings, [
    '“Office (work)” handles direct messages now. Only one brain can — turn it off there first, or saving will fail.',
    '“home” is the default now. Only one brain can be — turn it off there first, or saving will fail.',
  ]);
  assert.equal(el.querySelector('.group-pick-note').textContent, 'routed to Office (work)');
});
