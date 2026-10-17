// Time, size and count formatting. Pure functions, so they are unit-tested
// with node --test (web/test/format.test.js).

/** A Date for ts (a Date, an ISO string or a number), or null for empty and zero times. */
export function parseTs(ts) {
  if (!ts) return null;
  const d = ts instanceof Date ? ts : new Date(ts);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return null;
  return d;
}

/** "just now", "3 min ago", "in 2 hours", "yesterday"; a date beyond 30 days. */
export function relTime(ts, now = Date.now()) {
  const d = parseTs(ts);
  if (!d) return 'never';
  const diff = (now - d.getTime()) / 1000;
  const abs = Math.abs(diff);
  const future = diff < -5;
  let s;
  if (abs < 45) return future ? 'in a few seconds' : 'just now';
  if (abs < 90) s = '1 min';
  else if (abs < 3600) s = `${Math.round(abs / 60)} min`;
  else if (abs < 5400) s = '1 hour';
  else if (abs < 86400) s = `${Math.round(abs / 3600)} hours`;
  else if (abs < 172800) return future ? 'tomorrow' : 'yesterday';
  else if (abs < 30 * 86400) s = `${Math.round(abs / 86400)} days`;
  else return d.toLocaleDateString(undefined, { dateStyle: 'medium' });
  return future ? `in ${s}` : `${s} ago`;
}

export function absTime(ts) {
  const d = parseTs(ts);
  if (!d) return '';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' });
}

/** Local wall-clock time as HH:MM:SS, or '' for an unparseable time. */
export function hhmmss(ts) {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export function fmtDuration(seconds) {
  if (seconds === null || seconds === undefined || Number.isNaN(seconds)) return '';
  const s = Math.round(seconds);
  if (s < 1) return `${Math.round(seconds * 1000)} ms`;
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60 ? `${s % 60} s` : ''}`.trim();
  if (s < 86400) return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
  return `${Math.floor(s / 86400)} d ${Math.floor((s % 86400) / 3600)} h`;
}

export function fmtBytes(n) {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function plural(n, one, many) {
  return `${n} ${n === 1 ? one : (many || `${one}s`)}`;
}

export function prettyJSON(v) {
  return JSON.stringify(v, null, 2);
}
