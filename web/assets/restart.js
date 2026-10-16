// Restarting the bridge from the dashboard: POST /api/restart, then wait
// until /api/status answers from the new process. The token stays the same.

import { get, post } from './api.js';
import { store, setStatus, setRestartRequired } from './state.js';
import { h, mount, spinner, btn, toast, toastError } from './ui.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export async function restartBridge() {
  const before = store.status ? store.status.started_at : null;
  const msg = h('p', 'Restarting the bridge… this usually takes a few seconds.');
  const body = h('div.restart-body', spinner('large'), msg);
  const actions = h('div.dialog-actions');
  const dlg = h('dialog.dialog', { 'aria-label': 'Restarting' },
    h('div.dialog-head', h('h2', 'Restarting')),
    h('div.dialog-body', body),
    actions);
  dlg.addEventListener('cancel', (e) => e.preventDefault());
  document.body.appendChild(dlg);
  dlg.showModal();
  const close = () => { dlg.close(); dlg.remove(); };

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
        setStatus(s);
        setRestartRequired(null);
        close();
        toast('The bridge restarted', 'ok');
        return true;
      }
    } catch (e) {
      if (e.status === 401) { close(); return false; }
      sawDown = true;
    }
    await sleep(1000);
  }
  mount(body,
    h('p', 'The bridge has not answered yet.'),
    h('p.muted', 'If you changed the listen address (http_listen), the dashboard has moved: run ',
      h('code.mono', 'whatsapp-claude-bridge open'), ' in a terminal to open it at the new address. Otherwise check the terminal or service log of the bridge.'));
  mount(actions, btn('Close', { onClick: () => close() }), btn('Reload page', { kind: 'primary', onClick: () => location.reload() }));
  return false;
}
