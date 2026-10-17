import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { stubFetch } from './stub-fetch.js';
import { restartBridge } from '../assets/app/restart.js';
import { store, setStatus } from '../assets/app/store.js';

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
