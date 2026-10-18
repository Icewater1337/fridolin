// The settings of a brain that the add-brain wizard and the brain page both
// edit: its model and effort, and whether and what of its folder is backed
// up. One copy, so both say the same about them.

import { h } from '../lib/dom.js';
import { EFFORTS } from '../lib/brain.js';
import { field, textInput, select, toggle, linesArea, linesOf } from './fields.js';

/**
 * brainSettingsFields(manifest, eff) returns {model, backup, value()}: model
 * holds the model and effort fields, backup the backup switch and the
 * patterns to leave out, for the caller to place. manifest gives what they
 * show ({model, effort, backup: {enabled, exclude}}; without backup,
 * backups are on), eff the global brain_model and brain_effort that
 * empty fields inherit. value() is what they hold now, in brain.json's form.
 */
export function brainSettingsFields(manifest = {}, eff = {}) {
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
