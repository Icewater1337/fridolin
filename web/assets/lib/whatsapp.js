// Labels for the WhatsApp connection modes the bridge reports in /api/status.

/**
 * The kind (ok, warn, bad, neutral) of a WhatsApp connection mode.
 * @param {string | undefined} mode
 * @returns {string}
 */
export function waKind(mode) {
  return /** @type {Record<string, string>} */ ({
    connected: 'ok', connecting: 'warn', pairing: 'warn', 'qr-expired': 'warn', disconnected: 'warn',
    'logged-out': 'bad', replaced: 'bad', outdated: 'bad', banned: 'bad', failed: 'bad', disabled: 'neutral',
  })[mode] || 'neutral';
}

/**
 * What a WhatsApp connection mode means, for people.
 * @param {string | undefined} mode
 * @returns {string}
 */
export function waLabel(mode) {
  return /** @type {Record<string, string>} */ ({
    connected: 'WhatsApp connected',
    connecting: 'Connecting to WhatsApp…',
    pairing: 'Waiting for pairing',
    'qr-expired': 'Pairing code expired',
    disconnected: 'WhatsApp disconnected',
    'logged-out': 'Logged out of WhatsApp',
    replaced: 'Session used elsewhere',
    outdated: 'Fridolin needs an update',
    banned: 'Temporarily banned by WhatsApp',
    failed: 'WhatsApp refused the connection',
    disabled: 'WhatsApp off',
  })[mode] || (mode ? `WhatsApp: ${mode}` : 'WhatsApp');
}
