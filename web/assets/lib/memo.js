// A cheap guard for 3 s polling: paint a region only when its data changed.

/** Returns true (and remembers data under key) when data differs from last time. */
export function changed(holder, key, data) {
  const s = JSON.stringify(data);
  if (holder[key] === s) return false;
  holder[key] = s;
  return true;
}
