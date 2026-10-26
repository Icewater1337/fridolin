// The connections of Fridolin (docs/CONNECTIONS.md): a card each for OpenAI
// and ElevenLabs. Fridolin holds one API key per provider. Site modules
// that declare the connection in their module.json, and the brains ticked
// here, use the provider through the bridge, which keeps the key, checks
// what each caller may do and counts what it costs against its monthly
// limit. The key never comes back to the dashboard: the card names only
// its last characters. Both cards follow the one GET /api/connections.

import { del, post, put } from '../app/api.js';
import { brainsResource, resource } from '../app/resources.js';
import { h, paint } from '../lib/dom.js';
import { brainLabel } from '../lib/brain.js';
import { fmtUSD, plural } from '../lib/format.js';
import { budgetState, callerOf, monthName, spendText, usedText } from '../lib/connections.js';
import { icon } from './icons.js';
import { toast } from './toast.js';
import { btn } from './buttons.js';
import { confirmDialog } from './dialog.js';
import { field, nextId, textInput, toggle } from './fields.js';
import { badge, callout, loading, mono, pill } from './common.js';
import { liveForm } from './live-form.js';

/** @import { Page } from '../app/page.js' */
/** @import { Child } from '../lib/dom.js' */
/** @import { Caller } from '../lib/connections.js' */
/** @import { Brain, Connection, ConnectionModule, ConnectionUsage, Connections } from '../../types/api.js' */

/**
 * What sets each provider's card apart.
 * @typedef {object} Provider
 * @property {string} icon
 * @property {string} does what the provider does for modules and brains
 * @property {() => Child} keyHelp where the key comes from
 * @property {string[]} tools the tools a brain's runs get
 * @property {string} hardLimit what limits the bill for real
 */

/** @type {(href: string, text: string) => HTMLAnchorElement} */
const external = (href, text) => h('a', { href, target: '_blank', rel: 'noopener noreferrer' }, text);

/** @type {Record<Connection['id'], Provider>} */
const PROVIDERS = {
  openai: {
    icon: 'sparkle',
    does: 'to transcribe audio and to write text',
    keyHelp: () => h('span', 'Make one at ', external('https://platform.openai.com/api-keys', 'platform.openai.com/api-keys'), ', best in a project of its own with a budget.'),
    tools: ['openai_transcribe'],
    hardLimit: 'OpenAI’s own project budget is the hard limit: set one in the project’s settings on platform.openai.com.',
  },
  elevenlabs: {
    icon: 'play',
    does: 'to speak text and to transcribe audio',
    keyHelp: () => h('span', 'Make one under API keys in your ElevenLabs account. A key that may only use text to speech, speech to text and voices is enough.'),
    tools: ['elevenlabs_speak', 'elevenlabs_transcribe', 'elevenlabs_voices'],
    hardLimit: 'Your ElevenLabs plan’s credits are the hard limit.',
  },
};

/**
 * What the brains form is built from: the brains that may use the
 * connection, each one's limit, the default voice, and the brains to
 * pick from as [id, label].
 * @typedef {{allowed: string[], limit: number, voice: string, options: [string, string][]}} BrainsData
 */

/**
 * connectionCard(ctx, id): the card of one connection, following
 * GET /api/connections for the page. Its dirty() says whether one of its
 * forms has edits.
 * @param {Page} ctx
 * @param {Connection['id']} id
 * @returns {HTMLElement & {dirty: () => boolean}}
 */
export function connectionCard(ctx, id) {
  const p = PROVIDERS[id];
  const connections = resource('/api/connections');
  const refresh = () => connections.refresh();
  /** @type {Connections | null} */
  let data = null;
  /** @type {Brain[]} */
  let brains = [];
  let title = id === 'openai' ? 'OpenAI' : 'ElevenLabs'; // until the bridge names it
  let replacing = false; // the key form is open on a card that has a key
  /** @type {{hint: string, error: string} | null} */
  let failed = null; // the last check, when it failed for this key

  const headBox = h('div.region');
  const introBox = h('div.region');
  const keyBox = h('div.region');
  const brainsBox = h('div.connection-part');
  const modulesBox = h('div.connection-part');
  const usageBox = h('div.connection-part');
  const keyForm = liveForm({ build: buildKeyForm, edits: (d) => ({ organization: d.organization, project: d.project }) });
  const brainsForm = liveForm({
    build: buildBrainsForm,
    edits: (d) => ({ allowed: d.allowed, limit: d.limit, voice: d.voice }),
    staleNote: { title: 'Who may use it changed elsewhere', action: 'Load the new choice' },
  });
  brainsBox.append(h('h3', 'Brains'), brainsForm.el, h('div.actions', btn('Save', { kind: 'primary', icon: 'check', onClick: saveBrains })));

  const card = Object.assign(h('section.card', loading(`Loading ${title}…`)), {
    dirty: () => keyForm.dirty() || brainsForm.dirty(),
  });

  ctx.follow(brainsResource, (list) => { brains = (Array.isArray(list) ? list : []).filter((b) => b.id); render(); }, 60000);
  ctx.follow(connections, (d) => { data = d; render(); }, 30000);

  function render() {
    if (!data) return;
    const conn = (data.connections || []).find((c) => c.id === id);
    // A bridge that offers no such connection shows no card for it.
    card.hidden = !conn;
    if (!conn) return;
    title = conn.title || title;
    paint(card, 'ready', () => [headBox, introBox, keyBox, brainsBox, modulesBox, usageBox]);
    if (!conn.configured) replacing = false;
    const hint = conn.key_hint ? `…${conn.key_hint.replace(/^…/, '')}` : '';
    paint(headBox, { title, configured: conn.configured }, (s) => h('div.card-head',
      h('div.card-title', icon(p.icon), h('h2', s.title)),
      s.configured ? pill('Key set', 'ok') : pill('Not set up', 'neutral')));
    paint(introBox, title, (t) => [
      h('p', `Site modules and brains use ${t} through Fridolin ${p.does}; the key stays here.`),
      h('p.muted.small', 'Fridolin calls ', t, ' for them, checks what each may do, counts what it costs and stops one that goes over its monthly limit.'),
    ]);

    keyForm.set({ configured: conn.configured, organization: conn.organization || '', project: conn.project || '' });
    paint(keyBox, {
      configured: conn.configured, replacing, hint, organization: conn.organization || '', project: conn.project || '',
      failed: failed && failed.hint === hint ? failed.error : '',
    }, renderKey);

    // A brain that is gone keeps its switch, so that saving does not
    // quietly drop it; it is named as gone once the list has loaded.
    const known = new Set(brains.map((b) => b.id));
    /** @type {[string, string][]} */
    const options = brains.map((b) => [b.id, brainLabel(b)]);
    for (const b of conn.brains || []) if (!known.has(b)) options.push([b, brains.length ? `${b} (no such brain)` : b]);
    brainsForm.set({ allowed: conn.brains || [], limit: conn.brain_monthly_usd || 0, voice: conn.voice || '', options });

    paint(modulesBox, { configured: conn.configured, ops: conn.ops || [], modules: (data.modules || []).filter((m) => m.connection === id) }, renderModules);
    paint(usageBox, {
      configured: conn.configured, month: data.month,
      rows: (data.usage || []).filter((u) => u.connection === id).map((u) => ({ ...u, who: callerOf(u.caller, brains) })),
    }, renderUsage);
  }

  // ── the key ─────────────────────────────────────────────────────────

  /**
   * @param {{configured: boolean, organization: string, project: string}} d
   */
  function buildKeyForm({ configured, organization, project }) {
    const key = h('input', { type: 'password', id: nextId('ck'), autocomplete: 'off', placeholder: configured ? 'saved — type to replace' : '' });
    const org = id === 'openai' ? textInput({ class: 'mono', value: organization, placeholder: 'org-…' }) : null;
    const proj = id === 'openai' ? textInput({ class: 'mono', value: project, placeholder: 'proj_…' }) : null;
    const value = () => (org && proj
      ? { api_key: key.value, organization: org.value.trim(), project: proj.value.trim() }
      : { api_key: key.value });
    const el = h('div.settings-fields',
      field({ label: 'API key', control: key, help: configured ? 'Leave it empty to keep the key Fridolin has.' : p.keyHelp() }),
      org ? field({ label: 'Organization', tag: 'optional', control: org, help: 'Sent with each call as OpenAI-Organization.' }) : null,
      proj ? field({ label: 'Project', tag: 'optional', control: proj, help: 'Sent with each call as OpenAI-Project: the usage lands in that project, and its budget applies.' }) : null);
    return { el, value, key };
  }

  /**
   * @param {{configured: boolean, replacing: boolean, hint: string, organization: string, project: string, failed: string}} s
   */
  function renderKey(s) {
    if (!s.configured || s.replacing) {
      return [
        keyForm.el,
        h('div.actions',
          btn('Save key', { kind: 'primary', icon: 'key', onClick: saveKey }),
          s.replacing ? btn('Cancel', { onClick: () => { keyForm.discard(); replacing = false; render(); } }) : null),
        s.configured ? null : h('p.muted.small', 'Fridolin keeps the key in its data folder and never shows it again. No module, brain or site process ever gets it.'),
      ];
    }
    return [
      h('p', s.hint ? ['Key ending ', mono(s.hint), '.'] : 'A key is saved.',
        s.organization ? [' Organization ', mono(s.organization), '.'] : null,
        s.project ? [' Project ', mono(s.project), '.'] : null),
      s.failed ? callout('bad', 'The key did not work', s.failed) : null,
      h('div.actions',
        btn('Check', { icon: 'check', title: `Ask ${title} whether the key works`, onClick: () => check(s.hint) }),
        btn('Replace key', { icon: 'key', onClick: () => { replacing = true; render(); } }),
        btn('Remove', { kind: 'danger', icon: 'trash', small: true, onClick: remove })),
    ];
  }

  async function saveKey() {
    const form = keyForm.form();
    if (!form) return;
    const body = form.value();
    const conn = ((data && data.connections) || []).find((c) => c.id === id);
    if (!(conn && conn.configured) && !body.api_key.trim()) {
      toast(`Paste the ${title} key first`, 'warn');
      return;
    }
    await put(`/api/connections/${id}`, body);
    form.key.value = ''; // never kept in the page once it is saved
    keyForm.saved();
    replacing = false;
    failed = null;
    toast(body.api_key.trim() ? `${title} key saved` : `${title} settings saved`, 'ok');
    await refresh();
  }

  /** @param {string} hint the key it checks */
  async function check(hint) {
    const r = await post(`/api/connections/${id}/check`);
    if (r && r.ok) {
      failed = null;
      toast(`${title}: ${r.detail || 'the key works'}`, 'ok', { timeout: 8000 });
    } else {
      failed = { hint, error: (r && r.error) || 'The check failed.' };
    }
    render();
  }

  async function remove() {
    const ok = await confirmDialog({
      title: `Remove the ${title} key?`,
      message: `Site modules and brains can no longer use ${title} until you set a key again. Which brains may use it, and this month’s usage, stay.`,
      confirm: 'Remove', danger: true,
    });
    if (!ok) return;
    await del(`/api/connections/${id}`);
    failed = null;
    toast(`${title} key removed`, 'ok');
    await refresh();
  }

  // ── brains ──────────────────────────────────────────────────────────

  /**
   * @param {BrainsData} d
   */
  function buildBrainsForm({ allowed, limit, voice, options }) {
    const chosen = new Set(allowed);
    const switches = options.map(([b, label]) => Object.assign(toggle({ checked: chosen.has(b), label }), { brain: b }));
    const groupLabel = nextId('cb');
    const limitIn = textInput({ type: 'number', min: 0, step: 'any', value: limit ? String(limit) : '', placeholder: 'no limit' });
    const voiceIn = id === 'elevenlabs' ? textInput({ class: 'mono', value: voice, placeholder: 'a voice id' }) : null;
    const value = () => ({
      brains: switches.filter((s) => s.input.checked).map((s) => s.brain),
      limit: limitIn.value.trim(),
      voice: voiceIn ? voiceIn.value.trim() : undefined,
    });
    const el = h('div.settings-fields',
      h('div.field.wide',
        h('div.field-label', { id: groupLabel }, `May use ${title}`),
        switches.length ? h('div.connection-brains', { role: 'group', 'aria-labelledby': groupLabel }, switches) : h('p.muted', 'No brain yet.'),
        h('div.help', 'Their runs get ', p.tools.map((t, i) => [i ? ', ' : '', mono(t)]), ' while a key is set.')),
      field({ label: 'Monthly limit per brain (USD, estimate)', control: limitIn, help: 'Each brain’s own limit, on Fridolin’s estimate. A brain over it gets an error instead of a call until the month ends. Empty or 0: no limit.' }),
      voiceIn ? field({ label: 'Default voice', control: voiceIn, help: 'The ElevenLabs voice id that text to speech uses when a module or brain names none.' }) : null);
    return { el, value };
  }

  async function saveBrains() {
    const form = brainsForm.form();
    if (!form) return;
    const v = form.value();
    const limit = v.limit === '' ? 0 : Number(v.limit);
    if (!Number.isFinite(limit) || limit < 0) {
      toast('The monthly limit is an amount in dollars, or empty for none', 'warn');
      return;
    }
    /** @type {{brains: string[], brain_monthly_usd: number, voice?: string}} */
    const body = { brains: v.brains, brain_monthly_usd: limit };
    if (v.voice !== undefined) body.voice = v.voice;
    await put(`/api/connections/${id}/brains`, body);
    brainsForm.saved();
    toast(`Saved which brains may use ${title}`, 'ok');
    await refresh();
  }

  // ── modules and usage ───────────────────────────────────────────────

  /** @param {{configured: boolean, ops: string[], modules: ConnectionModule[]}} s */
  function renderModules({ configured, ops, modules }) {
    const declare = h('p.muted.small', 'A module asks for it in its ', mono('module.json'), ', under ', mono(`capabilities.${id}`),
      ops.length ? [': the calls it uses (', ops.map((o, i) => [i ? ', ' : '', mono(o)]), '), the models it may use and its monthly limit'] : null,
      '. You approve that when you deploy it on the ', h('a', { href: '#/site' }, 'Site'), ' page.');
    return [
      h('h3', 'Site modules'),
      !configured && modules.length ? callout('warn', `${plural(modules.length, 'live module')} ${modules.length === 1 ? 'uses' : 'use'} ${title}`, 'Their calls fail until a key is set.') : null,
      modules.length
        ? h('div.facts', modules.map((m) => h('div',
          mono(m.module), ' — ', (m.use || []).join(', '),
          ' · ', (m.models || []).length ? ['models ', m.models.map((x, i) => [i ? ', ' : '', mono(x)])] : 'any model',
          ' · ', m.monthly_usd ? `up to ${fmtUSD(m.monthly_usd)} a month` : 'no monthly limit')))
        : h('p.muted', `No live site module uses ${title}.`),
      declare,
    ];
  }

  /** @param {{configured: boolean, month: string, rows: (ConnectionUsage & {who: Caller})[]}} s */
  function renderUsage({ configured, month, rows }) {
    if (!configured && !rows.length) return null;
    const total = rows.reduce((sum, u) => sum + (u.usd || 0), 0);
    /** @param {Caller} who */
    const name = (who) => (who.kind === 'module' ? h('span', 'Site module ', mono(who.id))
      : who.kind === 'brain' ? h('span', 'Brain ', h('strong', who.name)) : mono(who.name));
    /** @param {ConnectionUsage} u */
    const over = (u) => {
      const state = budgetState(u.usd, u.limit_usd);
      return state === 'over' ? badge('over its limit', 'bad') : state === 'near' ? badge('near its limit', 'warn') : null;
    };
    return [
      h('h3', `Usage in ${monthName(month)}`),
      rows.length ? h('div.table-wrap', h('table.table',
        h('thead', h('tr', h('th', 'Caller'), h('th.right', 'Requests'), h('th', 'Used'), h('th.right', 'Estimated cost'))),
        h('tbody', rows.map((u) => {
          const flag = over(u);
          return h('tr',
            h('td', name(u.who)),
            h('td.right', String(u.requests), u.errors ? h('span.muted.small', ` (${u.errors} failed)`) : null),
            h('td', usedText(u) || h('span.muted', '—')),
            h('td.right', spendText(u.usd, u.limit_usd), flag ? [' ', flag] : null));
        })))) : h('p.muted', 'Nothing used yet this month.'),
      h('p.muted.small', rows.length > 1 ? `About ${fmtUSD(total)} in all. ` : '',
        `The costs are Fridolin’s estimates from a price table of its own, not ${title}’s bill. ${p.hardLimit}`),
    ];
  }

  return card;
}
