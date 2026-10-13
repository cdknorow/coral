/* Apply the saved theme before first paint (classic script, runs in <head>). */
(function () {
    var t = 'dark';
    try { t = localStorage.getItem('coral-theme') || 'dark'; } catch (_) {}
    if (t === 'system') t = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', t);
})();
/* The static prefix this page loaded from (/static/ or a versioned /static/b/<build>/).
   Scripts and tools that import app modules from outside the module graph (tests,
   devtools) import from here, so they share the page's module instances. */
try { window.__coralStatic = new URL('./', document.currentScript.src).pathname; } catch (_) { window.__coralStatic = '/static/'; }
