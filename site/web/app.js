// The site's home page: sign in, then a tile for every module you may open.

const app = document.getElementById('app');

// h('p', {class: 'x'}, 'text', child) — the attributes may be left out.
function h(tag, attrs, ...children) {
  if (attrs === null || attrs === undefined || typeof attrs !== 'object' || attrs instanceof Node || Array.isArray(attrs)) {
    if (attrs !== null && attrs !== undefined) children.unshift(attrs);
    attrs = {};
  }
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'style') for (const [p, val] of Object.entries(v)) el.style.setProperty(p, val);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) if (c !== null && c !== undefined && c !== false) el.append(c);
  return el;
}

function show(...nodes) {
  app.className = '';
  app.replaceChildren(...nodes);
}

async function json(url, init) {
  const r = await fetch(url, { credentials: 'same-origin', cache: 'no-store', ...init });
  let body = null;
  try { body = await r.json(); } catch { /* empty */ }
  return { ok: r.ok, status: r.status, body };
}

const next = new URLSearchParams(location.search).get('next') || '/';

function signIn(site, me) {
  const err = h('p', { class: 'error', role: 'alert' });
  const parts = [h('h1', site.title), h('p', { class: 'muted' }, 'Sign in to continue.')];
  if (me.google) {
    parts.push(h('a', { class: 'button primary', href: `/auth/google?next=${encodeURIComponent(next)}` }, 'Continue with Google'));
  }
  if (me.google && me.password) parts.push(h('div', { class: 'or' }, 'or'));
  if (me.password) {
    const user = h('input', { name: 'username', autocomplete: 'username', required: true });
    const pass = h('input', { name: 'password', type: 'password', autocomplete: 'current-password', required: true });
    const go = h('button', { type: 'submit', class: me.google ? '' : 'primary' }, 'Sign in');
    parts.push(h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        go.disabled = true;
        err.textContent = '';
        const r = await json('/auth/login', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username: user.value, password: pass.value }),
        });
        go.disabled = false;
        if (r.ok) location.assign(next.startsWith('/') && !next.startsWith('//') ? next : '/');
        else err.textContent = (r.body && r.body.error) || 'Sign-in failed';
      },
    }, h('label', {}, 'Username', user), h('label', {}, 'Password', pass), go, err));
  }
  if (!me.google && !me.password) {
    parts.push(h('p', { class: 'muted' }, 'Nobody can sign in yet. The site’s owner adds accounts in Fridolin’s dashboard.'));
  }
  show(h('section', { class: 'signin' }, parts));
}

function home(site, me, list) {
  const who = h('div', { class: 'who' },
    me.picture ? h('img', { src: me.picture, alt: '', referrerpolicy: 'no-referrer' }) : null,
    h('span', me.name || me.email),
    h('button', {
      class: 'small',
      onclick: async () => { await json('/auth/logout', { method: 'POST' }); location.assign('/'); },
    }, 'Sign out'));
  const tiles = list.modules.map((m) => h('a', { class: 'tile', href: m.href, style: { '--tile': m.color } },
    h('span', { class: 'tile-icon', 'aria-hidden': 'true' }, m.icon),
    h('div', {}, h('h3', m.title), m.description ? h('p', m.description) : null)));
  const broken = list.broken.length
    ? h('div', { class: 'broken' }, list.broken.map((b) => h('p', {}, '⚠️ Module ', h('strong', b.id), ` did not load: ${b.error}`)))
    : null;
  show(
    h('header', { class: 'topbar' }, h('h1', site.title), who),
    h('div', { class: 'content' },
      h('h2', `Hello, ${(me.name || me.email).split(' ')[0]}`),
      broken,
      tiles.length ? h('div', { class: 'tiles' }, tiles)
        : h('div', { class: 'empty' }, me.role === 'admin'
          ? 'Nothing here yet. Build your first module from Fridolin’s dashboard.'
          : 'Nothing here for you yet.')));
}

(async () => {
  const site = (await json('/_fridolin/site.json')).body || { title: 'Site' };
  document.title = site.title;
  if (site.preview) {
    const bar = document.getElementById('preview');
    bar.replaceChildren(`Preview of ${site.preview} — not live yet`,
      site.exit ? h('span', {}, ' · ', h('a', { href: '/_fridolin/preview-exit' }, 'Back to the live site')) : '');
    bar.hidden = false;
  }
  const me = await json('/auth/me');
  if (!me.ok) { signIn(site, me.body || {}); return; }
  const list = await json('/api/modules');
  home(site, me.body, list.ok ? list.body : { modules: [], broken: [] });
})();
