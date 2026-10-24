// Restarting the bridge from the dashboard: POST /api/restart, then wait
// until /api/status answers from the new process. The sign-in stays: the
// bridge keeps sessions in its data folder.
// A new process of another version serves other modules, and pages load
// theirs on first visit, so the page reloads instead of mixing the two.

import { get, post, errorStatus } from './api.js';
import { store, setStatus } from './store.js';
import { h, mount } from '../lib/dom.js';
import { toast, toastError } from '../components/toast.js';
import { openDialog } from '../components/dialog.js';
import { spinner } from '../components/common.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** reload is location.reload, unless a test says otherwise. */
export async function restartBridge({ reload = () => location.reload() } = {}) {
  const { status } = store.get();
  const before = status ? status.started_at : null;
  const version = status ? status.version : '';
  const body = h('div.restart-body', spinner('large'), h('p', 'Restarting the bridge… this usually takes a few seconds, up to half a minute while a brain finishes an answer.'));
  // Only the restart's outcome closes it, and leaving the page (the browser's
  // Back button) does not: the restart goes on regardless.
  const dlg = openDialog({ title: 'Restarting', body, actions: [], dismissible: false, outlivesPage: true });
  const close = () => dlg.close();

  try {
    await post('/api/restart');
  } catch (e) {
    close();
    toastError(e);
    return false;
  }

  const t0 = Date.now();
  let sawDown = false;
  await sleep(700);
  while (Date.now() - t0 < 45000) {
    try {
      const s = await get('/api/status');
      if ((before && s.started_at !== before) || (!before && sawDown) || (sawDown && s.started_at !== before)) {
        close();
        if (version && s.version && s.version !== version) {
          reload();
          return true;
        }
        setStatus(s); // the new process has nothing waiting for a restart
        toast('The bridge restarted', 'ok');
        return true;
      }
    } catch (e) {
      if (errorStatus(e) === 401) { close(); return false; }
      sawDown = true;
    }
    await sleep(1000);
  }
  mount(body,
    h('p', 'The bridge has not answered yet.'),
    h('p.muted', 'If you changed the listen address (http_listen), the dashboard has moved: run ',
      h('code.mono', 'fridolin open'), ' in a terminal to open it at the new address. Otherwise check the terminal or service log of the bridge.'));
  dlg.setActions([{ label: 'Close', value: null }, { label: 'Reload page', kind: 'primary', onClick: () => { reload(); return true; } }]);
  return false;
}
