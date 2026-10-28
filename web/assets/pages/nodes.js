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
import { hostMemoryText, memText } from '../lib/fride.js';
import { btn } from '../components/buttons.js';
import { confirmDialog, openDialog } from '../components/dialog.js';
import { select, textInput, toggle } from '../components/fields.js';
import { instanceBadge, stopInstance } from '../components/fride-instance.js';
import { nodeTerminal } from '../components/node-terminal.js';
import { toast } from '../components/toast.js';
import { badge, callout, empty, kv, mono, pageHeader, pill, spinner, timeAgo } from '../components/common.js';

/** @import { PageModule } from '../app/page.js' */
/** @import { BackupCap, CoreHostView, ExecRule, FrideCap, FrideHostCap, HostStats, Node as FleetNode, NodeCapabilities, NodesResponse, WorkstationCap } from '../../types/api.js' */

/**
 * A node as its card shows it: everything but what its live regions show,
 * which changes with every poll and so would rebuild the card: whether it
 * is online and its resources, and fride_host, whose memory and instances
 * change as they run. The card keeps what its capabilities come to in
 * words (capSummary), fride_host included.
 * @typedef {Omit<FleetNode, LiveField> & Partial<Record<LiveField, undefined>> & {summary: Record<CapSet, string>}} StableNode
 * @typedef {'online' | 'last_seen' | 'resources' | 'address'} LiveField
 * @typedef {'declared' | 'approved' | 'effective'} CapSet
 */

/**
 * What a node's FrIDE host region shows: fride_host as it reports it, and
 * whether its instances can be stopped from here.
 * @typedef {{cap: FrideHostCap, canStop: boolean}} HostView
 */

/**
 * c without fride_host.
 * @param {NodeCapabilities} c
 * @returns {NodeCapabilities}
 */
function withoutHost(c) {
  if (!c || !c.fride_host) return c;
  const rest = { ...c };
  delete rest.fride_host;
  return rest;
}

/**
 * A node's terminal as its card offers it: the folders granted for it.
 * @typedef {{id: string, name: string, roots: string[]}} TermTarget
 */

export default /** @satisfies {PageModule} */ ({
  title: 'Nodes',
  render(root, ctx) {
    /** @type {Partial<NodesResponse> | null} */
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

    /**
     * @param {StableNode} n
     * @param {string} path
     * @param {string} done
     */
    async function act(n, path, done) {
      await post(`/api/nodes/${encodeURIComponent(n.id)}/${path}`, {});
      toast(done, 'ok');
      await refresh();
    }

    // Updates: the core sends the node the build of its own version; the
    // node swaps it in and restarts, and goes back by itself when the new
    // one does not connect. A busy node (a FrIDE task, a backup) is
    // updated only when the owner says so.
    /** @param {StableNode} n */
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

    /**
     * @param {StableNode} n
     * @param {boolean} auto
     */
    async function setAuto(n, auto) {
      await put(`/api/nodes/${encodeURIComponent(n.id)}/update-policy`, { auto });
      toast(auto ? `${n.name} updates by itself from now on` : `${n.name} waits for you to update it`, 'ok');
      await refresh();
    }

    /** @param {StableNode} n */
    async function approveCommands(n) {
      await act(n, 'approve-commands', `Approved what ${n.name} offers`);
    }

    /** @param {StableNode} n */
    async function revoke(n) {
      const ok = await confirmDialog({
        title: `Revoke ${n.name}?`,
        message: 'It is disconnected now and refused from then on. To bring the machine back, it joins again with a new code.',
        confirm: 'Revoke', danger: true,
      });
      if (ok) await act(n, 'revoke', `Revoked ${n.name}`);
    }

    /** @param {StableNode} n */
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
    /** @param {TermTarget} n */
    async function openTerminal(n) {
      const { roots } = n;
      const pick = select(roots, roots[0], { 'aria-label': 'Folder' });
      const holder = h('div.term-holder');
      // The open terminal; an object, since the dialog's button replaces it.
      /** @type {{current: ReturnType<typeof nodeTerminal> | null}} */
      const term = { current: null };
      await openDialog({
        title: `Terminal on ${n.name}`,
        wide: true,
        body: [h('p.muted.small', 'A shell on the node, in a folder it granted. Every command line you type is in the audit log; a lockdown closes it.'), pick, holder],
        actions: [
          { label: 'Close', value: null },
          {
            label: 'Open', kind: 'primary',
            onClick: async () => {
              if (term.current) await term.current.close();
              term.current = nodeTerminal({ node: n.id, folder: pick.value });
              holder.replaceChildren(term.current.el);
              return false;
            },
          },
        ],
      });
      if (term.current) await term.current.close();
    }

    /** @param {StableNode} n */
    function statePills(n) {
      const out = [];
      if (n.device === 'pending') out.push(badge('device waiting', 'warn'));
      else if (n.device === 'revoked') out.push(badge('revoked', 'bad'));
      else if (n.commands === 'pending') out.push(badge(n.commands_approved_at ? 'offers more than approved' : 'commands waiting', 'warn'));
      else out.push(badge('approved', 'ok'));
      return out;
    }

    /** @param {StableNode} n */
    function actions(n) {
      const out = [];
      if (n.device === 'pending') {
        out.push(btn('Approve device', { kind: 'primary', small: true, icon: 'check', onClick: () => act(n, 'approve', `Approved the device ${n.name}`) }));
      } else if (n.device === 'approved' && n.commands === 'pending') {
        out.push(btn('Approve what it offers', { kind: 'primary', small: true, icon: 'check', onClick: () => approveCommands(n) }));
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

    /**
     * @param {StableNode} n
     * @param {HTMLElement} hostRegion where fride_host goes (paintAll)
     */
    function offers(n, hostRegion) {
      const declared = n.declared || {};
      const rules = ((declared.exec && declared.exec.allow) || []).filter((r) => !r.core);
      const roots = (declared.fs && declared.fs.roots) || [];
      return h('div.node-offers',
        rules.length ? h('div', h('strong', 'Commands: '), rules.map((r) => mono(ruleText(r))).flatMap((el, i) => (i ? [', ', el] : [el]))) : null,
        programsLines(n),
        roots.length ? h('div', h('strong', 'Folders: '), roots.map((r) => mono(`${r.path}${r.write ? ' (read and write)' : ' (read only)'}`)).flatMap((el, i) => (i ? [', ', el] : [el]))) : null,
        frideLine(declared.fride),
        hostRegion,
        backupLine(declared.backup, n.approved && n.approved.backup),
        workLine('Agent sessions', declared.agent),
        workLine('Terminal', declared.terminal),
        coreHostLine(n.core_host));
    }

    // core-host (docs/NODES.md, "core-host"): every node is one unless
    // it turned it off; the owner's !movecore vote confirms it, never
    // this page.
    /** @param {CoreHostView | undefined} ch */
    function coreHostLine(ch) {
      if (!ch) return null;
      const to = ch.send_to || [];
      return h('div', h('strong', 'Core host: '), 'device key ', mono(ch.fingerprint),
        h('span.muted', ch.confirmed ? ' · confirmed: it may hold the secrets' : ' · never held the core: the first !movecore vote in WhatsApp confirms it'),
        to.length ? h('span.muted', ` · pins ${to.join(', ')}`) : null);
    }

    // Programs from the core: the folders node.json opts in, the programs
    // the core added for approved command agents (with those agents), and
    // what the node refused of the core's last list, and why.
    /** @param {StableNode} n */
    function programsLines(n) {
      const folders = (n.declared && n.declared.exec && n.declared.exec.accept_from_core) || [];
      const added = ((n.declared && n.declared.exec && n.declared.exec.allow) || []).filter((r) => r.core);
      const st = n.programs || null;
      if (!folders.length && !added.length && !st) return null;
      /** @param {ExecRule} r */
      const agentsOf = (r) => {
        const p = st && (st.accepted || []).find((x) => x.name === r.name && (x.args || []).join('\0') === (r.args || []).join('\0'));
        const agents = (p && p.agents) || [];
        return agents.length ? h('span.muted', ` for ${agents.join(', ')}`) : null;
      };
      return [
        folders.length ? h('div', h('strong', 'Programs from the core: '), folders.map((f) => mono(f)).flatMap((el, i) => (i ? [', ', el] : [el])),
          h('span.muted', ' · approving a command agent adds its program from these folders')) : null,
        added.length ? h('div', h('strong', 'Added for agents: '), h('ul.plain-list', added.map((r) => h('li', mono(ruleText(r)), agentsOf(r))))) : null,
        st && (st.refused || []).length ? h('div', h('strong', 'Refused: '), h('ul.plain-list', st.refused.map((r) => h('li', mono(r.name), h('span.muted', ` — ${r.why}`))))) : null,
        st && st.error ? h('div.warn-text.small', `The core’s last list did not reach it: ${st.error}`) : null,
      ];
    }

    // Workstation: the folders an agent session or a terminal may run in,
    // the providers the node found and how it confines them.
    /**
     * @param {string} label
     * @param {WorkstationCap | undefined} w
     */
    function workLine(label, w) {
      if (!w) return null;
      const extra = [w.providers && w.providers.length ? w.providers.join(', ') : null, w.sandbox ? `sandbox ${w.sandbox}` : null].filter(Boolean);
      return h('div', h('strong', `${label}: `),
        (w.roots || []).map((r) => mono(r)).flatMap((el, i) => (i ? [', ', el] : [el])),
        extra.length ? h('span.muted', ` · ${extra.join(' · ')}`) : null);
    }

    // Backups: the places the node lends, and the folders the owner
    // picked for the Vault page's targets.
    /**
     * @param {BackupCap | undefined} b
     * @param {BackupCap | undefined} approved
     */
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
    /** @param {FrideCap | undefined} f */
    function frideLine(f) {
      if (!f) return null;
      const repos = f.repos || [];
      return h('div.node-fride', h('strong', 'FrIDE: '),
        h('a', { href: f.url, target: '_blank', rel: 'noopener noreferrer' }, f.url),
        repos.length ? h('span.muted', ` · ${repos.join(', ')}`) : null);
    }

    // FrIDE instances (fride_host): sbx on the node, its memory, and the
    // instances it has. A running one can be stopped here once fride_host
    // is approved; the core starts them when a job needs one, or the FrIDE
    // page does.
    /** @param {string} name */
    async function stop(name) {
      if (await stopInstance(name)) await refresh();
    }

    /** @param {HostView | null} v */
    function hostView(v) {
      if (!v) return null;
      const { cap, canStop } = v;
      const instances = cap.instances || [];
      return [
        h('div', h('strong', 'FrIDE instances: '), cap.version ? `sbx ${cap.version}` : 'sbx', ' · ',
          cap.ready ? 'ready' : h('span.warn-text', cap.problem ? `not ready: ${cap.problem}` : 'not ready'),
          h('span.muted', ` · memory ${hostMemoryText(cap)}`)),
        instances.length
          ? h('ul.plain-list.fride-host-instances', instances.map((i) => h('li',
            h('span', mono(i.name), ' ', instanceBadge(i.state),
              i.memory_mb ? h('span.muted', ` ${memText(i.memory_mb)}`) : null,
              i.problem ? h('span.warn-text', ` · ${i.problem}`) : null),
            canStop && i.state === 'running' ? btn('Stop', { small: true, kind: 'danger', icon: 'stop', title: `Stop ${i.name}`, onClick: () => stop(i.name) }) : null)))
          : h('div.muted', 'No instances yet: the core brain starts one here when a job needs it.'),
      ];
    }

    /**
     * @param {FleetNode} n
     * @returns {HostView | null}
     */
    const hostOf = (n) => {
      const approved = n.effective && n.effective.fride_host;
      const cap = approved || (n.declared && n.declared.fride_host);
      return cap ? { cap, canStop: !!approved && n.online && n.device === 'approved' } : null;
    };

    // What changes with every heartbeat: whether it is online and its
    // free resources. It is painted into the card on its own, so the card
    // and its buttons stay while a node reports (see paintAll).
    /**
     * @param {FleetNode} n
     * @returns {TermTarget | null}
     */
    const termOf = (n) => {
      const roots = (n.effective && n.effective.terminal && n.effective.terminal.roots) || [];
      return n.online && roots.length ? { id: n.id, name: n.name, roots } : null;
    };
    /** @param {FleetNode} n */
    const liveOf = (n) => ({ online: !!n.online, last_seen: n.last_seen || null, resources: /** @type {Partial<HostStats>} */ (n.resources || {}), address: n.address || null, term: termOf(n) });
    /**
     * @param {FleetNode} n
     * @returns {StableNode}
     */
    const stableOf = (n) => ({
      ...n, online: undefined, last_seen: undefined, resources: undefined, address: undefined,
      declared: withoutHost(n.declared), approved: withoutHost(n.approved), effective: withoutHost(n.effective),
      summary: { declared: capSummary(n.declared), approved: capSummary(n.approved), effective: capSummary(n.effective) },
    });
    const liveRegions = new Map(); // node id → its live region
    const hostRegions = new Map(); // node id → its FrIDE host region

    /** @param {ReturnType<typeof liveOf>} l */
    function live(l) {
      const res = l.resources;
      const { term } = l;
      const memory = res.mem_total ? `${gb(res.mem_available)} free of ${gb(res.mem_total)}` : null;
      return [
        h('div.node-online', l.online ? pill('online', 'ok', { pulse: true }) : pill('offline', 'neutral'),
          l.online ? null : h('span.muted.small', 'last seen ', timeAgo(l.last_seen)),
          term ? btn('Terminal', { small: true, icon: 'terminal', onClick: () => openTerminal(term) }) : null),
        kv([
          ['CPUs', res.cpus ? String(res.cpus) : null],
          ['Memory', memory],
          ['Disk free', res.disk_free ? gb(res.disk_free) : null],
          ['Battery', typeof res.battery === 'number' ? `${res.battery}%` : null],
          ['Address', l.address],
        ]),
      ];
    }

    // What waits for approval, in the words of the owner's WhatsApp
    // alert: how it was started, and what approving lets it do.
    /** @param {StableNode} n */
    function waitingCallout(n) {
      let title;
      if (n.device === 'pending') title = 'Waiting for you to approve this device';
      else if (n.device === 'approved' && n.commands === 'pending') title = n.commands_approved_at ? 'It asks to do more than you approved' : 'Waiting for you to approve what it offers';
      else return null;
      const started = n.device !== 'pending' ? 'The machine asked for this itself.'
        : n.joined_via === 'chat' ? 'Started with a join code you asked for with !join.'
          : n.joined_via === 'dashboard' ? 'Started with a join code from this dashboard (or fridolin nodes join-code).'
            : 'Unexpected: its join code came neither from your !join nor from the dashboard.';
      const allows = n.allows || [];
      return callout('warn', title,
        h('p', started, ' Was this you?'),
        allows.length ? h('p', 'If you approve, it can:') : null,
        allows.length ? h('ul', allows.map((a) => h('li', a))) : null,
        h('p.muted.small', n.commands_approved_at && n.device === 'approved'
          ? `Until you approve, it serves only what you approved before: ${n.summary.approved}.`
          : 'It serves nothing until you approve. In WhatsApp, !approve does the same.'));
    }

    /** @param {StableNode} n */
    function card(n) {
      const region = h('div.node-live');
      liveRegions.set(n.id, region);
      const hostRegion = h('div.node-fride-host', { hidden: true });
      hostRegions.set(n.id, hostRegion);
      return h('section.card.node-card',
        h('div.card-head',
          h('div.card-title', h('h2', n.name), h('span.muted', osName(n.os), n.arch ? ` · ${n.arch}` : '')),
          h('div.node-state', statePills(n))),
        waitingCallout(n),
        n.fride_approval && n.device === 'approved'
          ? callout('warn', 'fride waits for your approval', n.fride_approval)
          : null,
        region,
        kv([
          ['ID', mono(n.id, { copy: true })],
          ['May run', n.summary.effective],
          ['Offers', n.summary.declared],
          ['Tags', n.tags && n.tags.length ? h('span.tag-list', n.tags.map((t) => badge(t))) : h('span.muted', 'none')],
          ['Version', n.version || null],
          ['Update', updateText(n.update) || n.update_note || null],
        ]),
        n.device === 'approved' && n.effective && n.effective.update
          ? h('div.node-auto-update', toggle({ checked: !!(n.update && n.update.auto), label: 'Update by itself when the core runs a newer version', onChange: (v) => setAuto(n, v) }))
          : null,
        offers(n, hostRegion),
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
        const host = hostRegions.get(n.id);
        if (host && host.isConnected) {
          const v = hostOf(n);
          host.hidden = !v;
          paint(host, v, hostView);
        }
      }
    }

    ctx.follow(nodesResource, (d) => {
      data = d || { nodes: [] };
      paintAll();
    }, 3000);
  },
});
