/* Coral Service Worker — caches app shell for fast loads */

// v4: build-versioned /static/b/<build>/ files are no longer cached here (see
// fetch); bumping the name drops what v3 accumulated.
const CACHE_NAME = 'coral-v4';
const SHELL_ASSETS = [
    '/',
    '/static/style.css',
    '/static/favicon.png',
    '/static/coral.png',
];

self.addEventListener('install', (event) => {
    event.waitUntil(
        caches.open(CACHE_NAME).then((cache) => cache.addAll(SHELL_ASSETS))
    );
    self.skipWaiting();
});

self.addEventListener('activate', (event) => {
    event.waitUntil(
        caches.keys().then((names) =>
            Promise.all(names.filter((n) => n !== CACHE_NAME).map((n) => caches.delete(n)))
        )
    );
    self.clients.claim();
});

self.addEventListener('fetch', (event) => {
    const url = new URL(event.request.url);

    // API and WebSocket requests: always go to network
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws')) {
        return;
    }

    // Build-versioned files (/static/b/<build>/) are immutable: the browser's HTTP
    // cache keeps them. Caching them here too would add a full copy per build that
    // nothing ever evicts.
    if (url.pathname.startsWith('/static/b/')) {
        return;
    }

    // Static JS/CSS: network-first (ensures code updates load after restart)
    if (url.pathname.startsWith('/static/') && (url.pathname.endsWith('.js') || url.pathname.endsWith('.css'))) {
        event.respondWith(
            fetch(event.request)
                .then((response) => {
                    const clone = response.clone();
                    caches.open(CACHE_NAME).then((cache) => cache.put(event.request, clone));
                    return response;
                })
                .catch(() => caches.match(event.request))
        );
        return;
    }

    // Other static assets (images, fonts): cache-first
    if (url.pathname.startsWith('/static/')) {
        event.respondWith(
            caches.match(event.request).then((cached) => cached || fetch(event.request))
        );
        return;
    }

    // HTML pages: network-first (fall back to cache for offline shell)
    event.respondWith(
        fetch(event.request).catch(() => caches.match('/'))
    );
});
