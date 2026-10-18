// Runs theme.js, the classic script the page runs before its modules, in
// the fake DOM (installDom first): it applies the saved theme and leaves
// window.fridolinTheme, which the shell's theme buttons use. saved is what
// localStorage holds, blocked makes every storage call throw; storage is
// what it holds afterwards.

import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../assets/theme.js', import.meta.url), 'utf8');

export function runThemeScript(t, { saved = null, blocked = false } = {}) {
  const storage = new Map(saved === null ? [] : [['bridgeTheme', saved]]);
  const refuse = () => { throw new DOMException('The operation is insecure.', 'SecurityError'); };
  const localStorage = blocked
    ? { getItem: refuse, setItem: refuse }
    : { getItem: (k) => (storage.has(k) ? storage.get(k) : null), setItem: (k, v) => { storage.set(k, String(v)); } };
  const win = { addEventListener() {} };
  vm.runInNewContext(source, { window: win, document: globalThis.document, localStorage, setTimeout });
  globalThis.window = { fridolinTheme: win.fridolinTheme };
  t.after(() => { delete globalThis.window; });
  return storage;
}
