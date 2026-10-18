// The stylesheet has no build step or linter either. These tests hold it
// to its layout: app.css only orders the layers and imports one file into
// each, the tokens hold each colour once with its light and dark value,
// and no file says the same thing twice (one rule per selector and one
// media query per breakpoint), so a change goes where the rule lives
// instead of into an override further down.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ASSETS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../assets');
const CSS = path.join(ASSETS, 'css');
const read = (file) => fs.readFileSync(file, 'utf8');
const files = fs.readdirSync(CSS).filter((f) => f.endsWith('.css')).sort();

/**
 * The rules of a stylesheet: {at, selector, body} for each style rule,
 * where at lists the at-rules (e.g. @media) it sits in, and {statement}
 * for the at-rules that end in a semicolon (@layer, @import).
 */
function parseCSS(src) {
  const css = src.replace(/\/\*[\s\S]*?\*\//g, '');
  const out = [];
  const stack = [];
  let text = '';
  for (const ch of css) {
    if (ch === '{') {
      stack.push(text.trim());
      text = '';
    } else if (ch === '}') {
      const prelude = stack.pop();
      if (!prelude.startsWith('@')) {
        out.push({ at: stack.filter((p) => p.startsWith('@')), selector: prelude.replace(/\s+/g, ' '), body: text.trim() });
      }
      text = '';
    } else if (ch === ';' && !stack.some((p) => !p.startsWith('@'))) {
      if (text.trim().startsWith('@')) out.push({ statement: text.trim().replace(/\s+/g, ' ') });
      text = '';
    } else {
      text += ch;
    }
  }
  return out;
}

test('app.css imports each file of css/ into a layer of its own, in layer order', () => {
  const rules = parseCSS(read(path.join(ASSETS, 'app.css')));
  assert.ok(rules.every((r) => r.statement), 'app.css holds no rules of its own');
  const order = rules[0].statement.match(/^@layer (.+)$/);
  assert.ok(order, 'the first statement names the layers in order');
  const layers = order[1].split(',').map((s) => s.trim());
  const imports = rules.slice(1).map((r) => r.statement.match(/^@import url\("css\/([\w-]+\.css)"\) layer\(([\w-]+)\)$/));
  assert.ok(imports.every(Boolean), `every other statement imports a file into a layer: ${rules.slice(1).map((r) => r.statement).join('; ')}`);
  assert.deepEqual(imports.map((m) => m[2]), layers, 'one import per layer, in the same order');
  assert.deepEqual(imports.map((m) => m[1]), layers.map((l) => `${l}.css`), 'each layer is the file of its name');
  assert.deepEqual(imports.map((m) => m[1]).sort(), files, 'and every file in css/ is imported');
});

test('each colour token has its light and its dark value in one place', () => {
  const rules = parseCSS(read(path.join(CSS, 'tokens.css')));
  const root = rules.filter((r) => r.selector === ':root' && !r.at.length);
  assert.equal(root.length, 1, 'one :root block');
  for (const decl of root[0].body.split(';').map((d) => d.trim()).filter(Boolean)) {
    const [name, value] = [decl.slice(0, decl.indexOf(':')).trim(), decl.slice(decl.indexOf(':') + 1).trim()];
    if (!/#[0-9a-f]{3,8}\b|rgba?\(/i.test(value)) continue; // fonts, sizes
    const bare = value.replace(/light-dark\((?:[^()]|\([^()]*\))*\)/g, '');
    assert.doesNotMatch(bare, /#[0-9a-f]{3,8}\b|rgba?\(/i, `${name}: ${value} is not light-dark(light, dark)`);
  }
  for (const file of files) {
    const src = read(path.join(CSS, file));
    assert.doesNotMatch(src, /prefers-color-scheme/, `${file}: the theme is color-scheme's job`);
    for (const r of parseCSS(src)) {
      if (r.selector && /data-theme/.test(r.selector)) {
        assert.match(r.body, /^color-scheme: (light|dark);?$/, `${file}: ${r.selector} may only pick the scheme`);
      }
    }
  }
});

test('colours come only from the tokens', async (t) => {
  for (const file of files.filter((f) => f !== 'tokens.css')) {
    await t.test(file, () => {
      // A data: URL may spell a colour (%23…), as an image it has no other way to.
      const src = read(path.join(CSS, file)).replace(/\/\*[\s\S]*?\*\//g, '').replace(/url\([^)]*\)/g, 'url()');
      const found = src.match(/#[0-9a-f]{3,8}\b|\b(?:rgba?|hsla?)\(/gi);
      assert.equal(found, null, `use a token from tokens.css instead of ${found}`);
    });
  }
});

// 100vh is the height with the browser's toolbars hidden, so on a phone
// the bottom of a full-height layout sits under them; dvh follows them.
test('heights follow the visible viewport', async (t) => {
  for (const file of files) {
    await t.test(file, () => {
      const src = read(path.join(CSS, file)).replace(/\/\*[\s\S]*?\*\//g, '');
      assert.doesNotMatch(src, /\d(?:vh|svh|lvh)\b/, 'use dvh');
      assert.doesNotMatch(src, /calc\(\s*100dvh\s*-\s*\d{3,}px\s*\)/, 'a page part that fills the screen grows into the space left (flex), it does not subtract the height of what is above it');
    });
  }
});

test('no file declares a selector twice or repeats a breakpoint', async (t) => {
  for (const file of files) {
    await t.test(file, () => {
      const rules = parseCSS(read(path.join(CSS, file)));
      const seen = new Set();
      for (const r of rules) {
        if (!r.selector || r.at.some((a) => a.startsWith('@keyframes'))) continue;
        const key = `${r.at.join(' ')} ${r.selector}`;
        assert.ok(!seen.has(key), `${r.at.join(' ')} ${r.selector} is declared twice`);
        seen.add(key);
      }
      const media = [...read(path.join(CSS, file)).matchAll(/@media ([^{]+)\{/g)].map((m) => m[1].trim());
      assert.deepEqual(media, [...new Set(media)], 'one @media block per condition');
    });
  }
});
