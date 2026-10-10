/* Apply the saved theme before first paint (classic script, runs in <head>). */
(function () {
    var t = 'dark';
    try { t = localStorage.getItem('coral-theme') || 'dark'; } catch (_) {}
    if (t === 'system') t = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', t);
})();
