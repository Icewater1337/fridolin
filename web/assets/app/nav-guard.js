// Asking before a page with unsaved changes is left: by a link, the
// address bar, Back or Forward. A hashchange comes after the browser has
// moved, so the guard notes in history.state where each entry is, puts the
// address back by going the other way, and goes again only once the
// person agreed. The history keeps its entries as they were: cancelling
// leaves no copy of the page behind, and Back still works.

/**
 * guardNavigation({win, isDirty, confirm, onDiscard, onNavigate}) watches
 * win's links and hashchanges. isDirty() says whether leaving needs
 * asking, confirm() asks (resolving true to leave), onDiscard() drops the
 * page's changes once the person agreed, and onNavigate() renders the page
 * for location.hash. Returns {go(hash), replace(hash)}: go navigates as a
 * link does, replace changes the address in place without leaving the
 * page. Every other history.replaceState must go through replace, which
 * keeps an entry's place.
 */
export function guardNavigation({ win, isDirty, confirm, onDiscard, onNavigate }) {
  const { history, location, document } = win;
  const placeOf = (state) => (state && typeof state.idx === 'number' ? state.idx : null);
  let idx = placeOf(history.state); // where the shown page's entry is
  if (idx === null) {
    idx = 0;
    history.replaceState({ idx }, '');
  }
  let shown = location.hash; // the address of the page shown
  let undoing = 0; // moves back to the shown page whose hashchange is no navigation
  let asking = false; // the "Discard changes?" dialog is open

  const ask = async () => {
    asking = true;
    try {
      return await confirm();
    } finally {
      asking = false;
    }
  };

  // Moving between entries with the same address fires no hashchange;
  // the page stays, but its place in the history is another one now.
  win.addEventListener('popstate', () => {
    const at = placeOf(history.state);
    if (location.hash === shown && at !== null && !undoing) idx = at;
  });

  win.addEventListener('hashchange', async () => {
    let to = placeOf(history.state);
    if (to === null) {
      // A new entry, from a link or the address bar: it follows this one.
      to = idx + 1;
      history.replaceState({ idx: to }, '');
    }
    if (undoing && to === idx) { undoing -= 1; return; }
    if (!asking && !isDirty()) {
      idx = to;
      shown = location.hash;
      onNavigate();
      return;
    }
    const delta = to - idx;
    undoing += 1;
    history.go(-delta);
    if (asking) return; // one question at a time
    if (!await ask()) return;
    onDiscard();
    history.go(delta);
  });

  // A link to a page (#/…) is held before the browser follows it, so it
  // adds no entry until the person agreed. Other # links stay on the page
  // (the skip link), so leaving is not what they do.
  document.addEventListener('click', (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target && e.target.closest ? e.target.closest('a[href^="#/"]') : null;
    if (!a || a.getAttribute('target')) return;
    if (!asking && !isDirty()) return;
    e.preventDefault();
    go(a.getAttribute('href'));
  }, true);

  async function go(hash) {
    if (hash === location.hash || asking) return;
    if (isDirty()) {
      if (!await ask()) return;
      onDiscard();
    }
    location.hash = hash;
  }

  return {
    go,
    replace(hash) {
      history.replaceState({ idx }, '', hash);
      shown = location.hash;
    },
  };
}
