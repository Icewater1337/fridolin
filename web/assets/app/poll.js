// Polling: pages fetch their data now and then every few seconds. Polls
// pause while the tab is hidden or the connect screen is up, never overlap,
// wait longer after each failure, and turn errors into one toast per
// distinct message.

import { toastError } from '../components/toast.js';

const live = new Set();
let paused = () => false;

/** Sets what pauses every poll: a hidden tab, the connect screen. */
export function pausePollsWhen(fn) { paused = fn; }

/** Runs the polls that were due while paused. */
export function resumePolls() { live.forEach((p) => p.resume()); }

/** Runs the polls that are waiting out a failure, e.g. once the bridge answers again. */
export function retryPolls() { live.forEach((p) => p.retry()); }

// A failing poll waits twice as long after each failure in a row, up to
// this (or its own interval, when that is longer), so a bridge that is
// down is not asked every few seconds by every poll. One answer from it
// (retryPolls) brings them all back.
export const MAX_BACKOFF_MS = 30000;

/** How long a poll waits after failures failed runs in a row, given its interval. */
export function backoff(interval, failures) {
  if (!failures) return interval;
  return Math.min(interval * 2 ** failures, Math.max(interval, MAX_BACKOFF_MS));
}

const isAbort = (e) => e && (e.name === 'AbortError' || e.name === 'TimeoutError');

// 401 opens the connect screen and 0 shows the offline banner; anything
// else is worth a toast, and an error without a status is a bug.
function reportPollError(e) {
  if (e && e.status === undefined) console.error(e);
  if (e && e.status !== 401 && e.status !== 0) toastError(e);
}

/**
 * Runs fn(signal) now and then every interval ms (a number, or a function
 * returning one). kick() runs it again at once and resolves when a run that
 * started after the kick has finished, so a button can wait for the refresh
 * it asked for. A kick during a run aborts that run (its signal fires, so its
 * requests stop and it paints nothing stale) and starts a fresh one.
 */
export function makePoll(fn, interval, { onError = reportPollError } = {}) {
  let timer = null;
  let running = null; // the AbortController of the run in progress
  let stopped = false;
  let waiting = false; // due, but paused
  let failures = 0; // failed runs in a row
  let kicks = []; // resolvers of kick() calls the next run answers
  let lastErr = '';
  const next = () => backoff(typeof interval === 'function' ? interval() : interval, failures);
  const settle = (list) => list.forEach((resolve) => resolve());

  async function run() {
    clearTimeout(timer);
    timer = null;
    if (stopped) { settle(kicks.splice(0)); return; }
    if (paused()) { waiting = true; return; }
    waiting = false;
    const ctl = new AbortController();
    running = ctl;
    const answers = kicks.splice(0);
    try {
      await fn(ctl.signal);
      if (!ctl.signal.aborted) { lastErr = ''; failures = 0; }
    } catch (e) {
      if (!ctl.signal.aborted && !isAbort(e)) {
        const msg = (e && e.message) || String(e);
        if (msg !== lastErr) onError(e);
        lastErr = msg;
        failures += 1;
      }
    }
    running = null;
    if (ctl.signal.aborted && !stopped) {
      // Superseded by a kick: the fresh run answers this run's kicks too.
      kicks = answers.concat(kicks);
      run();
      return;
    }
    settle(answers);
    if (stopped) { settle(kicks.splice(0)); return; }
    timer = setTimeout(run, next());
  }

  const p = {
    kick() {
      return new Promise((resolve) => {
        if (stopped) { resolve(); return; }
        kicks.push(resolve);
        if (running) running.abort();
        else run();
      });
    },
    resume() { if (waiting && !stopped) run(); },
    retry() { if (failures && timer !== null && !stopped) run(); },
    stop() {
      stopped = true;
      live.delete(p);
      clearTimeout(timer);
      if (running) running.abort();
      else settle(kicks.splice(0));
    },
  };
  live.add(p);
  run();
  return p;
}
