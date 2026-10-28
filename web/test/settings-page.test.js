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

// fride.instances: one box per instance. A new one is named once; a
// saved one keeps its name. What the bridge would refuse is flagged and
// not saved; what is saved sits beside fride.news.
test('FrIDE instances are added, edited and removed in fride.instances', async (t) => {
  const work = {
    description: 'work repositories',
    repos: ['https://gitlab.example.com/group/app.git'],
    identities: ['gitlab'],
    egress: ['registry.example.com'],
    nodes: ['mac-mini'],
  };
  const { ctx, root, bridge, save } = await openPage(t, { config: { claude_path: 'claude', fride: { news: 'all', instances: { work } } } });
  // A save builds the form again, from the settings saved.
  const editor = () => root.querySelector('.fride-instances-editor');
  const boxes = () => editor().querySelectorAll('.fride-instance-edit');
  /** @type {(box: FakeElement, label: string) => FakeElement} */
  const inBox = (box, label) => {
    const l = box.querySelectorAll('label').find((x) => x.textContent === label);
    return /** @type {FakeElement} */ (document.getElementById(l.getAttribute('for')));
  };
  const tick = (box, label, on) => {
    const cb = inBox(box, label);
    cb.checked = on;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const click = (el, label) => el.querySelectorAll('button').find((b) => b.textContent === label).click();

  assert.equal(boxes().length, 1);
  assert.equal(boxes()[0].querySelector('h3').textContent, 'work');
  assert.equal(inBox(boxes()[0], 'Repositories').value, 'https://gitlab.example.com/group/app.git');
  assert.equal(inBox(boxes()[0], 'GitLab').checked, true);
  assert.equal(inBox(boxes()[0], 'GitHub').checked, false);
  assert.equal(ctx.dirty(), false, 'a form nobody touched holds what the file holds');

  click(editor(), 'Add instance');
  await settle();
  assert.equal(boxes().length, 2);
  const added = boxes()[1];
  assert.equal(ctx.dirty(), true);
  type(inBox(added, 'Name'), 'Work');
  assert.match(added.textContent, /A lower-case letter, then up to 23/);
  type(inBox(added, 'Name'), 'work');
  assert.match(added.textContent, /Another instance has this name/);
  type(inBox(added, 'Name'), 'native');
  assert.match(added.textContent, /“native” is the name of a node’s own FrIDE/);
  type(inBox(added, 'Name'), 'fridolin');
  type(inBox(added, 'Description'), 'fridolin and FrIDE themselves');
  type(inBox(added, 'Repositories'), 'https://github.com/you/fridolin.git\n\n https://github.com/you/fride.git \n');
  tick(added, 'GitHub', true);
  type(inBox(added, 'More hosts it may reach'), 'https://registry.example.com');
  type(inBox(added, 'Memory (MB)'), '1000');
  type(inBox(added, 'Stop when idle for (minutes)'), '-1');
  assert.match(added.textContent, /no host name: no scheme, path or port/);
  assert.match(added.textContent, /0, or from 2048 to 65536 MB/);

  await save();
  assert.equal(bridge.puts.length, 0, 'nothing is saved while an entry is wrong');
  assert.ok(document.querySelectorAll('.toast').some((el) => /Check “FrIDE instances”/.test(el.textContent)));

  type(inBox(added, 'More hosts it may reach'), '');
  type(inBox(added, 'Memory (MB)'), '8192');
  type(inBox(boxes()[0], 'Description'), 'work repositories, on the company GitLab');
  await save();
  assert.equal(bridge.puts.length, 1);
  assert.deepEqual(bridge.config.fride, {
    news: 'all',
    instances: {
      work: { ...work, description: 'work repositories, on the company GitLab' },
      fridolin: {
        description: 'fridolin and FrIDE themselves',
        repos: ['https://github.com/you/fridolin.git', 'https://github.com/you/fride.git'],
        identities: ['github'],
        memory_mb: 8192,
        idle_stop_minutes: -1,
      },
    },
  });
  assert.equal(ctx.dirty(), false);

  // Saved, the new instance keeps its name.
  const saved = boxes().find((b) => b.querySelector('h3') && b.querySelector('h3').textContent === 'fridolin');
  assert.ok(saved, 'fridolin is a saved instance now');
  click(boxes().find((b) => b.querySelector('h3').textContent === 'work'), 'Remove');
  await settle();
  assert.equal(ctx.dirty(), true);
  await save();
  assert.deepEqual(Object.keys(bridge.config.fride.instances), ['fridolin']);

  click(boxes()[0], 'Remove');
  await settle();
  assert.match(editor().textContent, /None yet/);
  await save();
  assert.deepEqual(bridge.config.fride, { news: 'all' }, 'no instances left: the key goes, fride.news stays');
});
