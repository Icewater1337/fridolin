import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, noConsoleErrors } from './serve.js';
import { joinPath, nodeFolderPicker } from '../assets/components/node-folder-picker.js';

test('paths join in the node\'s own spelling', () => {
  assert.equal(joinPath('D:\\', 'Backups', '\\'), 'D:\\Backups');
  assert.equal(joinPath('D:\\Backups', 'fridolin', '\\'), 'D:\\Backups\\fridolin');
  assert.equal(joinPath('/Volumes/Backup', 'fridolin', '/'), '/Volumes/Backup/fridolin');
  assert.equal(joinPath('/', 'mnt'), '/mnt');
});

test('the picker lists the drives, then folder names, and picks one', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const calls = serve(t, (path, init) => {
    const body = JSON.parse(init.body || '{}');
    if (body.method === 'fs.roots') return { result: { sep: '\\', roots: [{ path: 'D:\\', label: 'D:', removable: true, free: 2 ** 40 }] } };
    if (body.method === 'fs.browse') return { result: { path: body.params.path, folders: ['Backups', 'Photos'] } };
    return {};
  });
  let picked = '';
  const el = nodeFolderPicker({ node: 'a1b2c3d4e5f6', onPick: (p) => { picked = p; } });
  document.body.append(el);
  await settle();
  assert.ok(el.textContent.includes('D:\\'), el.textContent);
  assert.ok(el.textContent.includes('removable'));
  el.querySelectorAll('button.fb-item')[0].click();
  await settle();
  assert.ok(calls.some((c) => c.path === '/api/nodes/a1b2c3d4e5f6/call' && c.body.method === 'fs.browse' && c.body.params.path === 'D:\\'), 'browses the drive');
  assert.ok(el.textContent.includes('Backups') && el.textContent.includes('Photos'), el.textContent);
  el.querySelectorAll('button.fb-item')[0].click();
  await settle();
  el.querySelectorAll('button').find((b) => b.textContent === 'Use this folder').click();
  assert.equal(picked, 'D:\\Backups');
});
