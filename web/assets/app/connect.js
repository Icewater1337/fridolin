// The "Connect to your bridge" screen, shown when the bridge refuses a
// request: this browser is not signed in, or its sign-in ended.

import { h, mount } from '../lib/dom.js';
import { icon } from '../components/icons.js';
import { btn, copyButton } from '../components/buttons.js';
import { textInput } from '../components/fields.js';
import { get, signIn } from './api.js';
import { credentialFromHash } from './router.js';

/**
 * Fills el with the connect screen; onConnected runs once signed in.
 * @param {HTMLElement} el
 * @param {{reason?: string, onConnected: () => void}} options reason says why the screen is shown
 */
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
  const steps = h('ol.steps-list');
  mount(el, h('div.connect-card.card',
    h('div.connect-head', h('img.brand-logo', { src: '/assets/logo-mark.webp', alt: '', width: 56, height: 56 }), h('div', h('h1', 'Connect to your bridge'), h('p.muted', 'The dashboard needs your bridge’s access token.'))),
    reason ? h('div.callout.warn', icon('alert', 'callout-icon'), h('div.callout-body', reason)) : null,
    steps,
    h('div.connect-foot.muted.small', 'The token keeps programs on this computer — including your brains — from changing the bridge’s settings. It never leaves this machine, and this browser keeps only a sign-in for 30 days, not the token.')));
  // The steps depend on where the bridge runs, which /ready says without
  // a token. Until it answers, and if it does not, the native ones.
  mount(steps, nativeSteps(form, errorEl));
  runsInContainer().then((container) => {
    if (container) mount(steps, containerSteps(form, errorEl));
  });
  setTimeout(() => input.focus(), 50);
}

/** Whether the bridge says it runs in the fridolin-core container. */
async function runsInContainer() {
  try {
    return (await get('/ready', { timeout: 5000 })).container === true;
  } catch (e) {
    // /ready answers 503 with the same body while WhatsApp is not connected.
    return !!(e && e.data && e.data.container === true);
  }
}

/** @param {string} cmd */
function commandLine(cmd) {
  return h('div.code-line', h('code.mono', cmd), copyButton(cmd));
}

// A bridge on this computer: fridolin open reads the token from its data folder.
/**
 * @param {HTMLFormElement} form
 * @param {HTMLElement} errorEl
 */
function nativeSteps(form, errorEl) {
  return [
    h('li', h('div', 'In a terminal on this computer, run'), commandLine('fridolin open'), h('div.muted.small', 'It opens this dashboard and signs you in, with a link that works once.')),
    h('li', h('div', 'On another computer or phone, send ', h('code.mono', '!dashboard'), ' to Fridolin in your WhatsApp chat (or run ', h('code.mono', 'fridolin open --print --tailnet'), ' on the core) and open the link it sends.'), h('div.muted.small', 'The link works once, within 10 minutes; no token needed.')),
    h('li', h('div', 'Or paste the token from ', h('code.mono', '~/.fridolin/dashboard.token'), ' (in your data folder, if you moved it):'), form, errorEl),
  ];
}

// The container core (docs/CORE.md): its data folder is the fridolin-data
// volume, so a token in the host's home folder is not this bridge's.
/**
 * @param {HTMLFormElement} form
 * @param {HTMLElement} errorEl
 */
function containerSteps(form, errorEl) {
  return [
    h('li', h('div', 'This bridge runs in the ', h('code.mono', 'fridolin-core'), ' container. In a terminal on the computer that runs it, run'), commandLine('docker exec fridolin-core fridolin open --print'),
      h('div.muted.small', 'and open the link it prints. It works once, within a minute; no token needed. ', h('code.mono', 'fridolin open'), ' on that computer does the same and opens the browser for you.')),
    h('li', h('div', 'On another computer or phone, send ', h('code.mono', '!dashboard'), ' to Fridolin in your WhatsApp chat and open the link it sends.'), h('div.muted.small', 'The link works once, within 10 minutes; no token needed.')),
    h('li', h('div', 'Or paste the token. It is in the container’s data volume, not in your home folder; ', h('code.mono', 'docker exec fridolin-core cat /data/dashboard.token'), ' prints it:'), form, errorEl),
  ];
}
