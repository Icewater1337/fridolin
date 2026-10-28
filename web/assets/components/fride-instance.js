// Starting and stopping a FrIDE instance, as the Nodes and FrIDE pages
// offer it: the core places it as fride_spawn does, and stops it wherever
// it runs. Stopping one that has an open thread asks first.

import { errorStatus, post } from '../app/api.js';
import { errorMessage } from '../lib/errors.js';
import { stateKind } from '../lib/fride.js';
import { badge } from './common.js';
import { confirmDialog } from './dialog.js';
import { toast } from './toast.js';

/** @import { FrideInstanceState } from '../../types/api.js' */

// Stopping waits for sbx to stop the VM.
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
