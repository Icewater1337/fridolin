// Runs before the page paints, as a classic (blocking) script: applies the
// saved light/dark choice, so a dark dashboard does not flash white, and
// watches the boot, so a dashboard that fails to start says so instead of
// showing "Loading dashboard…" for ever.
(function () {
  // The theme is light, dark or auto (the system's). The page's data-theme
  // says which is in effect, and localStorage keeps it for the next visit.
  // The shell's theme button (app/shell.js) changes it through
  // window.fridolinTheme, as modules cannot import from a classic script.
  var KEY = 'bridgeTheme';
  var root = document.documentElement;
  var apply = function (t) {
    if (t === 'light' || t === 'dark') root.setAttribute('data-theme', t);
    else root.removeAttribute('data-theme');
  };
  window.fridolinTheme = {
    current: function () { return root.getAttribute('data-theme') || 'auto'; },
    set: function (t) {
      apply(t);
      try { localStorage.setItem(KEY, t); } catch (e) { /* storage blocked: this page only */ }
    },
  };
  var saved = 'auto';
  try { saved = localStorage.getItem(KEY) || 'auto'; } catch (e) { /* storage blocked */ }
  apply(saved);

  // The app sets data-ready once it shows something. Until then, a module
  // that fails to load (an error event on its <script>, which only a
  // capturing listener sees) or throws while it runs means it never will.
  var failed = function () {
    if (document.documentElement.hasAttribute('data-ready')) return;
    var msg = document.querySelector('.boot-msg');
    if (msg) msg.textContent = 'The dashboard failed to load. The browser console says why; reloading the page may help.';
  };
  window.addEventListener('error', function (e) {
    var el = /** @type {Element | null} */ (e.target);
    if (e.error !== undefined || (el && el.tagName === 'SCRIPT')) failed();
  }, true);
  window.addEventListener('load', function () { setTimeout(failed, 10000); });
})();
