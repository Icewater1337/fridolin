// Text must stay readable in both themes: every pair of text and
// background colour the stylesheet uses passes WCAG AA (4.5:1) with the
// light and with the dark value of its tokens. The pairs are the rules
// that set both a color and a background from tokens, and the ones
// text sits on without setting a background of its own.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const CSS = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../assets/css');
const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '');

// name → [light, dark] for each colour token.
const tokens = new Map();
for (const m of strip(fs.readFileSync(path.join(CSS, 'tokens.css'), 'utf8')).matchAll(/(--[\w-]+):\s*light-dark\((#[0-9a-f]{6}),\s*(#[0-9a-f]{6})\);/gi)) {
  tokens.set(m[1], [m[2], m[3]]);
}

function luminance(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// Text on the page, a card or a panel, which set only a background.
const GROUNDS = ['--bg', '--surface', '--surface-2'];
const ON_GROUNDS = ['--text', '--text-2', '--muted', '--accent', '--ok', '--warn', '--bad', '--info'];
const ON_LOG = ['--log-text', '--log-muted', '--log-err', '--log-warn', '--log-dev'];

function pairsInRules() {
  const pairs = [];
  for (const file of fs.readdirSync(CSS).filter((f) => f.endsWith('.css'))) {
    for (const m of strip(fs.readFileSync(path.join(CSS, file), 'utf8')).matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const fg = m[2].match(/(?:^|;)\s*color:\s*var\((--[\w-]+)\)/);
      const bg = m[2].match(/(?:^|;)\s*background(?:-color)?:\s*var\((--[\w-]+)\)/);
      if (fg && bg) pairs.push({ where: `${file}: ${m[1].trim()}`, fg: fg[1], bg: bg[1] });
    }
  }
  return pairs;
}

test('the tokens are read', () => {
  assert.ok(tokens.size > 30, `only ${tokens.size} colour tokens found`);
  assert.equal(contrast('#ffffff', '#000000').toFixed(1), '21.0');
});

test('every text colour passes WCAG AA on its background in both themes', async (t) => {
  const pairs = [
    ...pairsInRules(),
    ...GROUNDS.flatMap((bg) => ON_GROUNDS.map((fg) => ({ where: 'text on the page and cards', fg, bg }))),
    ...ON_LOG.map((fg) => ({ where: 'the log view', fg, bg: '--log-bg' })),
  ];
  assert.ok(pairs.some((p) => p.where.includes('.btn.danger')), 'the danger button sets its text and background from tokens');
  for (const { where, fg, bg } of pairs) {
    await t.test(`${fg} on ${bg} (${where})`, () => {
      assert.ok(tokens.has(fg) && tokens.has(bg), `${fg} and ${bg} must be light-dark() colour tokens`);
      ['light', 'dark'].forEach((theme, i) => {
        const ratio = contrast(tokens.get(fg)[i], tokens.get(bg)[i]);
        assert.ok(ratio >= 4.5, `${theme}: ${tokens.get(fg)[i]} on ${tokens.get(bg)[i]} is ${ratio.toFixed(2)}:1, below 4.5:1`);
      });
    });
  }
});
