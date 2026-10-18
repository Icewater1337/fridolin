import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { restartBridge } from '../assets/app/restart.js';
import { store, setStatus } from '../assets/app/store.js';
import { closeAllDialogs } from '../assets/components/dialog.js';

// setImmediate, because the tests mock setTimeout (the waits between checks).
const tick = () => new Promise((r) => setImmediate(r));

// Pages load their modules on first visit, so a tab that saw the bridge
// come back as another version reloads instead of mixing the two builds.
test('restarting reloads the page when the version changed', async (t) => {
  const cases = [
    ['same version', '1.2.3', false],
    ['new version', '1.3.0', true],
    ['the new process reports none', '', false],
  ];
  for (const [name, version, reloaded] of cases) {
    await t.test(name, async (t) => {
      t.mock.timers.enable({ apis: ['setTimeout'] });
      const document = installDom();
      const net = stubFetch(t);
      setStatus({ started_at: '2026-10-18T09:00:00Z', version: '1.2.3', restart_required: ['log_level'] });
      let reloads = 0;
      const done = restartBridge({ reload: () => { reloads++; } });
      assert.equal(net.calls[0].url, '/api/restart');
      net.answer(0, { status: 'restarting' });
      await tick();
      t.mock.timers.tick(700);
      await tick();
      assert.equal(net.calls[1].url, '/api/status');
      const fresh = { started_at: '2026-10-18T09:01:00Z', version, restart_required: [] };
      net.answer(1, fresh);
      assert.equal(await done, true);
      assert.equal(reloads, reloaded ? 1 : 0);
      assert.equal(document.querySelector('dialog'), null, 'the restart dialog is gone');
      if (!reloaded) assert.deepEqual(store.get().status, fresh, 'the new process has nothing waiting for a restart');
    });
  }
});

const button = (dialog, label) => dialog.querySelectorAll('button').find((b) => b.textContent === label);

// The restart dialog is the app's: going back a page while the bridge
// restarts leaves it up, and nothing closes it but the restart's outcome.
test('the restart dialog stays until the bridge is back', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const document = installDom();
  const net = stubFetch(t);
  setStatus({ started_at: '2026-10-18T09:00:00Z', version: '1.2.3', restart_required: [] });
  const done = restartBridge({ reload: () => {} });
  const dialog = document.querySelector('dialog');
  assert.equal(dialog.open, true);
  assert.equal(dialog.getAttribute('aria-label'), 'Restarting');
  assert.match(dialog.textContent, /Restarting the bridge… this usually takes a few seconds\./);
  assert.equal(dialog.querySelector('.dialog-close'), null, 'no close button');
  assert.equal(dialog.querySelectorAll('button').length, 0, 'and nothing to click');
  const cancel = new Event('cancel', { cancelable: true });
  dialog.dispatchEvent(cancel);
  assert.equal(cancel.defaultPrevented, true, 'Escape does not close it');
  closeAllDialogs(); // the page changed
  assert.equal(document.querySelector('dialog'), dialog, 'a page change leaves it open');

  net.answer(0, { status: 'restarting' });
  await tick();
  t.mock.timers.tick(700);
  await tick();
  net.answer(1, { started_at: '2026-10-18T09:01:00Z', version: '1.2.3', restart_required: [] });
  assert.equal(await done, true);
  assert.equal(document.querySelector('dialog'), null);
});

test('a bridge that does not come back offers to close the dialog or reload', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  const document = installDom();
  const net = stubFetch(t);
  const before = { started_at: '2026-10-18T09:00:00Z', version: '1.2.3', restart_required: [] };
  setStatus(before);
  let reloads = 0;
  let finished = false;
  const done = restartBridge({ reload: () => { reloads++; } });
  done.then(() => { finished = true; });
  net.answer(0, { status: 'restarting' });
  await tick();
  t.mock.timers.tick(700);
  // The old process keeps answering: the restart never happened.
  for (let i = 1; i < 100; i++) {
    if (finished) break;
    for (let n = 0; net.calls.length <= i && n < 5; n++) await tick();
    net.answer(i, before);
    await tick();
    t.mock.timers.tick(1000);
    await tick();
  }
  assert.equal(await done, false);
  const dialog = document.querySelector('dialog');
  assert.match(dialog.textContent, /The bridge has not answered yet\./);
  assert.deepEqual(dialog.querySelectorAll('.dialog-actions button').map((b) => b.textContent), ['Close', 'Reload page']);
  button(dialog, 'Reload page').click();
  await tick();
  assert.equal(reloads, 1);
  assert.equal(document.querySelector('dialog'), null);
});
