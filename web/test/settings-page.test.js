import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from './fake-dom.js';
import { serve, settle, type, noConsoleErrors } from './serve.js';
import { h } from '../assets/lib/dom.js';
import { createPage } from '../assets/app/page.js';
import { configResource } from '../assets/app/resources.js';
import page from '../assets/pages/settings.js';

/**
 * opts.params and opts.focusAt go to the page's ctx; opts.config is the
 * config.json the bridge holds at first.
 * @param {import('node:test').TestContext} t
 * @param {Pick<import('../assets/app/page.js').PageHooks, 'params' | 'focusAt'> & {config?: Record<string, unknown>}} [opts]
 */
async function openPage(t, { params, focusAt, config = { claude_path: 'claude', brain_model: '', allowed_numbers: ['15551234567'] } } = {}) {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] });
  installDom();
  noConsoleErrors(t);
  const scrolled = [];
  Object.assign(globalThis, { window: { scrollTo: (to) => scrolled.push(to) } });
  t.after(() => { delete globalThis.window; });
  const bridge = { etag: 'e1', config, puts: [], conflicts: 0 };
  serve(t, (path, init) => {
    if (path === '/api/config' && init.method === 'PUT') {
      const body = JSON.parse(init.body);
      bridge.puts.push(body);
      if (bridge.conflicts > 0 || body.etag !== bridge.etag) {
        bridge.conflicts -= 1;
        return { status: 409, body: { error: 'config.json changed' } };
      }
      bridge.config = body.config;
      bridge.etag = `${bridge.etag}+`;
      return { status: 200, body: { etag: bridge.etag, restart_required: [] } };
    }
    if (path.startsWith('/api/config')) return { etag: bridge.etag, path: '/Users/you/.fridolin/config.json', config: bridge.config, effective: bridge.config };
    if (path.startsWith('/api/google/client')) return { configured: false };
    return {};
  });
  const root = h('div');
  document.body.append(root);
  const ctx = createPage({ params, focusAt });
  t.after(() => ctx.destroy());
  page.render(root, ctx);
  await settle();
  const inputFor = (label) => {
    const l = root.querySelectorAll('label').find((x) => x.textContent === label);
    return /** @type {FakeElement} */ (document.getElementById(l.getAttribute('for')));
  };
  const note = () => /** @type {HTMLElement} */ (root.querySelector('.live-form').firstChild);
  const elsewhere = async (change) => {
    bridge.config = { ...bridge.config, ...change };
    bridge.etag = `${bridge.etag}*`;
    await configResource.refresh();
    await settle();
  };
  const save = async () => {
    document.querySelectorAll('.save-bar button').find((b) => b.textContent === 'Save').click();
    await settle(20);
  };
  return { root, ctx, bridge, inputFor, note, elsewhere, save, scrolled };
}

test('a change elsewhere to another setting is no conflict, and is kept', async (t) => {
  const { ctx, bridge, inputFor, note, elsewhere, save } = await openPage(t);
  const input = inputFor('Claude CLI');
  type(input, '/usr/local/bin/claude');
  assert.equal(ctx.dirty(), true);

  await elsewhere({ allowed_numbers: ['15551234567', '15559876543'] }); // the Chats page added one
  assert.equal(inputFor('Claude CLI'), input, 'the edit is kept');
  assert.equal(note().hidden, true, 'not a conflict');

  await save();
  assert.equal(bridge.puts.length, 1);
  assert.deepEqual(bridge.config.allowed_numbers, ['15551234567', '15559876543'], 'the change made elsewhere is kept');
  assert.equal(bridge.config.claude_path, '/usr/local/bin/claude');
  assert.equal(ctx.dirty(), false);
});

test('a change elsewhere to the edited setting is flagged, and not saved over', async (t) => {
  const { bridge, inputFor, note, elsewhere, save } = await openPage(t);
  type(inputFor('Claude CLI'), '/usr/local/bin/claude');
  await elsewhere({ claude_path: '/opt/claude' });
  assert.equal(note().hidden, false);
  assert.equal(inputFor('Claude CLI').value, '/usr/local/bin/claude');
  await save();
  assert.equal(bridge.puts.length, 0, 'nothing saved');
  assert.equal(bridge.config.claude_path, '/opt/claude');
});

test('a save that crosses a change elsewhere saves again on top of it', async (t) => {
  const { bridge, inputFor, save } = await openPage(t);
  type(inputFor('Claude CLI'), '/usr/local/bin/claude');
  // config.json changes after the page's last poll: the first PUT conflicts.
  bridge.config = { ...bridge.config, brain_model: 'opus' };
  bridge.etag = 'e2';
  await save();
  assert.equal(bridge.puts.length, 2, 'refused once, then saved again');
  assert.equal(bridge.config.brain_model, 'opus');
  assert.equal(bridge.config.claude_path, '/usr/local/bin/claude');
});

test('a save refused twice without a clash says to save again, not that it clashed', async (t) => {
  const { ctx, bridge, inputFor, note, save, scrolled } = await openPage(t);
  type(inputFor('Claude CLI'), '/usr/local/bin/claude');
  bridge.conflicts = 2; // config.json keeps changing while the page saves
  await save();
  assert.equal(bridge.puts.length, 2, 'refused, then refused again');
  assert.equal(note().hidden, true);
  assert.deepEqual(scrolled, [], 'no scrolling up to a note that is not there');
  const shown = document.querySelectorAll('.toast').map((el) => el.textContent);
  assert.ok(shown.some((m) => /Save once more/.test(m)), `toasts: ${shown}`);
  assert.ok(!shown.some((m) => /also changed somewhere else/.test(m)), `toasts: ${shown}`);
  assert.equal(ctx.dirty(), true, 'the edit is kept for the next try');

  await save();
  assert.equal(bridge.config.claude_path, '/usr/local/bin/claude');
});

// Settings such as queue.max_pending live in objects of config.json: an
// edit goes into its object, made if it is missing, beside what is there.
test('an edited nested setting is saved inside its object, beside the others', async (t) => {
  const { bridge, inputFor, save } = await openPage(t, { config: { claude_path: 'claude', queue: { mode: 'interrupt', notify: false } } });
  type(inputFor('Waiting messages per brain'), '5');
  const sandbox = inputFor('Sandbox brains');
  sandbox.checked = true;
  sandbox.dispatchEvent(new Event('change', { bubbles: true }));
  await save();
  assert.equal(bridge.puts.length, 1);
  assert.deepEqual(bridge.puts[0].config, {
    claude_path: 'claude',
    queue: { mode: 'interrupt', notify: false, max_pending: 5 },
    sandbox: { enabled: true },
  });
});

test('a dangerous setting says why in a warning beside it', async (t) => {
  const { inputFor } = await openPage(t);
  const field = inputFor('Let brains use tools without asking').closest('.field');
  const warn = field.querySelector('.callout.warn.inline-callout');
  assert.ok(warn, 'the help is a warning');
  // Without it, a run is refused the tools that need approval; it does
  // not wait forever for one.
  assert.match(warn.textContent, /Off: .*every tool that needs approval is refused/);
  assert.doesNotMatch(warn.textContent, /waits forever/);
  assert.match(warn.textContent, /Codex mode uses its read-only sandbox/);
  assert.match(warn.textContent, /The sandbox \(below\) limits what a run can reach either way/);
  assert.equal(field.querySelector('.help'), null, 'and is not repeated as plain help');
});

test('a link to a section gives the focus to its heading', async (t) => {
  for (const params of [{ section: 'claude' }, { section: 'google' }, { section: 'nope' }, {}]) {
    await t.test(params.section || 'no section', async (t) => {
      const asked = [];
      await openPage(t, { params, focusAt: (el) => asked.push(el.textContent) });
      const want = { claude: ['AI mode'], google: ['Google sign-in'] }[params.section] || [];
      assert.deepEqual(asked, want);
    });
  }
});

test('AI mode defaults to Claude and preserves separate model settings when switching', async (t) => {
  const { bridge, inputFor, save } = await openPage(t, { config: { claude_path: 'claude', brain_model: 'opus', brain_effort: 'max' } });
  assert.equal(inputFor('Mode').value, 'claude');
  assert.equal(inputFor('Codex model').getAttribute('placeholder'), 'gpt-6.1-sol');
  assert.equal(inputFor('Codex effort').value, 'high');
  const mode = inputFor('Mode');
  mode.value = 'codex';
  mode.dispatchEvent(new Event('change'));
  await save();
  assert.equal(bridge.config.agent_provider, 'codex');
  assert.equal(bridge.config.brain_model, 'opus');
  assert.equal(bridge.config.brain_effort, 'max');
  inputFor('Mode').value = 'claude';
  inputFor('Mode').dispatchEvent(new Event('change'));
  await save();
  assert.equal(bridge.config.agent_provider, 'claude');
});
