// A terminal on a node (docs/NODES.md, "Workstation"): a shell the node
// starts in a granted folder, which the bridge opens, numbers and audits
// command line by command line. The view is line-oriented on purpose: the
// output with its escape sequences left out, and a line to type into,
// sent with Enter. Full-screen programs (an editor, top) do not draw
// here; that is what the node's own terminal is for.

import { post } from '../app/api.js';
import { h } from '../lib/dom.js';
import { fromBase64, termText } from '../lib/nodes.js';
import { btn } from './buttons.js';
import { textInput } from './fields.js';

async function nodeCall(node, method, params) {
  const r = await post(`/api/nodes/${encodeURIComponent(node)}/call`, { method, params: params || {} });
  return r.result || {};
}

/**
 * nodeTerminal({node, folder}) opens a terminal and returns {el, close}:
 * el shows it, close ends it (once). onEnd(text) hears when it ended on
 * its own or failed.
 */
export function nodeTerminal({ node, folder, onEnd }) {
  const out = h('pre.term-out', { tabindex: '0', 'aria-live': 'polite', 'aria-label': 'Terminal output' });
  const status = h('p.muted.small.term-status', 'Opening…');
  const input = textInput({ placeholder: 'Type a command, then Enter', 'aria-label': 'Command', autocomplete: 'off', spellcheck: 'false' });
  input.disabled = true;
  const decoder = new TextDecoder();
  let id = null;
  let offset = 0;
  let shown = '';
  let ended = false;
  let exited = false;

  const show = (chunk) => {
    shown = termText(shown, chunk);
    out.textContent = shown;
    out.scrollTop = out.scrollHeight;
  };
  const end = (text) => {
    if (ended) return;
    ended = true;
    input.disabled = true;
    status.textContent = text;
    if (onEnd) onEnd(text);
  };
  const send = async (data) => {
    if (!id || ended) return;
    try {
      await nodeCall(node, 'terminal.send', { id, data });
    } catch (e) {
      status.textContent = `Not sent: ${e.message}`;
    }
  };

  input.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    const line = input.value;
    input.value = '';
    send(`${line}\r`);
  });

  async function readLoop() {
    while (!ended) {
      let o;
      try {
        o = await nodeCall(node, 'terminal.read', { id, offset, wait_ms: 15000 });
      } catch (e) {
        end(`Reading failed: ${e.message}`);
        return;
      }
      if (o.dropped) show('\n[earlier output is gone]\n');
      show(fromBase64(o.data, decoder));
      offset = o.next || offset;
      if (o.closed) {
        exited = true;
        end(`The shell exited${o.exit_code === undefined || o.exit_code === null ? '' : ` with code ${o.exit_code}`}.`);
        return;
      }
    }
  }

  (async () => {
    try {
      const info = await nodeCall(node, 'terminal.open', { folder, cols: 120, rows: 32 });
      id = info.id;
      if (ended) {
        // Closed while it opened.
        exited = true;
        await nodeCall(node, 'terminal.close', { id }).catch(() => {});
        return;
      }
      status.textContent = `${info.shell} in ${info.folder} · sandbox ${info.sandbox} · every command line is in the audit log`;
      input.disabled = false;
      input.focus();
      readLoop();
    } catch (e) {
      end(`Could not open: ${e.message}`);
    }
  })();

  const el = h('div.term',
    status,
    out,
    h('div.term-input', input,
      btn('Ctrl-C', { small: true, kind: 'ghost', title: 'Interrupt what runs', onClick: () => send('\x03') }),
      btn('Tab', { small: true, kind: 'ghost', title: 'Complete what is typed', onClick: () => send(`${input.value}\t`).then(() => { input.value = ''; }) })));

  async function close() {
    if (!id) {
      end('Closed.');
      return;
    }
    end('Closed.');
    if (!exited) {
      exited = true;
      try {
        await nodeCall(node, 'terminal.close', { id });
      } catch {
        // It may have ended already; the bridge's audit log has the rest.
      }
    }
  }
  return { el, close };
}
