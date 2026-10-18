// The "Connect to your bridge" screen, shown when the bridge refuses a
// request: this browser is not signed in, or its sign-in ended.

import { h, mount } from '../lib/dom.js';
import { icon } from '../components/icons.js';
import { btn, copyButton } from '../components/buttons.js';
import { textInput } from '../components/fields.js';
import { get, signIn } from './api.js';
import { credentialFromHash } from './router.js';

/** Fills el with the connect screen; onConnected runs once signed in. */
export function connectScreen(el, { reason, onConnected }) {
  const input = textInput({ placeholder: 'Paste the token (or the whole dashboard link)', class: 'mono', 'aria-label': 'Dashboard token' });
  const errorEl = h('p.form-error', { hidden: true });
  const form = h('form.connect-form', input, btn('Connect', { kind: 'primary', type: 'submit', icon: 'key' }));
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const t = input.value.trim();
    if (!t) { input.focus(); return; }
    // A whole dashboard link signs in with what its fragment carries.
    const hash = t.indexOf('#');
    const credential = (hash >= 0 && credentialFromHash(t.slice(hash))) || { token: t };
    const submitBtn = form.querySelector('button');
    submitBtn.disabled = true;
    try {
      await signIn(credential);
      await get('/api/status');
      onConnected();
    } catch (err) {
      errorEl.hidden = false;
      errorEl.textContent = err.status === 401
        ? (credential.code ? 'That link was already used or has expired. Run the command above for a new one.' : 'That token was not accepted. Check that you copied all of it.')
        : err.message;
    } finally {
      submitBtn.disabled = false;
    }
  });
  const cmd = 'fridolin open';
  mount(el, h('div.connect-card.card',
    h('div.connect-head', h('img.brand-logo', { src: '/assets/logo-mark.webp', alt: '', width: 56, height: 56 }), h('div', h('h1', 'Connect to your bridge'), h('p.muted', 'The dashboard needs your bridge’s access token.'))),
    reason ? h('div.callout.warn', icon('alert', 'callout-icon'), h('div.callout-body', reason)) : null,
    h('ol.steps-list',
      h('li', h('div', 'In a terminal on this computer, run'), h('div.code-line', h('code.mono', cmd), copyButton(cmd)), h('div.muted.small', 'It opens this dashboard and signs you in, with a link that works once.')),
      h('li', h('div', 'Or paste the token from ', h('code.mono', '~/.fridolin/dashboard.token'), ' (in your data folder, if you moved it):'), form, errorEl)),
    h('div.connect-foot.muted.small', 'The token keeps programs on this computer — including your brains — from changing the bridge’s settings. It never leaves this machine, and this browser keeps only a sign-in for 30 days, not the token.')));
  setTimeout(() => input.focus(), 50);
}
