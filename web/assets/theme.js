// Applies the saved light/dark choice before the page paints, so a dark
// dashboard does not flash white. Loaded as a classic (blocking) script.
(function () {
  var t = 'auto';
  try { t = localStorage.getItem('bridgeTheme') || 'auto'; } catch (e) { /* storage blocked */ }
  if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);
})();
