import { test } from 'node:test';
import assert from 'node:assert/strict';
import { BRAIN_ID_RE, BRIDGE_BACKUP_ID, slugify, routingSummary, brainLabel, backupOwner, issueLabel, runtimeName, runtimeState, egressProblem, sandboxWeaker } from '../assets/lib/brain.js';
import { installDom } from './fake-dom.js';
import { brainStatusPill, runtimeSummary, sandboxFact, sandboxPill } from '../assets/components/brain.js';
import { h } from '../assets/lib/dom.js';

test('slugify', async (t) => {
  const cases = [
    ['Personal', 'personal'],
    ['My Work Brain', 'my-work-brain'],
    ['  --Mixed__Case.name  ', 'mixed-case-name'],
    ['Ünïcödé', 'ncd'],
    ['!!!', 'brain'],
    ['', 'brain'],
    ['a'.repeat(39) + ' b', 'a'.repeat(39)],
  ];
  for (const [input, want] of cases) {
    await t.test(JSON.stringify(input), () => {
      const got = slugify(input);
      assert.equal(got, want);
      assert.match(got, BRAIN_ID_RE);
    });
  }
});

test('BRAIN_ID_RE', async (t) => {
  /** @type {[string, boolean][]} */
  const cases = [['personal', true], ['a_b-1', true], ['-lead', false], ['Upper', false], ['a'.repeat(40), true], ['a'.repeat(41), false]];
  for (const [id, want] of cases) {
    await t.test(id, () => assert.equal(BRAIN_ID_RE.test(id), want));
  }
});

test('routingSummary', async (t) => {
  /** @type {[string, Parameters<typeof routingSummary>[0], string][]} */
  const cases = [
    ['none', null, 'No chats'],
    ['empty', {}, 'No chats routed yet'],
    ['DMs', { handles_dms: true }, 'Direct messages'],
    ['everything', { handles_dms: true, default: true, groups: ['x', 'y'] }, 'Direct messages · Default for other chats · 2 groups'],
    ['one group', { groups: ['x'] }, '1 group'],
  ];
  for (const [name, routing, want] of cases) {
    await t.test(name, () => assert.equal(routingSummary(routing), want));
  }
});

test('brainLabel', async (t) => {
  /** @type {[string, Parameters<typeof brainLabel>[0], string][]} */
  const cases = [
    ['a name of its own', { id: 'work', name: 'Office' }, 'Office (work)'],
    ['the name is the id', { id: 'work', name: 'work' }, 'work'],
    ['no name', { id: 'work' }, 'work'],
  ];
  for (const [name, b, want] of cases) {
    await t.test(name, () => assert.equal(brainLabel(b), want));
  }
});

test('backupOwner', async (t) => {
  const brains = [{ id: 'work', name: 'Office' }, { id: 'notes' }];
  const cases = [
    ['the bridge', BRIDGE_BACKUP_ID, 'Bridge settings'],
    ['a brain with a name', 'work', 'Office'],
    ['a brain without one', 'notes', 'notes'],
    ['a brain that is gone', 'old', 'old'],
  ];
  for (const [name, id, want] of cases) {
    await t.test(name, () => assert.equal(backupOwner(id, brains), want));
  }
  assert.equal(BRAIN_ID_RE.test(BRIDGE_BACKUP_ID), false, 'no brain can have the bridge’s id');
});

test('one name per issue kind, short on a pill', async (t) => {
  installDom();
  const cases = [
    // kind, long, short (the status pill)
    ['drift', 'brain.json changed on disk', 'Changed on disk'],
    ['missing', 'Brain folder missing', 'Folder missing'],
    ['invalid', 'Invalid brain configuration', 'Invalid'],
    ['unapproved', 'Brain not approved', 'Not approved'],
    ['someday', 'someday', 'someday'],
  ];
  for (const [kind, long, short] of cases) {
    await t.test(kind, () => {
      assert.equal(issueLabel(kind), long);
      assert.equal(issueLabel(kind, { short: true }), short);
      assert.equal(brainStatusPill(kind).textContent, short);
    });
  }
  assert.equal(brainStatusPill('ok').textContent, 'Active');
});

test('runtimeSummary', async (t) => {
  installDom();
  // Runtimes with only what the summary reads.
  /** @type {(o: object) => Parameters<typeof runtimeSummary>[0]} */
  const runtime = (o) => /** @type {Parameters<typeof runtimeSummary>[0]} */ (/** @type {unknown} */ (o));
  /** @type {[string, Parameters<typeof runtimeSummary>[0], string][]} */
  const cases = [
    ['idle, never ran', runtime({}), 'IdleNo runs yet'],
    ['working with a queue', runtime({ running: { preview: 'hi' }, queue: [{}, {}], last: { outcome: 'ok', finished: '' } }), 'Working2 queuedRepliednever'],
    ['the last run failed', runtime({ last: { outcome: 'error', error: 'exit 1' } }), 'IdleFailednever'],
  ];
  for (const [name, rt, want] of cases) {
    await t.test(name, () => assert.equal(h('div', runtimeSummary(rt)).textContent, want));
  }
});

// Where a brain's runs happen (#149), as /api/brains gives it.
const where = (o) => ({ configured: 'docker-sandbox', effective: 'docker-sandbox', fallback: 'bubblewrap', node: 'Mac mini', ready: true, reason: '', egress: [], ...o });

test('runtimeState says where a brain runs, and never says bubblewrap', async (t) => {
  /** @type {[string, object | null, string | null, string | null][]} */
  const cases = [
    // name, the view, the sentence, the pill
    ['in a Docker Sandbox', where({}), 'Runs in a Docker Sandbox on Mac mini', 'Docker Sandbox'],
    ['in a Docker Sandbox on no named node', where({ node: '' }), 'Runs in a Docker Sandbox', 'Docker Sandbox'],
    ['in Fridolin’s sandbox, as set', where({ configured: 'bubblewrap', effective: 'bubblewrap', ready: false, reason: 'sbx is not installed' }), 'Runs in Fridolin’s sandbox', 'Fridolin’s sandbox'],
    ['falling back', where({ effective: 'bubblewrap', ready: false, reason: 'the Mac mini is offline since 14:02' }), 'Docker Sandbox set, but runs in Fridolin’s sandbox now: the Mac mini is offline since 14:02', 'Fallback'],
    ['falling back without a reason', where({ effective: 'bubblewrap', ready: false }), 'Docker Sandbox set, but runs in Fridolin’s sandbox now', 'Fallback'],
    ['refusing', where({ fallback: 'refuse', ready: false, reason: 'sbx is not signed in' }), 'Docker Sandbox set, but runs are refused until it is back: sbx is not signed in', 'Refusing runs'],
    ['from a bridge without the view', null, null, null],
  ];
  for (const [name, rs, text, pill] of cases) {
    await t.test(name, () => {
      const st = runtimeState(/** @type {any} */ (rs));
      assert.equal(st ? st.text : null, text);
      assert.equal(st ? st.pill : null, pill);
      assert.doesNotMatch(st ? st.text : '', /bubblewrap/);
    });
  }
  assert.equal(runtimeName('docker-sandbox'), 'Docker Sandbox');
  assert.equal(runtimeName(''), 'Fridolin’s sandbox');
  assert.equal(runtimeName(undefined), 'Fridolin’s sandbox');
});

test('a brain’s runtime as a pill and a sentence, with the sites it may reach', () => {
  installDom();
  const pillOf = (rs) => sandboxPill(rs);
  assert.equal(pillOf(where({})).className, 'pill ok');
  assert.equal(pillOf(where({ effective: 'bubblewrap', ready: false })).className, 'pill warn');
  assert.equal(pillOf(where({ fallback: 'refuse', ready: false })).className, 'pill bad');
  assert.equal(pillOf(where({ configured: 'bubblewrap', effective: 'bubblewrap' })).className, 'pill neutral');
  assert.equal(pillOf(where({ reason: 'x' })).getAttribute('title'), 'Runs in a Docker Sandbox on Mac mini');
  assert.equal(sandboxPill(undefined), null);
  assert.equal(sandboxFact(undefined), null);
  assert.equal(sandboxFact(where({ egress: ['api.github.com', '*.wikipedia.org'] })).textContent,
    'Runs in a Docker Sandbox on Mac mini· also reaches api.github.com, *.wikipedia.org');
  assert.equal(sandboxFact(where({ configured: 'bubblewrap', effective: 'bubblewrap', egress: ['api.github.com'] })).textContent,
    'Runs in Fridolin’s sandbox', 'the sites matter only in a Docker Sandbox');
});

test('egressProblem takes hosts and patterns as sbx does', () => {
  for (const ok of ['api.github.com', '*.wikipedia.org', '**:443', 'localhost:8765', 'example.com:*', '']) {
    assert.equal(egressProblem(ok), '', ok);
  }
  assert.match(egressProblem('https://example.com'), /without https:\/\//);
  assert.match(egressProblem('example.com/path'), /without https:\/\/ or a path/);
  assert.match(egressProblem('two words'), /A host such as/);
  assert.match(egressProblem('example.com:http'), /A host such as/);
});

test('sandboxWeaker names what a brain.json on disk gives up', async (t) => {
  const m = (sandbox) => ({ sandbox });
  /** @type {[string, object, object, string, string[]][]} */
  const cases = [
    // name, approved, on disk, the config's default, what it says
    ['a Docker Sandbox given up', m({ runtime: 'docker-sandbox' }), m({ runtime: 'bubblewrap' }), '', ['It would run in Fridolin’s sandbox instead of a Docker Sandbox.']],
    ['the inherited Docker Sandbox given up', m(undefined), m({ runtime: 'bubblewrap' }), 'docker-sandbox', ['It would run in Fridolin’s sandbox instead of a Docker Sandbox.']],
    ['dropped, to inherit Fridolin’s sandbox', m({ runtime: 'docker-sandbox' }), m(undefined), 'bubblewrap', ['It would run in Fridolin’s sandbox instead of a Docker Sandbox.']],
    ['falling back where it refused', m({ runtime: 'docker-sandbox', fallback: 'refuse' }), m({ runtime: 'docker-sandbox' }), '', ['While its Docker Sandbox is not available, it would run in Fridolin’s sandbox instead of refusing.']],
    ['more sites', m({ egress: ['api.github.com'] }), m({ egress: ['api.github.com', '**:443'] }), 'docker-sandbox', ['It could also reach **:443.']],
    ['a stronger sandbox', m(undefined), m({ runtime: 'docker-sandbox', fallback: 'refuse' }), '', []],
    ['fewer sites', m({ egress: ['a.example', 'b.example'] }), m({ egress: ['a.example'] }), 'docker-sandbox', []],
    ['nothing of the sandbox', {}, {}, 'docker-sandbox', []],
  ];
  for (const [name, a, disk, def, want] of cases) {
    await t.test(name, () => assert.deepEqual(sandboxWeaker(a, disk, def), want));
  }
});
