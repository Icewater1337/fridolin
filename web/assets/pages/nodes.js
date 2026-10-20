// Nodes: the machines of the fleet (docs/NODES.md). A node joins with a
// code from here or `fridolin nodes join-code`, waits for its device to be
// approved, then for what it offers to be approved, and may be revoked at
// any time; each step goes through the bridge (/api/nodes/...), which the
// node hears at once.

import { post, put } from '../app/api.js';
import { nodesResource } from '../app/resources.js';
import { h, mount, paint, reconcile } from '../lib/dom.js';
import { absTime } from '../lib/format.js';
import { capSummary, gb, osName, parseTags, ruleText } from '../lib/nodes.js';
import { btn } from '../components/buttons.js';
import { confirmDialog, openDialog } from '../components/dialog.js';
import { textInput } from '../components/fields.js';
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
          h('p', `On the new machine, with fridolin installed, run this line. The code works once, until ${absTime(r.expires_at)}.`),
          h('div.join-command', mono(r.command, { copy: true })),
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

    function statePills(n) {
      const out = [n.online ? pill('online', 'ok', { pulse: true }) : pill('offline', 'neutral')];
      if (n.device === 'pending') out.push(badge('device waiting', 'warn'));
      else if (n.device === 'revoked') out.push(badge('revoked', 'bad'));
      else if (n.commands === 'pending') out.push(badge('offers more than approved', 'warn'));
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
        roots.length ? h('div', h('strong', 'Folders: '), roots.map((r) => mono(`${r.path}${r.write ? ' (read and write)' : ' (read only)'}`)).flatMap((el, i) => (i ? [', ', el] : [el]))) : null);
    }

    function card(n) {
      const res = n.resources || {};
      const memory = res.mem_total ? `${gb(res.mem_available)} free of ${gb(res.mem_total)}` : null;
      return h('section.card.node-card',
        h('div.card-head',
          h('div.card-title', h('h2', n.name), h('span.muted', osName(n.os), n.arch ? ` · ${n.arch}` : '')),
          h('div.node-state', statePills(n))),
        n.device === 'pending' ? callout('warn', 'Waiting for approval', `Approve this device if you just joined ${n.name} yourself. What it offers is approved in a second step.`) : null,
        n.device === 'approved' && n.commands === 'pending'
          ? callout('warn', 'It offers more than you approved', `Approved so far: ${capSummary(n.approved)}. It now offers: ${capSummary(n.declared)}. Until you approve, it serves only what both include.`)
          : null,
        kv([
          ['ID', mono(n.id, { copy: true })],
          ['Last seen', n.online ? 'now' : timeAgo(n.last_seen)],
          ['May run', capSummary(n.effective)],
          ['Offers', capSummary(n.declared)],
          ['Tags', (n.tags || []).length ? h('span.tag-list', n.tags.map((t) => badge(t))) : h('span.muted', 'none')],
          ['CPUs', res.cpus ? String(res.cpus) : null],
          ['Memory', memory],
          ['Disk free', res.disk_free ? gb(res.disk_free) : null],
          ['Battery', typeof res.battery === 'number' ? `${res.battery}%` : null],
          ['Version', n.version || null],
          ['Address', n.address || null],
        ]),
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
      reconcile(list, nodes, (n) => n.id, card);
    }

    ctx.follow(nodesResource, (d) => {
      data = d || { nodes: [] };
      paintAll();
    }, 3000);
  },
};
