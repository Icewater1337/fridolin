// Form controls: labelled rows, switches, selects, text inputs and editable
// lists of strings.

import { h } from '../lib/dom.js';
import { icon } from './icons.js';
import { btn } from './buttons.js';

let uid = 0;
export function nextId(prefix = 'f') { uid += 1; return `${prefix}-${uid}`; }

/** A labelled form row. */
export function field({ label, help, control, id, tag, wide, warn }) {
  const ctrl = control;
  const target = id || (ctrl && ctrl.id) || (ctrl && ctrl.querySelector && ctrl.querySelector('input,select,textarea')?.id);
  return h(`div.field${wide ? '.wide' : ''}`,
    label ? h('label.field-label', { for: target || null }, label, tag ? h('span.field-tag', tag) : null) : null,
    ctrl,
    help ? h('div.help', help) : null,
    warn ? h('div.help.warn-text', warn) : null);
}

/** A switch (checkbox with role=switch). Returns the wrapper; .input is the checkbox. */
export function toggle({ checked = false, label, onChange, disabled, id }) {
  const input = h('input', { type: 'checkbox', role: 'switch', checked: !!checked, disabled, id: id || nextId('sw') });
  if (onChange) input.addEventListener('change', () => onChange(input.checked, input));
  const el = h('label.switch', { for: input.id }, input, h('span.track', h('span.thumb')), label ? h('span.switch-label', label) : null);
  el.input = input;
  return el;
}

export function select(options, value, attrs = {}) {
  const s = h('select', { id: attrs.id || nextId('sel'), ...attrs });
  for (const o of options) {
    const [v, label] = Array.isArray(o) ? o : [o, o];
    s.appendChild(h('option', { value: v, selected: v === value }, label));
  }
  if (value !== undefined) s.value = value;
  return s;
}

export function textInput(attrs = {}) {
  return h('input', { type: 'text', id: attrs.id || nextId('in'), autocomplete: 'off', spellcheck: 'false', ...attrs });
}

/** Editable list of strings, one row per item. el.getValues() returns the trimmed list. */
export function listEditor({ values = [], placeholder = '', mono = true, addLabel = 'Add', onChange, validate, empty: emptyText }) {
  const rows = h('div.list-rows');
  const el = h('div.list-editor', rows);
  const changed = () => { if (onChange) onChange(); };
  const addRow = (v = '', focus = false) => {
    const input = textInput({ value: v, placeholder, class: mono ? 'mono' : '' });
    input.addEventListener('input', () => { check(input); changed(); });
    const del = h('button.icon-btn', { type: 'button', title: 'Remove', 'aria-label': 'Remove' }, icon('x'));
    const row = h('div.list-row', input, del);
    del.addEventListener('click', () => { row.remove(); renderEmpty(); changed(); });
    rows.appendChild(row);
    check(input);
    renderEmpty();
    if (focus) input.focus();
  };
  const check = (input) => {
    const msg = validate && input.value.trim() ? validate(input.value.trim()) : '';
    input.classList.toggle('invalid', !!msg);
    input.title = msg || '';
  };
  const emptyEl = h('div.list-empty.muted', emptyText || 'None');
  const renderEmpty = () => { emptyEl.hidden = rows.children.length > 0; };
  el.prepend(emptyEl);
  (values || []).forEach((v) => addRow(v));
  renderEmpty();
  el.appendChild(h('div.list-add', btn(addLabel, { icon: 'plus', small: true, kind: 'ghost', onClick: () => { addRow('', true); changed(); } })));
  el.getValues = () => [...rows.querySelectorAll('input')].map((i) => i.value.trim()).filter(Boolean);
  el.isValid = () => ![...rows.querySelectorAll('input')].some((i) => i.classList.contains('invalid'));
  return el;
}

/** Textarea holding one item per line. */
export function linesArea(values, attrs = {}) {
  const ta = h('textarea.mono', { id: attrs.id || nextId('ta'), rows: attrs.rows || 4, spellcheck: 'false', ...attrs });
  ta.value = (values || []).join('\n');
  return ta;
}

export function linesOf(textarea) {
  return textarea.value.split('\n').map((s) => s.trim()).filter(Boolean);
}
