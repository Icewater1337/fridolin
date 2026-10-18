// A form over data that polls keep bringing in, modelled as {initial,
// current}: the data the form was built from, and what it shows now. New
// data rebuilds a form nobody has edited. A form with edits keeps them, and
// says so when what it edits changed somewhere else meanwhile, instead of
// either losing the edits or quietly saving over that change.

import { h, mount } from '../lib/dom.js';
import { btn } from './buttons.js';
import { callout } from './common.js';

/**
 * liveForm({build, edits, onChange, staleNote}) returns
 * {el, set(data), dirty(), stale(), discard(), saved(), form(), data()}.
 *
 * build(data, changed) returns {el, value(), ...}: the form for data, and
 * what it would save now (anything JSON can hold). It calls changed() after
 * a change that fires no input or change event, such as a removed row.
 *
 * edits(data, form) is the part of data the form edits (all of it unless
 * given); form is what build returned, e.g. to look only at the fields that
 * have edits. A form with edits is stale while that part differs between
 * the data it was built from and the newest data; then it shows staleNote
 * ({title, text, action}) above itself, whose button calls discard().
 *
 * onChange({dirty, stale}) runs whenever either may have changed.
 *
 * set(data) brings in newer data: a form without edits is built again from
 * it, one with edits keeps them (and is built from the newest data once
 * they are undone and the focus leaves it). dirty() is true while value()
 * differs from what the form showed when it was built. discard() builds
 * the form again from the newest data. saved() says the edits are saved:
 * the form counts as clean, and the next data that differs builds it
 * again in any case. form() is what build returned last; data() the
 * newest data.
 */
export function liveForm({ build, edits = (d) => d, onChange = () => {}, staleNote = {} }) {
  const note = callout('warn', staleNote.title || 'These settings changed elsewhere', { hidden: true },
    staleNote.text || 'Saving now would overwrite that change.',
    h('div.actions', btn(staleNote.action || 'Load the new settings', { small: true, onClick: () => discard() })));
  const box = h('div.live-form-body');
  const el = h('div.live-form', note, box);

  let built = null; // what build returned
  let initial; // the data it was built from
  let base = ''; // JSON of its value() right after building
  let latest; // the newest data
  let moved = false; // what the form edits changed since it was built
  let rebuildNext = false;

  const json = (v) => JSON.stringify(v);
  const dirty = () => !!built && json(built.value()) !== base;
  // Without edits there is nothing to lose: a form taken back to what it
  // showed is not stale, though the next new data still builds it again.
  const isStale = () => moved && dirty();
  const report = () => {
    note.hidden = !isStale();
    onChange({ dirty: dirty(), stale: isStale() });
  };
  const setMoved = (v) => {
    if (v === moved) return;
    moved = v;
    report();
  };

  function rebuild(data) {
    latest = data;
    initial = data;
    built = build(data, report);
    base = json(built.value());
    rebuildNext = false;
    moved = false;
    mount(box, built.el);
    report();
  }

  function discard() { if (built) rebuild(latest); }

  box.addEventListener('input', report);
  box.addEventListener('change', report);
  // A form taken back to what it showed after the data changed is clean
  // but shows the old data, and new data comes only with the next change.
  // It catches up once the focus leaves it, which interrupts no typing.
  // Saved edits wait for the data that holds them instead.
  box.addEventListener('focusout', (e) => {
    const to = e.relatedTarget || document.activeElement;
    if (to && box.contains(to)) return;
    if (built && !rebuildNext && !dirty() && json(latest) !== json(initial)) rebuild(latest);
  });

  return {
    el,
    set(data) {
      latest = data;
      if (built && json(data) === json(initial)) { setMoved(false); return; }
      if (!built || rebuildNext || !dirty()) { rebuild(data); return; }
      setMoved(json(edits(initial, built)) !== json(edits(data, built)));
    },
    dirty,
    stale: isStale,
    discard,
    saved() {
      if (built) base = json(built.value());
      rebuildNext = true;
      moved = false;
      report();
    },
    form: () => built,
    data: () => latest,
  };
}
