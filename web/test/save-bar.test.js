import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { saveBar } from '../assets/components/save-bar.js';

test('the save bar shows what is unsaved and offers to discard it', () => {
  installDom();
  let discarded = 0;
  const save = document.createElement('button');
  const bar = saveBar({ save, onDiscard: () => { discarded++; } });
  assert.equal(bar.hidden, true, 'hidden until something changes');
  assert.ok(bar.contains(save));
  bar.show();
  assert.equal(bar.hidden, false);
  assert.match(bar.textContent, /Unsaved changes/);
  bar.show('2 unsaved changes');
  assert.match(bar.textContent, /2 unsaved changes/);
  bar.querySelectorAll('button').find((b) => b.textContent === 'Discard').click();
  assert.equal(discarded, 1);
  bar.hide();
  assert.equal(bar.hidden, true);
  assert.equal(saveBar({ save, onDiscard() {}, floating: true }).classList.contains('floating'), true);
});
