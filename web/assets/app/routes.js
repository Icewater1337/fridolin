// Every page of the dashboard in one table: the sidebar, the router and the
// highlighted sidebar entry all come from it. Pages load on first visit, so a
// page that fails to load breaks only itself.

export const ROUTES = [
  { id: 'overview', path: 'overview', nav: { label: 'Overview', icon: 'overview' }, load: () => import('../pages/overview.js') },
  { id: 'activity', path: 'activity/:brain?', nav: { label: 'Activity', icon: 'activity' }, load: () => import('../pages/activity.js') },
  { id: 'brains', path: 'brains', nav: { label: 'Brains', icon: 'brain' }, load: () => import('../pages/brains.js') },
  { id: 'brain', path: 'brains/:id', navId: 'brains', load: () => import('../pages/brain.js') },
  { id: 'add-brain', path: 'add-brain', navId: 'brains', load: () => import('../pages/add-brain.js') },
  { id: 'chats', path: 'chats', nav: { label: 'Chats', icon: 'chats' }, load: () => import('../pages/chats.js') },
  { id: 'threads', path: 'threads', nav: { label: 'Threads', icon: 'clock' }, load: () => import('../pages/threads.js') },
  { id: 'approvals', path: 'approvals', nav: { label: 'Approvals', icon: 'shield' }, load: () => import('../pages/approvals.js') },
  { id: 'audit', path: 'audit/:run?', nav: { label: 'Audit', icon: 'lock' }, load: () => import('../pages/audit.js') },
  { id: 'settings', path: 'settings/:section?', nav: { label: 'Settings', icon: 'settings' }, load: () => import('../pages/settings.js') },
  { id: 'nodes', path: 'nodes', nav: { label: 'Nodes', icon: 'server' }, load: () => import('../pages/nodes.js') },
  { id: 'backup', path: 'backup', nav: { label: 'Backup', icon: 'backup' }, load: () => import('../pages/backup.js') },
  { id: 'vault', path: 'vault', nav: { label: 'Vault', icon: 'lock' }, load: () => import('../pages/vault.js') },
  { id: 'integrations', path: 'integrations', nav: { label: 'Integrations', icon: 'inbox' }, load: () => import('../pages/integrations.js') },
  { id: 'site', path: 'site', nav: { label: 'Site', icon: 'external' }, load: () => import('../pages/site.js') },
  { id: 'build', path: 'build', nav: { label: 'Build', icon: 'sparkle' }, load: () => import('../pages/build.js') },
  { id: 'logs', path: 'logs', nav: { label: 'Logs', icon: 'terminal' }, load: () => import('../pages/logs.js') },
  { id: 'dev', path: 'dev', nav: { label: 'Dev', icon: 'dev', devOnly: true }, load: () => import('../pages/dev.js') },
];
