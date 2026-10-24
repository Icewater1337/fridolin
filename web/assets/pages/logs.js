// Logs: a live tail of the bridge's log output.

import { get, qs } from '../app/api.js';
import { h, mount } from '../lib/dom.js';
import { absTime, hhmmss } from '../lib/format.js';
import { logLevel } from '../lib/logs.js';
import { btn } from '../components/buttons.js';
import { toggle, textInput } from '../components/fields.js';
import { pageHeader } from '../components/common.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { LogLine } from '../../types/api.js' */

const MAX_LINES = 5000;
// Go's log package prefixes "2006/01/02 15:04:05 "; the time column shows it already.
const GO_TS = /^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? /;

export default /** @satisfies {PageModule} */ ({
  title: 'Logs',
  render(root, ctx) {
    let after = 0; // the last line shown, as the bridge numbers them
    /** @type {number | null} */
    let boot = null; // when the process that numbers them started
    let count = 0; // lines shown, not counting the restart notes
    let filter = '';
    let auto = true;

    const view = h('div.log-view', { role: 'log', 'aria-live': 'off', tabindex: '0' });
    const filterIn = textInput({ type: 'search', placeholder: 'Filter lines', 'aria-label': 'Filter log lines' });
    const autoSw = toggle({ checked: true, label: 'Follow', onChange: (v) => { auto = v; if (v) scrollEnd(); } });
    const countEl = h('span.muted.small');
    const clearBtn = btn('Clear view', { small: true, kind: 'ghost', onClick: () => { mount(view); count = 0; paintCount(); } });

    mount(root,
      pageHeader('Logs', 'Live output of the bridge — the same lines it writes to the terminal or service log.'),
      h('div.toolbar', h('div.toolbar-group', filterIn, autoSw), h('div.toolbar-group', countEl, clearBtn)),
      view);

    /** @param {Element} el */
    const isLine = (el) => el.classList.contains('log-line');
    // A line's text is its last cell; the filter looks at what is shown.
    /** @param {Element} el */
    const matches = (el) => !filter || ((el.lastChild && el.lastChild.textContent) || '').toLowerCase().includes(filter);
    const paintCount = () => {
      const shown = filter ? /** @type {HTMLElement[]} */ ([...view.children]).filter((el) => isLine(el) && !el.hidden).length : count;
      countEl.textContent = filter ? `${shown} of ${count} lines` : `${count} lines`;
    };
    const scrollEnd = () => { view.scrollTop = view.scrollHeight; };

    filterIn.addEventListener('input', ctx.debounce(() => {
      filter = filterIn.value.trim().toLowerCase();
      for (const el of /** @type {HTMLCollectionOf<HTMLElement>} */ (view.children)) if (isLine(el)) el.hidden = !matches(el);
      paintCount();
      if (auto) scrollEnd();
    }, 120));

    view.addEventListener('scroll', () => {
      const atEnd = view.scrollHeight - view.scrollTop - view.clientHeight < 24;
      if (atEnd !== auto) {
        auto = atEnd;
        autoSw.input.checked = atEnd;
      }
    });

    /** @param {LogLine[]} lines */
    function addLines(lines) {
      if (!lines.length) return;
      const frag = document.createDocumentFragment();
      for (const l of lines) {
        const text = String(l.text || '').replace(GO_TS, '');
        const lvl = logLevel(text);
        const el = h(`div.log-line${lvl ? `.${lvl}` : ''}`,
          h('span.log-time', { title: absTime(l.ts) }, hhmmss(l.ts)),
          h('span.log-text', text));
        if (!matches(el)) el.hidden = true;
        frag.appendChild(el);
      }
      view.appendChild(frag);
      count += lines.length;
      // The oldest go first, and a restart note with them.
      while (count > MAX_LINES) {
        const first = view.children[0]; // view holds elements only: the lines and restart notes
        if (isLine(first)) count -= 1;
        first.remove();
      }
      paintCount();
      if (auto) scrollEnd();
    }

    // Each answer says when its process started (boot). A new process
    // numbers its lines from 1 again, so the answer to the old cursor is
    // dropped and the new process is read from its first line, in the same
    // run.
    ctx.poll(async (signal) => {
      for (let tries = 0; tries < 2; tries++) {
        const r = await get(`/api/logs${qs({ after, limit: 500 })}`, { signal });
        if (boot !== null && r.boot !== boot) {
          boot = r.boot;
          after = 0;
          view.appendChild(h('div.log-sep', '— the bridge restarted —'));
          continue;
        }
        boot = r.boot;
        addLines(r.lines || []);
        if (typeof r.next === 'number') after = r.next;
        return;
      }
    }, 2000);
  },
});
