// Logs: a live tail of the bridge's log output.

import { get, qs } from '../app/api.js';
import { onStatus } from '../app/state.js';
import { h, mount } from '../lib/dom.js';
import { absTime, hhmmss } from '../lib/format.js';
import { btn } from '../components/buttons.js';
import { toggle, textInput } from '../components/fields.js';
import { pageHeader } from '../components/common.js';

const MAX_LINES = 5000;
// Go's log package prefixes "2006/01/02 15:04:05 "; the time column shows it already.
const GO_TS = /^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? /;

function levelOf(text) {
  if (/\b(ERROR|FATAL|FAILED|panic)\b|\berror\b/i.test(text)) return 'err';
  if (/\b(WARN|WARNING)\b|\bwarn(ing)?\b/i.test(text)) return 'warn';
  if (/\[dev\]/.test(text)) return 'dev';
  if (/\bDEBUG\b/.test(text)) return 'debug';
  return '';
}

export default {
  title: 'Logs',
  render(root, ctx) {
    let after = 0;
    let count = 0;
    let filter = '';
    let auto = true;
    let startedAt = null;

    const view = h('div.log-view', { role: 'log', 'aria-live': 'off', tabindex: '0' });
    const filterIn = textInput({ type: 'search', placeholder: 'Filter lines', 'aria-label': 'Filter log lines' });
    const autoSw = toggle({ checked: true, label: 'Follow', onChange: (v) => { auto = v; if (v) scrollEnd(); } });
    const countEl = h('span.muted.small');
    const clearBtn = btn('Clear view', { small: true, kind: 'ghost', onClick: () => { mount(view); count = 0; paintCount(); } });

    mount(root,
      pageHeader('Logs', 'Live output of the bridge — the same lines it writes to the terminal or service log.'),
      h('div.toolbar', h('div.toolbar-group', filterIn, autoSw), h('div.toolbar-group', countEl, clearBtn)),
      view);

    const matches = (el) => !filter || el.dataset.text.includes(filter);
    const paintCount = () => {
      const shown = filter ? view.querySelectorAll('.log-line:not([hidden])').length : count;
      countEl.textContent = filter ? `${shown} of ${count} lines` : `${count} lines`;
    };
    const scrollEnd = () => { view.scrollTop = view.scrollHeight; };

    let t;
    filterIn.addEventListener('input', () => {
      clearTimeout(t);
      t = setTimeout(() => {
        filter = filterIn.value.trim().toLowerCase();
        for (const el of view.children) if (el.dataset.text !== undefined) el.hidden = !matches(el);
        paintCount();
        if (auto) scrollEnd();
      }, 120);
    });

    view.addEventListener('scroll', () => {
      const atEnd = view.scrollHeight - view.scrollTop - view.clientHeight < 24;
      if (atEnd !== auto) {
        auto = atEnd;
        autoSw.input.checked = atEnd;
      }
    });

    function addLines(lines) {
      if (!lines.length) return;
      const frag = document.createDocumentFragment();
      for (const l of lines) {
        const text = String(l.text || '').replace(GO_TS, '');
        const lvl = levelOf(text);
        const el = h(`div.log-line${lvl ? `.${lvl}` : ''}`,
          h('span.log-time', { title: absTime(l.ts) }, hhmmss(l.ts)),
          h('span.log-text', text));
        el.dataset.text = String(l.text).toLowerCase();
        if (!matches(el)) el.hidden = true;
        frag.appendChild(el);
      }
      view.appendChild(frag);
      count += lines.length;
      while (view.children.length > MAX_LINES) { view.firstChild.remove(); count -= 1; }
      paintCount();
      if (auto) scrollEnd();
    }

    // The log sequence restarts with the bridge; start over when it restarts.
    ctx.onCleanup(onStatus((s) => {
      if (startedAt && s.started_at && s.started_at !== startedAt) {
        after = 0;
        view.appendChild(h('div.log-sep', '— the bridge restarted —'));
      }
      startedAt = s.started_at;
    }));

    ctx.poll(async (signal) => {
      const r = await get(`/api/logs${qs({ after, limit: 500 })}`, { signal });
      addLines(r.lines || []);
      if (typeof r.next === 'number') after = r.next;
    }, 2000);
  },
};
