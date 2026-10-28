// The editor of fride.instances on the Settings page: one box per FrIDE
// instance (a whole FrIDE in a Docker Sandbox of its own on a node, for
// one trust domain), with what the core brain reads to pick it and what
// the instance may reach. It checks what the bridge checks
// (config.validateFrideInstances), so a save is not refused for a typo.

import { h } from '../lib/dom.js';
import { IDLE_STOP_DEFAULT, INSTANCE_IDENTITIES, INSTANCE_LIMITS, egressProblem, instanceNameProblem, repoProblem } from '../lib/fride.js';
import { btn } from './buttons.js';
import { field, linesArea, linesOf, nextId, textInput } from './fields.js';

/** @import { FrideInstanceConfig, GitIdentityID } from '../../types/api.js' */

/**
 * One instance's box: its element, its name now, what it holds and
 * whether that would pass the bridge's checks.
 * @typedef {object} InstanceBox
 * @property {HTMLElement} el
 * @property {() => string} name
 * @property {() => FrideInstanceConfig} value
 * @property {(taken: string[]) => boolean} check
 */

/** @type {Record<GitIdentityID, string>} */
const IDENTITY_LABELS = { github: 'GitHub', gitlab: 'GitLab' };

/**
 * An instance as config.json keeps it: its fields in one order, the
 * empty ones left out (as the bridge writes them) and its identities
 * sorted, so a form nobody changed holds what the file held.
 * @param {Partial<FrideInstanceConfig> | null | undefined} c
 * @returns {FrideInstanceConfig}
 */
function clean(c) {
  const src = c || {};
  /** @type {FrideInstanceConfig} */
  const out = {};
  /** @type {(v: unknown) => string[]} */
  const list = (v) => (Array.isArray(v) ? v.map((x) => String(x)) : []);
  if (src.description) out.description = String(src.description);
  const repos = list(src.repos);
  if (repos.length) out.repos = repos;
  const ids = /** @type {GitIdentityID[]} */ (list(src.identities).sort());
  if (ids.length) out.identities = ids;
  const egress = list(src.egress);
  if (egress.length) out.egress = egress;
  const nodes = list(src.nodes);
  if (nodes.length) out.nodes = nodes;
  if (src.memory_mb) out.memory_mb = src.memory_mb;
  if (src.idle_stop_minutes) out.idle_stop_minutes = src.idle_stop_minutes;
  return out;
}

/**
 * fride.instances as the editor writes it, from what config.json holds:
 * by name, each instance cleaned, the names in alphabetical order.
 * @param {unknown} raw
 * @returns {Record<string, FrideInstanceConfig>}
 */
export function normalizeInstances(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {};
  const all = /** @type {Record<string, Partial<FrideInstanceConfig>>} */ (raw);
  return Object.fromEntries(Object.keys(all).sort().map((name) => [name, clean(all[name])]));
}

/**
 * Marks control as wrong with problem (its tooltip and the note under it),
 * or as right when problem is "". Returns whether it is right.
 * @param {HTMLInputElement | HTMLTextAreaElement} control
 * @param {HTMLElement} note
 * @param {string} problem
 */
function mark(control, note, problem) {
  control.classList.toggle('invalid', !!problem);
  control.title = problem;
  note.textContent = problem;
  note.hidden = !problem;
  return !problem;
}

/**
 * An integer typed into input: 0 when empty, NaN when it is none.
 * @param {HTMLInputElement} input
 */
function intOf(input) {
  const v = input.value.trim();
  if (v === '') return 0;
  return /^-?\d+$/.test(v) ? Number(v) : NaN;
}

/**
 * The first problem of a list of lines: too many, or a line problem() names.
 * @param {string[]} lines
 * @param {(line: string) => string} [problem]
 */
function listProblem(lines, problem) {
  if (lines.length > INSTANCE_LIMITS.list) return `At most ${INSTANCE_LIMITS.list}.`;
  if (!problem) return '';
  for (const l of lines) {
    const p = problem(l);
    if (p) return p;
  }
  return '';
}

/**
 * One instance's box. A new one (isNew) has its name typed in; a saved
 * one keeps its name, since another name is another instance (remove
 * this one and add that).
 * @param {string} name
 * @param {FrideInstanceConfig} cfg
 * @param {{isNew: boolean, onRemove: () => void}} options
 * @returns {InstanceBox}
 */
function instanceBox(name, cfg, { isNew, onRemove }) {
  const note = () => h('div.help.warn-text', { hidden: true });
  const nameIn = textInput({ value: name, placeholder: 'work', class: 'mono' });
  const nameNote = note();
  const desc = textInput({ value: cfg.description || '', placeholder: 'work repositories, on the company GitLab' });
  const repos = linesArea(cfg.repos, { rows: 3, placeholder: 'https://gitlab.example.com/group/app.git' });
  const reposNote = note();
  const ids = INSTANCE_IDENTITIES.map((id) => ({ id, box: h('input', { type: 'checkbox', id: nextId('ident'), checked: (cfg.identities || []).includes(id) }) }));
  const egress = linesArea(cfg.egress, { rows: 2, placeholder: 'registry.example.com' });
  const egressNote = note();
  const nodes = linesArea(cfg.nodes, { rows: 2, placeholder: 'any node that offers fride_host' });
  const nodesNote = note();
  const memory = textInput({ value: cfg.memory_mb ? String(cfg.memory_mb) : '', inputmode: 'numeric', placeholder: '0 — the core decides', class: 'num' });
  const memoryNote = note();
  const idle = textInput({ value: cfg.idle_stop_minutes ? String(cfg.idle_stop_minutes) : '', inputmode: 'numeric', placeholder: `0 — ${IDLE_STOP_DEFAULT} minutes`, class: 'num' });
  const idleNote = note();

  const title = isNew
    ? field({ label: 'Name', control: nameIn, help: 'A lower-case letter, then lower-case letters, digits and dashes: what the core brain and the chat call it.' })
    : h('h3.fride-instance-name', h('code.mono', name));
  const el = h('section.fride-instance-edit',
    h('div.fride-instance-head', title,
      btn('Remove', { small: true, kind: 'ghost', icon: 'x', title: `Remove ${isNew ? 'this instance' : name}`, onClick: () => { el.remove(); onRemove(); } })),
    isNew ? nameNote : null,
    h('div.form-grid',
      field({ label: 'Description', control: desc, wide: true, help: 'What it is for. The core brain reads it to pick the instance for a job, such as “work repositories” or “fridolin and FrIDE themselves”.' }),
      h('div.field', h('label.field-label', { for: repos.id }, 'Repositories'), repos, h('div.help', 'One git URL per line. They are cloned inside the instance when missing.'), reposNote),
      h('div.field', h('span.field-label', 'Git identities'),
        h('div.check-list', ids.map(({ id, box }) => h('label.check-row', { for: box.id }, box, IDENTITY_LABELS[id]))),
        h('div.help', 'Only these reach the instance, as placeholders for their tokens. They are connected under ', h('a', { href: '#/integrations' }, 'Integrations'), '.')),
      h('div.field', h('label.field-label', { for: egress.id }, 'More hosts it may reach'), egress, h('div.help', 'One host name per line. Its provider, its identities’ hosts, Go, npm and Docker Hub are always allowed.'), egressNote),
      h('div.field', h('label.field-label', { for: nodes.id }, 'Nodes'), nodes, h('div.help', 'Where it may run, by node name or id, one per line. Empty: any approved node that offers fride_host.'), nodesNote),
      h('div.field', h('label.field-label', { for: memory.id }, 'Memory (MB)'), memory, h('div.help', `0 lets the core size it at each start (3, 6 or 12 GB). Else a fixed size from ${INSTANCE_LIMITS.minMemoryMB} to ${INSTANCE_LIMITS.maxMemoryMB}.`), memoryNote),
      h('div.field', h('label.field-label', { for: idle.id }, 'Stop when idle for (minutes)'), idle, h('div.help', `With no open thread and no news. 0 is the default, ${IDLE_STOP_DEFAULT}; -1 never stops it. At most ${INSTANCE_LIMITS.maxIdleMinutes}.`), idleNote)));

  return {
    el,
    name: () => (isNew ? nameIn.value.trim() : name),
    value: () => clean({
      description: desc.value.trim(),
      repos: linesOf(repos),
      identities: ids.filter(({ box }) => box.checked).map(({ id }) => id),
      egress: linesOf(egress),
      nodes: linesOf(nodes),
      memory_mb: intOf(memory),
      idle_stop_minutes: intOf(idle),
    }),
    check(taken) {
      const mem = intOf(memory);
      const minutes = intOf(idle);
      const results = [
        !isNew || mark(nameIn, nameNote, instanceNameProblem(nameIn.value.trim(), taken)),
        mark(repos, reposNote, listProblem(linesOf(repos), repoProblem)),
        mark(egress, egressNote, listProblem(linesOf(egress), egressProblem)),
        mark(nodes, nodesNote, listProblem(linesOf(nodes))),
        mark(memory, memoryNote, Number.isNaN(mem) || mem < 0 || (mem !== 0 && (mem < INSTANCE_LIMITS.minMemoryMB || mem > INSTANCE_LIMITS.maxMemoryMB))
          ? `0, or from ${INSTANCE_LIMITS.minMemoryMB} to ${INSTANCE_LIMITS.maxMemoryMB} MB.` : ''),
        mark(idle, idleNote, Number.isNaN(minutes) || minutes < -1 || minutes > INSTANCE_LIMITS.maxIdleMinutes
          ? `-1 (never), 0 (${IDLE_STOP_DEFAULT} minutes) or up to ${INSTANCE_LIMITS.maxIdleMinutes}.` : ''),
      ];
      return results.every(Boolean);
    },
  };
}

/**
 * frideInstancesEditor({instances, onChange}) edits fride.instances as
 * config.json holds it. el.getValue() is the map it would save (the
 * instances in the order shown, as normalizeInstances writes them), and
 * el.isValid() whether the bridge would take it. onChange runs after a
 * change that fires no input or change event: an instance added or
 * removed.
 * @param {{instances: unknown, onChange: () => void}} options
 * @returns {HTMLDivElement & {getValue: () => Record<string, FrideInstanceConfig>, isValid: () => boolean}}
 */
export function frideInstancesEditor({ instances, onChange }) {
  /** @type {InstanceBox[]} */
  const boxes = [];
  const list = h('div.fride-instance-list');
  const emptyEl = h('p.list-empty.muted', 'None yet. Without instances the core brain hands work only to the nodes’ own FrIDE.');
  const addBtn = btn('Add instance', {
    icon: 'plus', small: true, kind: 'ghost',
    onClick: () => {
      const b = add('', {}, true);
      const input = b.el.querySelector('input');
      if (input) input.focus();
      onChange();
    },
  });
  const el = h('div.fride-instances-editor', emptyEl, list, h('div.list-add', addBtn));

  // Every box checks itself; a name is checked against the others' names.
  const check = () => boxes.map((b, i) => b.check(boxes.filter((_, j) => j !== i).map((o) => o.name()))).every(Boolean);
  const refresh = () => {
    emptyEl.hidden = boxes.length > 0;
    addBtn.disabled = boxes.length >= INSTANCE_LIMITS.instances;
    addBtn.title = addBtn.disabled ? `At most ${INSTANCE_LIMITS.instances} instances` : '';
    check();
  };

  /**
   * @param {string} name
   * @param {FrideInstanceConfig} cfg
   * @param {boolean} isNew
   */
  function add(name, cfg, isNew) {
    const b = instanceBox(name, cfg, {
      isNew,
      onRemove: () => {
        boxes.splice(boxes.indexOf(b), 1);
        refresh();
        onChange();
      },
    });
    boxes.push(b);
    list.appendChild(b.el);
    refresh();
    return b;
  }

  for (const [name, cfg] of Object.entries(normalizeInstances(instances))) add(name, cfg, false);
  refresh();
  el.addEventListener('input', check);
  el.addEventListener('change', check);

  return Object.assign(el, {
    getValue: () => Object.fromEntries(boxes.map((b) => [b.name(), b.value()])),
    isValid: check,
  });
}
