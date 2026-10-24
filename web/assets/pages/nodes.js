// Nodes: the machines of the fleet (docs/NODES.md). A node joins with a
// code from here or `fridolin nodes join-code`, waits for its device to be
// approved, then for what it offers to be approved, and may be revoked at
// any time; each step goes through the bridge (/api/nodes/...), which the
// node hears at once.

import { ApiError, post, put } from '../app/api.js';
import { nodesResource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { absTime } from '../lib/format.js';
import { capSummary, gb, osName, parseTags, ruleText, updateText } from '../lib/nodes.js';
import { btn } from '../components/buttons.js';
import { confirmDialog, openDialog } from '../components/dialog.js';
import { select, textInput, toggle } from '../components/fields.js';
import { nodeTerminal } from '../components/node-terminal.js';
import { toast } from '../components/toast.js';
import { badge, callout, empty, kv, mono, pageHeader, pill, spinner, timeAgo } from '../components/common.js';

export default {
  title: 'Nodes',
  render(root, ctx) {
    let data = null;
    const notice = h('div');
    const list = h('div.node-list', spinner());
    const joinBtn = btn('New join code', { kind: 'primary', icon: 'plus', onClick: () => newJoinCode() });

    mount(root,
      pageHeader('Nodes', 'Other machines that lend this core their commands, files and devices. Each joins with a code, then you approve the device and, separately, what it offers.', joinBtn),
      notice,
      list);

    const refresh = () => nodesResource.refresh();

    async function newJoinCode() {
      const r = await post('/api/nodes/join-code', {});
      await openDialog({
        title: 'Join a new machine',
        wide: true,
        body: [
          ...(r.box_command ? [
            h('p', `A node in a box: on the new machine, with Docker, run this line. It asks before it starts, and for the rest. The code works once, until ${absTime(r.expires_at)}.`),
            h('p.muted.small', 'macOS, Linux:'),
            h('div.join-command', mono(r.box_command, { copy: true })),
            h('p.muted.small', 'Windows, in PowerShell:'),
            h('div.join-command', mono(r.box_command_powershell, { copy: true })),
            h('p', 'Or natively, with fridolin installed on it (Apple builds, USB devices, notifications):'),
          ] : [
            h('p', `On the new machine, with fridolin installed, run this line. The code works once, until ${absTime(r.expires_at)}.`),
          ]),
          h('p.muted.small', 'macOS, Linux:'),
          h('div.join-command', mono(r.command, { copy: true })),
          ...(r.command_powershell ? [
            h('p.muted.small', 'Windows, in PowerShell:'),
            h('div.join-command', mono(r.command_powershell, { copy: true })),
          ] : []),
          h('p.muted.small', 'It joins and installs the node service. The node then shows up here, waiting for you to approve it.'),
        ],
      });
      await refresh();
    }

    async function act(n, path, done) {
      await post(`/api/nodes/${encodeURIComponent(n.id)}/${path}`, {});
      toast(done, 'ok');
      await refresh();
    }

    // Updates: the core sends the node the build of its own version; the
    // node swaps it in and restarts, and goes back by itself when the new
    // one does not connect. A busy node (a FrIDE task, a backup) is
    // updated only when the owner says so.
    async function update(n) {
      const path = `/api/nodes/${encodeURIComponent(n.id)}/update`;
      try {
        await post(path, {});
      } catch (err) {
        if (!(err instanceof ApiError) || err.status !== 409 || !/force/.test(err.message)) throw err;
        const ok = await confirmDialog({
          title: `Update ${n.name} now?`,
          message: `${err.message}. Updating restarts its agent, which may cut that off.`,
          confirm: 'Update anyway', danger: true,
        });
        if (!ok) return;
        await post(path, { force: true });
      }
      toast(`Updating ${n.name} to ${n.update_to}`, 'ok');
      await refresh();
    }

    async function setAuto(n, auto) {
      await put(`/api/nodes/${encodeURIComponent(n.id)}/update-policy`, { auto });
      toast(auto ? `${n.name} updates by itself from now on` : `${n.name} waits for you to update it`, 'ok');
      await refresh();
    }

    async function revoke(n) {
      const ok = await confirmDialog({
        title: `Revoke ${n.name}?`,
        message: 'It is disconnected now and refused from then on. To bring the machine back, it joins again with a new code.',
        confirm: 'Revoke', danger: true,
      });
      if (ok) await act(n, 'revoke', `Revoked ${n.name}`);
    }

    async function editTags(n) {
      const input = textInput({ value: (n.tags || []).join(', '), placeholder: 'macos, docker, gpu', 'aria-label': 'Tags' });
      await openDialog({
        title: `Tags of ${n.name}`,
        body: [h('p.muted.small', 'Lower-case words, separated by commas: what runs there (docker, xcode, gpu) or how much memory it has (memory:32gb).'), input],
        actions: [
          { label: 'Cancel', value: null },
          {
            label: 'Save', kind: 'primary',
            onClick: async () => {
              await put(`/api/nodes/${encodeURIComponent(n.id)}/tags`, { tags: parseTags(input.value) });
              await refresh();
              return true;
            },
          },
        ],
      });
    }

    // A shell in a folder the node granted for terminal; it ends with
    // the dialog.
    async function openTerminal(n) {
      const { roots } = n;
      const pick = select(roots, roots[0], { 'aria-label': 'Folder' });
      const holder = h('div.term-holder');
      let term = null;
      await openDialog({
        title: `Terminal on ${n.name}`,
        wide: true,
        body: [h('p.muted.small', 'A shell on the node, in a folder it granted. Every command line you type is in the audit log; a lockdown closes it.'), pick, holder],
        actions: [
          { label: 'Close', value: null },
          {
            label: 'Open', kind: 'primary',
            onClick: async () => {
              if (term) await term.close();
              term = nodeTerminal({ node: n.id, folder: pick.value });
              holder.replaceChildren(term.el);
              return false;
            },
          },
        ],
      });
      if (term) await term.close();
    }

    function statePills(n) {
      const out = [];
      if (n.device === 'pending') out.push(badge('device waiting', 'warn'));
      else if (n.device === 'revoked') out.push(badge('revoked', 'bad'));
      else if (n.commands === 'pending') out.push(badge(n.commands_approved_at ? 'offers more than approved' : 'commands waiting', 'warn'));
      else out.push(badge('approved', 'ok'));
      return out;
    }

    function actions(n) {
      const out = [];
      if (n.device === 'pending') {
        out.push(btn('Approve device', { kind: 'primary', small: true, icon: 'check', onClick: () => act(n, 'approve', `Approved the device ${n.name}`) }));
      } else if (n.device === 'approved' && n.commands === 'pending') {
        out.push(btn('Approve what it offers', { kind: 'primary', small: true, icon: 'check', onClick: () => act(n, 'approve-commands', `Approved what ${n.name} offers`) }));
      }
      const updating = n.update && (n.update.state === 'sending' || n.update.state === 'restarting');
      // Offline nodes are refused by the bridge, which says so.
      if (n.update_to && !updating) {
        out.push(btn(`Update to ${n.update_to}`, { kind: 'primary', small: true, icon: 'refresh', onClick: () => update(n) }));
      }
      if (n.device !== 'revoked') {
        out.push(btn('Tags', { small: true, kind: 'ghost', icon: 'edit', onClick: () => editTags(n) }));
        out.push(btn('Revoke', { small: true, kind: 'danger', icon: 'x', onClick: () => revoke(n) }));
      }
      return out;
    }

    function offers(n) {
      const declared = n.declared || {};
      const rules = (declared.exec && declared.exec.allow) || [];
      const roots = (declared.fs && declared.fs.roots) || [];
      return h('div.node-offers',
        rules.length ? h('div', h('strong', 'Commands: '), rules.map((r) => mono(ruleText(r))).flatMap((el, i) => (i ? [', ', el] : [el]))) : null,
        roots.length ? h('div', h('strong', 'Folders: '), roots.map((r) => mono(`${r.path}${r.write ? ' (read and write)' : ' (read only)'}`)).flatMap((el, i) => (i ? [', ', el] : [el]))) : null,
        frideLine(declared.fride),
        backupLine(declared.backup, n.approved && n.approved.backup),
        workLine('Agent sessions', declared.agent),
        workLine('Terminal', declared.terminal));
    }

    // Workstation: the folders an agent session or a terminal may run in,
    // the providers the node found and how it confines them.
    function workLine(label, w) {
      if (!w) return null;
      const extra = [w.providers && w.providers.length ? w.providers.join(', ') : null, w.sandbox ? `sandbox ${w.sandbox}` : null].filter(Boolean);
      return h('div', h('strong', `${label}: `),
        (w.roots || []).map((r) => mono(r)).flatMap((el, i) => (i ? [', ', el] : [el])),
        extra.length ? h('span.muted', ` · ${extra.join(' · ')}`) : null);
    }

    // Backups: the places the node lends, and the folders the owner
    // picked for the Vault page's targets.
    function backupLine(b, approved) {
      if (!b) return null;
      const roots = b.roots || [];
      const folders = (approved && approved.folders) || [];
      return h('div', h('strong', 'Backups: '),
        roots.length ? roots.map((r) => mono(r)).flatMap((el, i) => (i ? [', ', el] : [el])) : 'any of its drives',
        folders.length ? h('span.muted', ` · targets in ${folders.join(', ')}`) : h('span.muted', ' · pick a folder on the Vault page'));
    }

    // FrIDE answers on the node. Its UI is on the node's own loopback,
    // which the node agent is to publish over the tailnet; until then the
    // page shows the address the node reports.
    function frideLine(f) {
      if (!f) return null;
      const repos = f.repos || [];
      return h('div.node-fride', h('strong', 'FrIDE: '),
        h('a', { href: f.url, target: '_blank', rel: 'noopener noreferrer' }, f.url),
        repos.length ? h('span.muted', ` · ${repos.join(', ')}`) : null);
    }

    // What changes with every heartbeat: whether it is online and its
    // free resources. It is painted into the card on its own, so the card
    // and its buttons stay while a node reports (see paintAll).
    const termOf = (n) => {
      const roots = (n.effective && n.effective.terminal && n.effective.terminal.roots) || [];
      return n.online && roots.length ? { id: n.id, name: n.name, roots } : null;
    };
    const liveOf = (n) => ({ online: !!n.online, last_seen: n.last_seen || null, resources: n.resources || {}, address: n.address || null, term: termOf(n) });
    const stableOf = (n) => ({ ...n, online: undefined, last_seen: undefined, resources: undefined, address: undefined });
    const liveRegions = new Map(); // node id → its live region

    function live(l) {
      const res = l.resources;
      const memory = res.mem_total ? `${gb(res.mem_available)} free of ${gb(res.mem_total)}` : null;
      return [
        h('div.node-online', l.online ? pill('online', 'ok', { pulse: true }) : pill('offline', 'neutral'),
          l.online ? null : h('span.muted.small', 'last seen ', timeAgo(l.last_seen)),
          l.term ? btn('Terminal', { small: true, icon: 'terminal', onClick: () => openTerminal(l.term) }) : null),
        kv([
          ['CPUs', res.cpus ? String(res.cpus) : null],
          ['Memory', memory],
          ['Disk free', res.disk_free ? gb(res.disk_free) : null],
          ['Battery', typeof res.battery === 'number' ? `${res.battery}%` : null],
          ['Address', l.address],
        ]),
      ];
    }

    function card(n) {
      const region = h('div.node-live');
      liveRegions.set(n.id, region);
      return h('section.card.node-card',
        h('div.card-head',
          h('div.card-title', h('h2', n.name), h('span.muted', osName(n.os), n.arch ? ` · ${n.arch}` : '')),
          h('div.node-state', statePills(n))),
        n.device === 'pending' ? callout('warn', 'Waiting for approval', `Approve this device if you just joined ${n.name} yourself. What it offers is approved in a second step.`) : null,
        n.device === 'approved' && n.commands === 'pending' && !n.commands_approved_at
          ? callout('warn', 'Waiting for you to approve what it offers', `It offers ${capSummary(n.declared)}, listed below. It serves nothing until you approve.`)
          : null,
        n.device === 'approved' && n.commands === 'pending' && n.commands_approved_at
          ? callout('warn', 'It offers more than you approved', `Approved so far: ${capSummary(n.approved)}. It now offers: ${capSummary(n.declared)}. Until you approve, it serves only what both include.`)
          : null,
        region,
        kv([
          ['ID', mono(n.id, { copy: true })],
          ['May run', capSummary(n.effective)],
          ['Offers', capSummary(n.declared)],
          ['Tags', (n.tags || []).length ? h('span.tag-list', n.tags.map((t) => badge(t))) : h('span.muted', 'none')],
          ['Version', n.version || null],
          ['Update', updateText(n.update) || n.update_note || null],
        ]),
        n.device === 'approved' && n.effective && n.effective.update
          ? h('div.node-auto-update', toggle({ checked: !!(n.update && n.update.auto), label: 'Update by itself when the core runs a newer version', onChange: (v) => setAuto(n, v) }))
          : null,
        offers(n),
        h('div.actions', actions(n)));
    }

    function paintAll() {
      if (!data) return;
      paint(notice, { enabled: data.enabled, listen: data.listen }, ({ enabled, listen }) => {
        if (!enabled) return callout('info', 'Nodes are off', 'Set nodes.enabled in the config and restart the bridge to let other machines connect. The nodes below stay as they are.');
        return listen ? h('p.muted.small', 'Node port: ', mono(listen)) : callout('warn', 'The node port is not open', 'The bridge could not open nodes.listen; its log says why.');
      });
      joinBtn.disabled = !data.enabled;
      const nodes = data.nodes || [];
      if (!nodes.length) {
        paint(list, 'none', () => h('div.card', empty('No nodes yet', 'Make a join code and run the line it gives you on the machine that should join.')));
        return;
      }
      paint(list, 'nodes', () => null);
      reconcile(list, nodes.map(stableOf), (n) => n.id, card);
      for (const n of nodes) {
        const region = liveRegions.get(n.id);
        if (region && region.isConnected) paint(region, liveOf(n), live);
      }
    }

    ctx.follow(nodesResource, (d) => {
      data = d || { nodes: [] };
      paintAll();
    }, 3000);
  },
};
