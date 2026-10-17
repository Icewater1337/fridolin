// The "Connect to your bridge" screen, shown when the bridge refuses the
// saved token (or there is none).

import { h, mount } from '../lib/dom.js';
import { icon } from '../components/icons.js';
import { btn, copyButton } from '../components/buttons.js';
import { textInput } from '../components/fields.js';
import { get, getToken, setToken } from './api.js';

/** Fills el with the connect screen; onConnected runs once a token works. */
export function connectScreen(el, { reason, onConnected }) {
  const input = textInput({ placeholder: 'Paste the token (or the whole dashboard link)', class: 'mono', 'aria-label': 'Dashboard token' });
  const errorEl = h('p.form-error', { hidden: true });
  const form = h('form.connect-form', input, btn('Connect', { kind: 'primary', type: 'submit', icon: 'key' }));
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    let t = input.value.trim();
    const m = t.match(/#token=([A-Za-z0-9]+)/);
    if (m) t = m[1];
    if (!t) { input.focus(); return; }
    const submitBtn = form.querySelector('button');
    submitBtn.disabled = true;
    const prev = getToken();
    setToken(t);
    try {
      await get('/api/status');
      onConnected();
    } catch (err) {
      setToken(prev);
      errorEl.hidden = false;
      errorEl.textContent = err.status === 401 ? 'That token was not accepted. Check that you copied all of it.' : err.message;
    } finally {
      submitBtn.disabled = false;
    }
  });
  const cmd = 'fridolin open';
  mount(el, h('div.connect-card.card',
    h('div.connect-head', h('img.brand-logo', { src: '/assets/logo-mark.webp', alt: '', width: 56, height: 56 }), h('div', h('h1', 'Connect to your bridge'), h('p.muted', 'The dashboard needs your bridge’s access token.'))),
    reason ? h('div.callout.warn', icon('alert', 'callout-icon'), h('div.callout-body', reason)) : null,
    h('ol.steps-list',
      h('li', h('div', 'In a terminal on this computer, run'), h('div.code-line', h('code.mono', cmd), copyButton(cmd)), h('div.muted.small', 'It opens this dashboard with the token filled in.')),
      h('li', h('div', 'Or paste the token from ', h('code.mono', '~/.fridolin/dashboard.token'), ' (in your data folder, if you moved it):'), form, errorEl)),
    h('div.connect-foot.muted.small', 'The token keeps programs on this computer — including your brains — from changing the bridge’s settings. It never leaves this machine.')));
  setTimeout(() => input.focus(), 50);
}
