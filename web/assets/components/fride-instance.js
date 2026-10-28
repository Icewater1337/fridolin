// Starting and stopping a FrIDE instance, as the Nodes and FrIDE pages
// offer it: the core places it as fride_spawn does, and stops it wherever
// it runs. Stopping one that has an open thread asks first.

import { errorStatus, post } from '../app/api.js';
import { errorMessage } from '../lib/errors.js';
import { stateKind } from '../lib/fride.js';
import { badge } from './common.js';
import { confirmDialog } from './dialog.js';
import { toast } from './toast.js';

/** @import { FrideInstanceState, FrideInstanceView } from '../../types/api.js' */

// Starting makes or wakes the sandbox, waits for its FrIDE and may clone
// its repositories first; stopping waits for sbx to stop the VM.
const START_TIMEOUT_MS = 15 * 60 * 1000;
const STOP_TIMEOUT_MS = 3 * 60 * 1000;

/** @param {string} name */
const base = (name) => `/api/fride/instances/${encodeURIComponent(name)}`;

/**
 * A badge for an instance's state; "not running" while no node has it.
 * @param {FrideInstanceState | '' | null | undefined} state
 */
export function instanceBadge(state) {
  return badge(state || 'not running', stateKind(state));
}

/**
 * Stops the instance called name. While it has an open thread the bridge
 * refuses (409) until asked with force, which this asks the owner first.
 * Resolves with whether it was stopped.
 * @param {string} name
 * @returns {Promise<boolean>}
 */
export async function stopInstance(name) {
  try {
    await post(`${base(name)}/stop`, {}, { timeout: STOP_TIMEOUT_MS });
  } catch (e) {
    if (errorStatus(e) !== 409) throw e;
    const ok = await confirmDialog({
      title: `Stop ${name} anyway?`,
      message: `${errorMessage(e).replace(/\.$/, '')}. Stopping it cuts off the work in progress; the instance’s disk stays.`,
      confirm: 'Stop anyway',
      danger: true,
    });
    if (!ok) return false;
    await post(`${base(name)}/stop`, { force: true }, { timeout: STOP_TIMEOUT_MS });
  }
  toast(`Stopped ${name}`, 'ok');
  return true;
}

/**
 * Starts the instance called name, at size (small, medium or large) or the
 * core's choice when none; resolves with the instance as it runs now.
 * @param {string} name
 * @param {string} [size]
 * @returns {Promise<FrideInstanceView>}
 */
export async function startInstance(name, size) {
  /** @type {FrideInstanceView} */
  const view = await post(`${base(name)}/start`, size ? { size } : {}, { timeout: START_TIMEOUT_MS });
  const where = view && view.node ? ` on ${view.node}` : '';
  toast(`Started ${name}${where}`, 'ok');
  return view;
}
