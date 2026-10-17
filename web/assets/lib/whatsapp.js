// Labels for the WhatsApp connection modes the bridge reports in /api/status.

export function waKind(mode) {
  return { connected: 'ok', connecting: 'warn', pairing: 'warn', 'qr-expired': 'warn', disconnected: 'warn', 'logged-out': 'bad', replaced: 'bad', disabled: 'neutral' }[mode] || 'neutral';
}

export function waLabel(mode) {
  return {
    connected: 'WhatsApp connected',
    connecting: 'Connecting to WhatsApp…',
    pairing: 'Waiting for pairing',
    'qr-expired': 'Pairing code expired',
    disconnected: 'WhatsApp disconnected',
    'logged-out': 'Logged out of WhatsApp',
    replaced: 'Session used elsewhere',
    disabled: 'WhatsApp off',
  }[mode] || (mode ? `WhatsApp: ${mode}` : 'WhatsApp');
}
