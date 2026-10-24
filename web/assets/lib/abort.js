// A deadline for a request, on top of the caller's own AbortSignal.

/**
 * withDeadline(signal, ms) returns {signal, expired, clear()}: a signal that
 * aborts when the given one does (with its reason), or with a TimeoutError
 * after ms, then expired is true. ms 0 sets no deadline. Call clear() once
 * the work is done, so the timer and the listener go away.
 * @param {AbortSignal | null | undefined} signal
 * @param {number} ms
 * @returns {{signal: AbortSignal, expired: boolean, clear: () => void}}
 */
export function withDeadline(signal, ms) {
  const ctl = new AbortController();
  const onAbort = () => ctl.abort(signal && signal.reason);
  if (signal) {
    if (signal.aborted) onAbort();
    else signal.addEventListener('abort', onAbort, { once: true });
  }
  const timer = ms > 0 ? setTimeout(() => {
    d.expired = true;
    ctl.abort(new DOMException(`No answer within ${ms} ms`, 'TimeoutError'));
  }, ms) : undefined;
  const d = {
    signal: ctl.signal,
    expired: false,
    clear: () => {
      clearTimeout(timer);
      if (signal) signal.removeEventListener('abort', onAbort);
    },
  };
  return d;
}
