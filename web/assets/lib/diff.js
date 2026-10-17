// Comparing an approved brain.json with the one on disk: a per-setting table
// and a line diff of the pretty-printed JSON.

/** A copy of v with every object's keys sorted, so equal values print alike. */
export function sortKeys(v) {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (v && typeof v === 'object') {
    const out = {};
    for (const k of Object.keys(v).sort()) out[k] = sortKeys(v[k]);
    return out;
  }
  return v;
}

function flatten(v, prefix, out) {
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    for (const k of Object.keys(v)) flatten(v[k], prefix ? `${prefix}.${k}` : k, out);
  } else {
    out.set(prefix, v);
  }
  return out;
}

function show(v) {
  if (v === undefined) return '(not set)';
  if (Array.isArray(v)) return v.length ? v.map((x) => JSON.stringify(x)).join('\n') : '[]';
  return JSON.stringify(v);
}

/** The settings (dotted paths) whose values differ, as {path, a, b}; unset and empty count as equal. */
export function fieldDiff(a, b) {
  const fa = flatten(a, '', new Map());
  const fb = flatten(b, '', new Map());
  const keys = [...new Set([...fa.keys(), ...fb.keys()])].sort();
  const rows = [];
  for (const k of keys) {
    const x = show(fa.get(k));
    const y = show(fb.get(k));
    const empty = (s) => s === '(not set)' || s === '""' || s === '[]' || s === 'null';
    if (x !== y && !(empty(x) && empty(y))) rows.push({ path: k, a: x, b: y });
  }
  return rows;
}

/** Line diff via LCS; marks lines that only appear on one side. */
export function lineDiff(aText, bText) {
  const a = aText.split('\n');
  const b = bText.split('\n');
  const n = a.length;
  const m = b.length;
  const dp = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  }
  const keepA = new Array(n).fill(false);
  const keepB = new Array(m).fill(false);
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) { keepA[i] = true; keepB[j] = true; i++; j++; } else if (dp[i + 1][j] >= dp[i][j + 1]) i++; else j++;
  }
  return {
    left: a.map((text, k) => ({ text, changed: !keepA[k] })),
    right: b.map((text, k) => ({ text, changed: !keepB[k] })),
  };
}
