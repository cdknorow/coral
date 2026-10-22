/* Apply the saved theme before first paint (classic script, runs in <head>). Only dark, light and system are themes here: anything else (a name a hub stored, an old value) is not one, and a page with no saved choice
   starts dark on this computer and follows the system on the Cloud Hub, its sign-in and connect pages included (#6087: a phone through the hub has no saved choice, and must never be left with no theme tokens). */
(function () {
    var mode = document.documentElement.hasAttribute('data-hub-page') ? 'cloud' : 'local'; // the hub's own pages (sign-in, connect a machine) say so on <html>: they follow the system too (#6094); this computer's own pages (the dashboard, its sign-in and password pages) do not
    try { var boot = document.getElementById('coral-boot'); if (boot) mode = JSON.parse(boot.textContent).mode || 'local'; } catch (_) {}
    var t = null;
    try { t = localStorage.getItem('coral-theme'); } catch (_) {}
    if (t !== 'dark' && t !== 'light' && t !== 'system') t = mode === 'cloud' ? 'system' : 'dark';
    if (t === 'system') t = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', t);
})();
/* The static prefix this page loaded from (/static/ or a versioned /static/b/<build>/).
   Scripts and tools that import app modules from outside the module graph (tests,
   devtools) import from here, so they share the page's module instances. */
try { window.__coralStatic = new URL('./', document.currentScript.src).pathname; } catch (_) { window.__coralStatic = '/static/'; }
