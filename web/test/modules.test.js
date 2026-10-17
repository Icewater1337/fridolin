// The dashboard has no build step or linter, so nothing else notices a
// broken import before a browser does, and one broken module stops the
// whole page. These tests read every module and check the imports resolve,
// are used, and keep the layers: lib/ stands alone, components/ never reach
// into pages/, and pages never import each other.

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
          assert.match(body, new RegExp(`\\b${local.replace(/\$/g, '\\$')}\\b`), `${local} is imported but not used`);
        }
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
