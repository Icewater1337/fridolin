import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { capSummary, fromBase64, termText } from '../assets/lib/nodes.js';
import { nodeTerminal } from '../assets/components/node-terminal.js';

const bytesB64 = (bytes) => btoa(String.fromCharCode(...bytes));
const b64 = (s) => bytesB64(new TextEncoder().encode(s));

test('terminal output reads as lines', () => {
  assert.equal(termText('', '\x1b[32mok\x1b[0m\r\n'), 'ok\n', 'colours go');
  assert.equal(termText('$ ', 'ab\bc\n'), '$ ac\n', 'a backspace takes back a character');
  assert.equal(termText('', '50%\r100%\n'), '100%\n', 'a carriage return starts the line over');
  assert.equal(termText('', '\x1b]0;title\x07x\x07'), 'x', 'titles and bells go');
  assert.equal(termText('abc', 'def', 4), 'cdef', 'only the end is kept');
  const d = new TextDecoder();
  const bytes = new TextEncoder().encode('é');
  assert.equal(fromBase64(bytesB64(bytes.subarray(0, 1)), d) + fromBase64(bytesB64(bytes.subarray(1)), d), 'é', 'a character split between reads comes out whole');
  assert.equal(fromBase64(''), '');
});

test('agent and terminal in the capability words', () => {
  assert.equal(capSummary({ agent: { roots: ['/a'] }, terminal: { roots: ['/a', '/b'] } }), 'agent (1 folder), terminal (2 folders)');
});

test('a terminal opens, shows output, sends a line and closes', async (t) => {
  installDom();
  noConsoleErrors(t);
  let reads = 0;
  const sent = [];
  let closed = 0;
  let finishRead = () => {};
  serve(t, (path, init) => {
    assert.equal(path, '/api/nodes/n1/call');
    const { method, params } = JSON.parse(init.body);
    if (method === 'terminal.open') {
      assert.deepEqual([params.folder, params.cols, params.rows], ['/w', 120, 32]);
      return { result: { id: 't1', folder: '/w', shell: 'zsh', sandbox: 'sandbox-exec' } };
    }
    if (method === 'terminal.read') {
      reads += 1;
      if (reads === 1) return { result: { id: 't1', data: b64('$ '), offset: 0, next: 2 } };
      assert.equal(params.offset, 2, 'reading goes on where it ended');
      return new Promise((resolve) => { finishRead = () => resolve({ result: { id: 't1', data: '', offset: 2, next: 2, closed: true } }); });
    }
    if (method === 'terminal.send') { sent.push(params.data); return { result: { id: 't1' } }; }
    if (method === 'terminal.close') { closed += 1; finishRead(); return { result: { id: 't1', closed: true } }; }
    throw new Error(`unexpected ${method}`);
  });
  const term = nodeTerminal({ node: 'n1', folder: '/w' });
  document.body.append(term.el);
  await settle();
  assert.match(term.el.textContent, /zsh in \/w · sandbox sandbox-exec/);
  assert.equal(term.el.querySelector('pre').textContent, '$ ');

  const input = term.el.querySelector('input');
  input.value = 'ls';
  input.dispatchEvent(Object.assign(new Event('keydown', { cancelable: true }), { key: 'Enter' }));
  await settle();
  assert.deepEqual(sent, ['ls\r']);
  assert.equal(input.value, '');
  term.el.querySelectorAll('button').find((b) => b.textContent === 'Ctrl-C').click();
  await settle();
  assert.deepEqual(sent, ['ls\r', '\x03']);

  await term.close();
  await term.close();
  await settle();
  assert.equal(closed, 1, 'closed once');
  assert.equal(input.disabled, true);
});

test('a shell that exits says so and is not closed again', async (t) => {
  installDom();
  noConsoleErrors(t);
  let closed = 0;
  serve(t, (path, init) => {
    const { method } = JSON.parse(init.body);
    if (method === 'terminal.open') return { result: { id: 't2', folder: '/w', shell: 'sh', sandbox: 'none' } };
    if (method === 'terminal.read') return { result: { id: 't2', data: b64('bye\n'), offset: 0, next: 4, closed: true, exit_code: 0 } };
    if (method === 'terminal.close') closed += 1;
    return { result: {} };
  });
  const ended = [];
  const term = nodeTerminal({ node: 'n1', folder: '/w', onEnd: (s) => ended.push(s) });
  document.body.append(term.el);
  await settle();
  assert.deepEqual(ended, ['The shell exited with code 0.']);
  await term.close();
  assert.equal(closed, 0);
});
