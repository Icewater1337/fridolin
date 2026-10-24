// The core brain's grants (brain.json "grants"): what it may do with each
// other brain. Saved with the rest of the brain's settings, through the
// same brain.json write; a brain that edits them in its own brain.json
// waits for your approval like any other change.

import { h } from '../lib/dom.js';
import { nextId } from './fields.js';

/** @import { Brain } from '../../types/api.js' */

/** The kinds of grant, in brain.json's order, with what each lets the core do. */
const GRANT_KINDS = [
  ['delegate', 'Delegate', 'Hand it a task and get its answer'],
  ['read', 'Read notes', 'Read its memories/ notes and skills'],
  ['files', 'Files', 'Fetch files from its vault paths (once there is a vault)'],
];

/**
 * grantsEditor({grants, brains, onChange}) is a table with a row per other
 * brain ([{id, name}]) and a checkbox per kind of grant. A grant on a
 * brain that is no longer configured gets a row of its own, marked so,
 * for you to take away: the bridge refuses to save it. el.getValue() is
 * the grants as brain.json has them: only brains with a grant, their
 * kinds sorted.
 * @param {object} [options]
 * @param {Record<string, string[]> | null} [options.grants]
 * @param {Pick<Brain, 'id' | 'name'>[]} [options.brains]
 * @param {() => void} [options.onChange]
 * @returns {HTMLDivElement & {getValue: () => Record<string, string[]>}}
 */
export function grantsEditor({ grants = {}, brains = [], onChange } = {}) {
  const given = grants || {};
  const rows = brains.map((b) => ({ id: b.id, name: b.name || b.id, known: true }));
  for (const id of Object.keys(given).sort()) {
    if (!rows.some((r) => r.id === id)) rows.push({ id, name: id, known: false });
  }
  /** @type {{id: string, kind: string, input: HTMLInputElement}[]} */
  const boxes = []; // [{id, kind, input}]
  const el = h('div.grants-editor');
  if (!rows.length) {
    el.append(h('p.muted', 'There are no other brains yet. Add one under Brains, then come back to grant it.'));
  } else {
    el.append(h('div.table-wrap', h('table.table.grants-table',
      h('thead', h('tr', h('th', 'Brain'), GRANT_KINDS.map(([, label, help]) => h('th', { title: help }, label)))),
      h('tbody', rows.map((r) => h(`tr${r.known ? '' : '.unknown'}`,
        h('td', h('strong', r.name), ' ', h('code.mono.muted', r.id), r.known ? null : h('span.warn-text.small', ' not configured')),
        GRANT_KINDS.map(([kind, label]) => {
          const input = h('input', {
            type: 'checkbox', id: nextId('grant'),
            checked: (given[r.id] || []).includes(kind),
            'aria-label': `${label}: ${r.name}`,
          });
          if (onChange) input.addEventListener('change', () => onChange());
          boxes.push({ id: r.id, kind, input });
          return h('td', input);
        })))))));
  }
  const getValue = () => {
    /** @type {Record<string, string[]>} */
    const out = {};
    for (const { id, kind, input } of boxes) {
      if (input.checked) (out[id] = out[id] || []).push(kind);
    }
    for (const id of Object.keys(out)) {
      out[id].sort(); // as brain.json keeps them
    }
    return out;
  };
  return Object.assign(el, { getValue });
}
