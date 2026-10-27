// The settings of a brain that the add-brain wizard and the brain page both
// edit: its model and effort, and whether and what of its folder is backed
// up. One copy, so both say the same about them. And where its runs
// happen, which the brain page edits.

import { h } from '../lib/dom.js';
import { EFFORTS, MAX_EGRESS, SANDBOX_FALLBACKS, egressProblem, runtimeName } from '../lib/brain.js';
import { field, textInput, select, toggle, linesArea, linesOf, listEditor } from './fields.js';

/** @import { BrainManifest, BrainSandbox, ConfigFile, SandboxFallback, SandboxRuntime } from '../../types/api.js' */

/**
 * brainSettingsFields(manifest, eff) returns {model, backup, value()}: model
 * holds the model and effort fields, backup the backup switch and the
 * patterns to leave out, for the caller to place. manifest gives what they
 * show ({model, effort, backup: {enabled, exclude}}; without backup,
 * backups are on), eff the global brain_model and brain_effort that
 * empty fields inherit. value() is what they hold now, in brain.json's form.
 * @param {Partial<Pick<BrainManifest, 'model' | 'effort' | 'backup'>>} [manifest]
 * @param {Partial<Pick<ConfigFile, 'brain_model' | 'brain_effort'>>} [eff]
 */
export function brainSettingsFields(manifest = {}, eff = {}) {
  /** @type {Partial<BrainManifest['backup']>} */
  const backup = manifest.backup || { enabled: true };
  const modelIn = textInput({ value: manifest.model || '', class: 'mono', placeholder: eff.brain_model ? `inherit (${eff.brain_model})` : 'inherit' });
  const effortSel = select(EFFORTS.map(([v, l]) => [v, v === '' && eff.brain_effort ? `Inherit (${eff.brain_effort})` : l]), manifest.effort || '');
  const backupSw = toggle({ checked: !!backup.enabled, label: 'Include in Google Drive backups' });
  const excludes = linesArea(backup.exclude || [], { rows: 5, placeholder: 'node_modules/\n*.log' });
  return {
    model: h('div.form-grid',
      field({ label: 'Model', control: modelIn, help: 'e.g. sonnet, opus or a full model id. Empty uses the global brain model.' }),
      field({ label: 'Effort', control: effortSel, help: 'How hard the model thinks. Inherit uses the global setting.' })),
    backup: [
      h('div.field', backupSw, h('div.help', 'Backs up this folder with the daily backup, once Google Drive is connected.')),
      h('details.details', h('summary', 'Files to leave out of backups'),
        field({ label: 'Exclude patterns', control: excludes, help: 'One per line, relative to the brain folder. A trailing / means a folder.' })),
    ],
    value: () => ({
      model: modelIn.value.trim(),
      effort: effortSel.value,
      backup: { enabled: backupSw.input.checked, exclude: linesOf(excludes) },
    }),
  };
}

/**
 * brainSandboxFields(sandbox, defaultRuntime, onChange) returns {el,
 * value(), problem()}: the fields for brain.json's sandbox object (where
 * the brain runs, what it does while its Docker Sandbox is not available,
 * and the extra hosts it may reach there), for the caller to place.
 * defaultRuntime is config.json's sandbox.runtime, which "Default"
 * inherits; onChange runs after a change that fires no event, such as a
 * removed host. value() is the object to write, or undefined for none;
 * it keeps what the brain.json had where the fields leave it as it was.
 * problem() says why it cannot be saved, or "".
 * @param {BrainSandbox | undefined} sandbox
 * @param {string | undefined} defaultRuntime
 * @param {() => void} onChange
 */
export function brainSandboxFields(sandbox, defaultRuntime, onChange) {
  const sb = sandbox || {};
  const runtimeSel = select([
    ['', `Default (${runtimeName(defaultRuntime)})`],
    ['docker-sandbox', 'Docker Sandbox'],
    ['bubblewrap', 'Fridolin’s sandbox'],
  ], sb.runtime || '', { name: 'sandbox-runtime' });
  const fallbackSel = select(SANDBOX_FALLBACKS, sb.fallback || 'bubblewrap', { name: 'sandbox-fallback' });
  const egress = listEditor({
    values: sb.egress || [],
    placeholder: 'api.github.com',
    addLabel: 'Add site',
    validate: egressProblem,
    empty: 'None: only its AI provider and Fridolin',
    onChange,
  });
  // Only a Docker Sandbox falls back or keeps to a list of sites.
  const docker = h('div.form-grid',
    field({ label: 'When the Docker Sandbox is not available', control: fallbackSel, help: 'When its node is offline, or Docker Sandboxes there are stopped or not signed in. Falling back keeps the brain answering, and you get a message. Refuse for a brain that must never run outside its Docker Sandbox.' }),
    field({ label: 'Other sites it may reach', control: egress, help: `In its Docker Sandbox, beyond its AI provider and Fridolin. One host each, such as api.github.com or *.wikipedia.org; **:443 lets it reach any site. At most ${MAX_EGRESS}.` }));
  const show = () => { docker.hidden = (runtimeSel.value || defaultRuntime) !== 'docker-sandbox'; };
  runtimeSel.addEventListener('change', show);
  show();
  return {
    el: [
      field({ label: 'Where it runs', control: runtimeSel, help: 'A Docker Sandbox is a small virtual machine of its own on a node, which reaches only what it needs. Fridolin’s sandbox runs it beside the bridge. More in docs/SANDBOXES.md.' }),
      docker,
    ],
    /** @returns {BrainSandbox | undefined} */
    value: () => {
      /** @type {BrainSandbox} */
      const out = {};
      if (runtimeSel.value) out.runtime = /** @type {SandboxRuntime} */ (runtimeSel.value);
      if (fallbackSel.value !== 'bubblewrap' || sb.fallback) out.fallback = /** @type {SandboxFallback} */ (fallbackSel.value);
      const hosts = egress.getValues();
      if (hosts.length || sb.egress) out.egress = hosts;
      return Object.keys(out).length ? out : undefined;
    },
    problem: () => {
      if (!egress.isValid()) return 'One of the sites it may reach is not a host';
      if (egress.getValues().length > MAX_EGRESS) return `At most ${MAX_EGRESS} sites it may reach`;
      return '';
    },
  };
}
