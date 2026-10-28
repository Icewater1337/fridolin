// FrIDE instances (docs/NODES.md, "FrIDE instances"): each a whole FrIDE
// in a Docker Sandbox of its own on a node, for one trust domain. What the
// dashboard says about them, and the rules for fride.instances as the
// bridge checks them (config.validateFrideInstances).

/** @import { FrideHostCap, FrideInstanceState } from '../../types/api.js' */

/** The bounds of fride.instances, as the bridge holds them. */
export const INSTANCE_LIMITS = Object.freeze({
  instances: 16,
  /** Repositories, egress hosts and nodes, each. */
  list: 64,
  minMemoryMB: 2048,
  maxMemoryMB: 65536,
  maxIdleMinutes: 1440,
});

/** How long an instance idles before it stops when its config does not say, in minutes. */
export const IDLE_STOP_DEFAULT = 30;

/**
 * The sizes the core starts an instance at, with their memory in MB
 * (fride_spawn's size).
 * @type {readonly (readonly ['small' | 'medium' | 'large', number])[]}
 */
export const INSTANCE_SIZES = [['small', 3072], ['medium', 6144], ['large', 12288]];

/** The git identities an instance may get. */
export const INSTANCE_IDENTITIES = /** @type {const} */ (['github', 'gitlab']);

const NAME = /^[a-z][a-z0-9-]{0,23}$/;

/**
 * Why name cannot name an instance beside those in taken, "" when it can.
 * @param {string} name
 * @param {readonly string[]} [taken]
 */
export function instanceNameProblem(name, taken = []) {
  if (!name) return 'Give it a name.';
  if (!NAME.test(name)) return 'A lower-case letter, then up to 23 lower-case letters, digits and dashes.';
  if (name === 'native') return '“native” is the name of a node’s own FrIDE.';
  if (taken.includes(name)) return 'Another instance has this name.';
  return '';
}

/**
 * Why a line of an instance's repositories is no git URL, "" when it is.
 * @param {string} url
 */
export function repoProblem(url) {
  return /\s/.test(url) ? `“${url}” is no git URL: it has a space.` : '';
}

/**
 * Why a line of an instance's egress is no host name, "" when it is.
 * @param {string} host
 */
export function egressProblem(host) {
  return /[\s/:]/.test(host) ? `“${host}” is no host name: no scheme, path or port.` : '';
}

/**
 * Megabytes as people read them: 6144 → "6 GB", 1536 → "1.5 GB",
 * 512 → "512 MB"; "" for none.
 * @param {number | null | undefined} mb
 */
export function memText(mb) {
  if (!mb || mb < 0) return '';
  if (mb < 1024) return `${mb} MB`;
  const gb = mb / 1024;
  return `${Number.isInteger(gb) || gb >= 10 ? Math.round(gb) : gb.toFixed(1)} GB`;
}

/**
 * A state's badge kind: running ok, starting info, failed bad, the rest neutral.
 * @param {FrideInstanceState | '' | null | undefined} state
 */
export function stateKind(state) {
  switch (state) {
    case 'running': return 'ok';
    case 'starting': return 'info';
    case 'failed': return 'bad';
    default: return 'neutral';
  }
}

/**
 * A node's memory as fride_host reports it: "64 GB, 40 GB available,
 * 6 GB kept for the machine: 34 GB for instances".
 * @param {FrideHostCap} cap
 */
export function hostMemoryText(cap) {
  const total = memText(cap.total_memory_mb);
  const parts = [
    total || 'memory unknown',
    `${memText(cap.available_memory_mb) || '0 MB'} available`,
    cap.reserve_mb ? `${memText(cap.reserve_mb)} kept for the machine` : null,
  ].filter(Boolean).join(', ');
  const room = Math.max(0, (cap.available_memory_mb || 0) - (cap.reserve_mb || 0));
  return `${parts}: ${memText(room) || 'nothing'} for instances`;
}

/**
 * How long an instance idles before it stops, from GET /api/fride's
 * idle_stop_minutes: the value in effect, 0 for never.
 * @param {number | null | undefined} minutes
 */
export function idleStopText(minutes) {
  return minutes && minutes > 0 ? `stops after ${minutes} minutes idle` : 'never stops by itself';
}
