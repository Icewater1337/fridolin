// Tabs with the ARIA tab pattern: a tablist whose tabs each name the
// panel they control, only the chosen tab in the Tab order, and the
// arrow keys, Home and End to move between tabs (which shows each one).

import { h } from '../lib/dom.js';
import { nextId } from './fields.js';

/**
 * How a tab came to be chosen: a click, a key, or tabs() itself at first.
 * @typedef {'click' | 'key' | 'init'} TabSelectedBy
 */

/** @type {Record<string, (i: number, n: number) => number>} */
const MOVES = {
  ArrowRight: (i, n) => (i + 1) % n,
  ArrowLeft: (i, n) => (i - 1 + n) % n,
  Home: () => 0,
  End: (i, n) => n - 1,
};

/**
 * tabs({label, items: [[key, text]], selected, onSelect(key, panel, {by})})
 * returns {list, panel, select(key)}. Place list (the tablist) above panel
 * (the tabpanel); onSelect fills panel for the chosen tab, at once for
 * selected and again on each change. by is 'click', 'key' or 'init', so a
 * panel can focus its input when the tab was clicked, but not take the
 * focus from a tab being chosen with the keyboard.
 * @template {string} K
 * @param {object} options
 * @param {string} options.label
 * @param {readonly (readonly [K, string])[]} options.items
 * @param {K} [options.selected]
 * @param {(key: K, panel: HTMLDivElement, how: {by: TabSelectedBy}) => void} options.onSelect
 * @returns {{list: HTMLDivElement, panel: HTMLDivElement, select: (key: K, by?: TabSelectedBy) => void}}
 */
export function tabs({ label, items, selected = items[0][0], onSelect }) {
  const id = nextId('tabs');
  const panel = h('div.tab-panel', { role: 'tabpanel', id: `${id}-panel` });
  const buttons = items.map(([key, text], i) => {
    const b = h('button.tab', { type: 'button', role: 'tab', id: `${id}-${i}`, 'aria-controls': panel.id }, text);
    b.addEventListener('click', () => select(key, 'click'));
    b.addEventListener('keydown', (e) => {
      const move = MOVES[e.key];
      if (!move) return;
      e.preventDefault();
      const next = move(i, items.length);
      select(items[next][0], 'key');
      buttons[next].focus();
    });
    return b;
  });
  const list = h('div.tabs', { role: 'tablist', 'aria-label': label }, buttons);
  /** @type {K | null} */
  let current = null;

  /**
   * @param {K} key
   * @param {TabSelectedBy} [by]
   */
  function select(key, by = 'click') {
    if (key === current) return;
    current = key;
    items.forEach(([k], i) => {
      buttons[i].setAttribute('aria-selected', String(k === key));
      buttons[i].setAttribute('tabindex', k === key ? '0' : '-1');
      if (k === key) panel.setAttribute('aria-labelledby', buttons[i].id);
    });
    onSelect(key, panel, { by });
  }

  select(selected, 'init');
  return { list, panel, select };
}
