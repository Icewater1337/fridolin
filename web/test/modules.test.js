// The dashboard has no build step, and ESLint (make lint-web) reads one
// file at a time, so nothing else notices a broken import before a browser
// does, and one broken module stops the whole page. These tests read every
// module and check the imports resolve, are used, and keep the layers:
// lib/ stands alone, components/ never reach into pages/, and pages never
// import each other. Every export is imported somewhere, so none is left
// behind by code that is gone.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const ASSETS = path.join(WEB, 'assets');

function walk(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    return e.isDirectory() ? walk(p) : p.endsWith('.js') ? [p] : [];
  });
}

const modules = walk(ASSETS).map((file) => {
  const src = fs.readFileSync(file, 'utf8');
  const exports = new Set();
  for (const m of src.matchAll(/^export\s+(?:async\s+)?(?:function\*?|const|let|class)\s+([\w$]+)/gm)) exports.add(m[1]);
  if (/^export default/m.test(src)) exports.add('default');
  const imports = [];
  for (const m of src.matchAll(/^import\s*(?:\{([^}]*)\}|([\w$]+))\s*from\s*'([^']+)';/gm)) {
    const names = m[1] ? m[1].split(',').map((n) => n.trim().split(/\s+as\s+/)[0]).filter(Boolean) : ['default'];
    imports.push({ spec: m[3], names });
  }
  for (const m of src.matchAll(/\bimport\('([^']+)'\)/g)) imports.push({ spec: m[1], names: ['default'] });
  return { file, rel: path.relative(ASSETS, file), src, exports, imports };
});
const byFile = new Map(modules.map((m) => [m.file, m]));
const layer = (rel) => rel.split(path.sep)[0];
// Of these, only $ can be in a JavaScript name, but escaping them all keeps
// the pattern right whatever the import list holds.
const escapeRegExp = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

test('every import resolves to an export', async (t) => {
  for (const m of modules) {
    await t.test(m.rel, () => {
      for (const { spec, names } of m.imports) {
        assert.match(spec, /^\.\.?\//, `${spec}: only relative imports work without a build step`);
        const target = byFile.get(path.resolve(path.dirname(m.file), spec));
        assert.ok(target, `${spec} does not exist`);
        for (const n of names) assert.ok(target.exports.has(n), `${spec} has no export ${n}`);
      }
    });
  }
});

test('every imported name is used', async (t) => {
  for (const m of modules) {
    await t.test(m.rel, () => {
      const body = m.src.replace(/^import\s[\s\S]*?;$/gm, '');
      for (const im of m.src.matchAll(/^import\s*\{([^}]*)\}\s*from/gm)) {
        for (const local of im[1].split(',').map((n) => n.trim().split(/\s+as\s+/).pop()).filter(Boolean)) {
          assert.match(body, new RegExp(`\\b${escapeRegExp(local)}\\b`), `${local} is imported but not used`);
        }
      }
    });
  }
});

// The tests import from the modules too: an export only a test uses is a
// pure helper kept apart so it can be tested.
const testImports = fs.readdirSync(path.join(WEB, 'test')).filter((f) => f.endsWith('.js')).flatMap((f) => {
  const file = path.join(WEB, 'test', f);
  const src = fs.readFileSync(file, 'utf8');
  return [...src.matchAll(/^import\s*(?:\{([^}]*)\}|([\w$]+))\s*from\s*'([^']+)';/gm)]
    .map((m) => ({ target: path.resolve(path.dirname(file), m[3]), names: m[1] ? m[1].split(',').map((n) => n.trim().split(/\s+as\s+/)[0]).filter(Boolean) : ['default'] }));
});

// Modules nothing imports, which the page loads itself (index.html).
const ENTRY_POINTS = new Set([path.join('app', 'main.js')]);

test('every export is imported somewhere', async (t) => {
  const used = new Map(modules.map((m) => [m.file, new Set()]));
  const all = [
    ...modules.flatMap((m) => m.imports.map(({ spec, names }) => ({ target: path.resolve(path.dirname(m.file), spec), names }))),
    ...testImports,
  ];
  for (const { target, names } of all) if (used.has(target)) names.forEach((n) => used.get(target).add(n));
  for (const m of modules) {
    if (ENTRY_POINTS.has(m.rel)) continue;
    await t.test(m.rel, () => {
      for (const name of m.exports) {
        assert.ok(used.get(m.file).has(name), `${name} is exported but no module or test imports it: drop the export, or the code`);
      }
    });
  }
});

test('modules keep their layers', async (t) => {
  const allowed = {
    lib: ['lib'],
    components: ['lib', 'components', 'app'],
    pages: ['lib', 'components', 'app'],
    app: ['lib', 'components', 'app', 'pages'],
  };
  for (const m of modules) {
    if (!m.rel.includes(path.sep)) continue; // theme.js runs before the modules
    await t.test(m.rel, () => {
      const own = layer(m.rel);
      assert.ok(allowed[own], `${m.rel} is in no known folder`);
      for (const { spec } of m.imports) {
        const to = layer(path.relative(ASSETS, path.resolve(path.dirname(m.file), spec)));
        assert.ok(allowed[own].includes(to), `${m.rel} imports ${spec}: ${own}/ may not import ${to}/`);
      }
    });
  }
});

test('index.html loads files that exist', () => {
  const html = fs.readFileSync(path.join(WEB, 'index.html'), 'utf8');
  const refs = [...html.matchAll(/(?:src|href)="\/([^"]+)"/g)].map((m) => m[1]);
  assert.ok(refs.includes('assets/app/main.js'), 'the entry module is not loaded');
  for (const r of refs) assert.ok(fs.existsSync(path.join(WEB, r)), `${r} does not exist`);
});

// The bridge serves the dashboard with a Content-Security-Policy that
// allows no inline script or style: one in the page would be refused, and
// what it does would silently not happen. inlineCode lists what in html
// the policy would refuse.
function inlineCode(html) {
  const found = [];
  // A browser ends a script at </script followed by anything up to the >,
  // such as </script foo>, so the end tag allows that too.
  for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script\b[^>]*>/gi)) {
    if (!/\ssrc="/.test(m[1])) found.push(`a script without src: ${m[0]}`);
    if (m[2].trim() !== '') found.push(`a script with a body: ${m[0]}`);
  }
  if (/<style\b/i.test(html)) found.push('a <style> element');
  for (const m of html.matchAll(/<[a-z][\w-]*\b([^>]*)>/gi)) {
    if (/(^|\s)style\s*=/i.test(m[1])) found.push(`a style attribute: ${m[0]}`);
    if (/(^|\s)on[a-z]+\s*=/i.test(m[1])) found.push(`an event handler attribute: ${m[0]}`);
  }
  return found;
}

test('index.html has no inline script or style', () => {
  assert.deepEqual(inlineCode(fs.readFileSync(path.join(WEB, 'index.html'), 'utf8')), []);
});

// The check on pages that break each rule, so a check that misses one
// fails here rather than passing index.html.
test('inlineCode finds each kind of inline script and style', async (t) => {
  const cases = [
    ['scripts loaded by src', '<script src="/a.js"></script>\n<script type="module" src="/b.js"></script>', []],
    ['an inline script', '<script>run()</script>', ['a script without src', 'a script with a body']],
    ['a body beside src', '<script src="/a.js">run()</script>', ['a script with a body']],
    ['an end tag with attributes', '<script>run()</script\tfoo>', ['a script without src', 'a script with a body']],
    ['upper case and a newline', '<SCRIPT>run()</SCRIPT\n>', ['a script without src', 'a script with a body']],
    ['a style element', '<style>p { color: red }</style>', ['a <style> element']],
    ['a style attribute', '<p style="color: red">x</p>', ['a style attribute']],
    ['an event handler attribute', '<button onclick="go()">Go</button>', ['an event handler attribute']],
    ['names that only contain style or on', '<p data-style="a" data-onclick="b" class="onward">x</p>', []],
  ];
  for (const [name, html, want] of cases) {
    await t.test(name, () => assert.deepEqual(inlineCode(html).map((f) => f.split(':')[0]), want));
  }
});
