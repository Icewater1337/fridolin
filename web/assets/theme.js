// Runs before the page paints, as a classic (blocking) script: applies the
// saved light/dark choice, so a dark dashboard does not flash white, and
// watches the boot, so a dashboard that fails to start says so instead of
// showing "Loading dashboard…" for ever.
(function () {
  var t = 'auto';
  try { t = localStorage.getItem('bridgeTheme') || 'auto'; } catch (e) { /* storage blocked */ }
  if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);

  // The app sets data-ready once it shows something. Until then, a module
  // that fails to load (an error event on its <script>, which only a
  // capturing listener sees) or throws while it runs means it never will.
  var failed = function () {
    if (document.documentElement.hasAttribute('data-ready')) return;
    var msg = document.querySelector('.boot-msg');
    if (msg) msg.textContent = 'The dashboard failed to load. The browser console says why; reloading the page may help.';
  };
  window.addEventListener('error', function (e) {
    var el = e.target;
    if (e.error !== undefined || (el && el.tagName === 'SCRIPT')) failed();
  }, true);
  window.addEventListener('load', function () { setTimeout(failed, 10000); });
})();
