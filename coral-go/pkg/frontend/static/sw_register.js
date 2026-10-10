/* Register the service worker, except on the Cloud Hub: its pages and API
 * answers are per-user, and a worker that caches them could leak one account's
 * data to the next person on the same browser. */
(function () {
    var mode = 'local';
    try { mode = JSON.parse(document.getElementById('coral-boot').textContent).mode || 'local'; } catch (_) {}
    if (mode === 'cloud') {
        if ('serviceWorker' in navigator) {
            navigator.serviceWorker.getRegistrations().then(function (rs) { rs.forEach(function (r) { r.unregister(); }); }).catch(function () {});
        }
        return;
    }
    if ('serviceWorker' in navigator) {
        navigator.serviceWorker.register('/static/sw.js', { scope: '/', updateViaCache: 'none' }).catch(function () {});
    }
})();
