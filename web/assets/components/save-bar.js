// The bar at the bottom of a form with unsaved changes: what changed,
// Discard, and the form's save button.

import { h } from '../lib/dom.js';
import { icon } from './icons.js';
import { btn } from './buttons.js';

/**
 * saveBar({save, onDiscard, floating}) returns the bar, hidden at first.
 * save is the form's save button; floating keeps the bar in view at the
 * bottom of the window. bar.show(text) shows it, by default with "Unsaved
 * changes", and bar.hide() hides it.
 */
export function saveBar({ save, onDiscard, floating = false }) {
  const text = h('span');
  const bar = h(`div.save-bar${floating ? '.floating' : ''}`, { hidden: true },
    icon('edit'), text, btn('Discard', { onClick: () => onDiscard() }), save);
  bar.show = (what = 'Unsaved changes') => { text.textContent = what; bar.hidden = false; };
  bar.hide = () => { bar.hidden = true; };
  return bar;
}
