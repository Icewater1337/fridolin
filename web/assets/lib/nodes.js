// What the Nodes page says about a node's capabilities, as the bridge's
// Capabilities.Summary says it in the CLI and !fleet.

/** "exec (uname, df), fs (2 roots, 1 writable), host, notify, fride, backup (1 folder)", or "nothing". */
export function capSummary(c) {
  const parts = [];
  if (c && c.exec && (c.exec.allow || []).length) {
    parts.push(`exec (${c.exec.allow.map((r) => r.name).join(', ')})`);
  }
  const roots = (c && c.fs && c.fs.roots) || [];
  if (roots.length) {
    const w = roots.filter((r) => r.write).length;
    parts.push(`fs (${roots.length} root${roots.length === 1 ? '' : 's'}${w ? `, ${w} writable` : ''})`);
  }
  if (c && c.host) parts.push('host');
  if (c && c.notify) parts.push('notify');
  if (c && c.fride) parts.push('fride');
  if (c && c.backup) {
    const n = (c.backup.folders || []).length;
    parts.push(n ? `backup (${n} folder${n === 1 ? '' : 's'})` : 'backup');
  }
  if (c && c.update) parts.push('update');
  return parts.length ? parts.join(', ') : 'nothing';
}

/** How an exec rule reads: "git status|log", "hostname (no arguments)", "go (any arguments)". */
export function ruleText(r) {
  if (r.args === null || r.args === undefined) return `${r.name} (any arguments)`;
  if (!r.args.length) return `${r.name} (no arguments)`;
  return `${r.name} ${r.args.join(' | ')}`;
}

/** How a node's last update from the core went, in a few words; null for none. */
export function updateText(u) {
  if (!u || !u.state) return null;
  switch (u.state) {
    case 'sending': return `sending ${u.to}…`;
    case 'restarting': return `restarting into ${u.to}…`;
    case 'ok': return `updated from ${u.from} to ${u.to}`;
    case 'failed': return `the update to ${u.to} failed: ${u.error || 'no reason given'}`;
    default: return u.state;
  }
}

/** The OS as people name it. */
export function osName(goos) {
  return { darwin: 'macOS', windows: 'Windows', linux: 'Linux' }[goos] || goos || '?';
}

/** "4 GB of 16 GB free" style figures from bytes. */
export function gb(n) {
  if (!n) return '';
  return `${(n / (1 << 30)).toFixed(n < 10 * (1 << 30) ? 1 : 0)} GB`;
}

/** Tags typed into one box, as the bridge takes them. */
export function parseTags(text) {
  return [...new Set(String(text || '').split(/[\s,]+/).map((t) => t.trim().toLowerCase()).filter(Boolean))];
}
