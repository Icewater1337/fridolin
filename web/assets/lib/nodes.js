// What the Nodes page says about a node's capabilities, as the bridge's
// Capabilities.Summary says it in the CLI and !fleet.

/** "exec (uname, df), fs (2 roots, 1 writable), host, notify", or "nothing". */
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
  return parts.length ? parts.join(', ') : 'nothing';
}

/** How an exec rule reads: "git status|log", "hostname (no arguments)", "go (any arguments)". */
export function ruleText(r) {
  if (r.args === null || r.args === undefined) return `${r.name} (any arguments)`;
  if (!r.args.length) return `${r.name} (no arguments)`;
  return `${r.name} ${r.args.join(' | ')}`;
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
