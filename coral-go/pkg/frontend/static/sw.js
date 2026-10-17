/* Coral Service Worker — caches app shell for fast loads */

// v5: nothing is precached at install (#3973). The install step used to fetch the page again (30 KB), a 37 KB icon and a legacy
// style.css stub on every first visit, in the background of the load that was already fetching its own files. The offline fallback
// page is now the last good answer for "/" (cached by the fetch handler below on the first navigation the worker sees). Images and
// fonts come from the browser's HTTP cache: the handler below only answers from this cache when it already holds the file. Build-versioned /static/b/<build>/ files are not cached here (see fetch). Bumping the name drops
// what v4 accumulated.
const CACHE_NAME = 'coral-v5';

self.addEventListener('install', () => {
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

    // Build-versioned files (/static/b/<build>/, and the bundled /static/bb/<build>/) are immutable: the browser's HTTP
    // cache keeps them. Caching them here too would add a full copy per build that
    // nothing ever evicts.
    if (url.pathname.startsWith('/static/b/') || url.pathname.startsWith('/static/bb/')) {
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

    // HTML pages: network-first. The cached shell is only the offline fallback, so every good
    // answer for the page replaces it: a shell left from install time would name the build
    // of a version long gone (#3751).
    event.respondWith(
        fetch(event.request).then((response) => {
            if (event.request.mode === 'navigate' && url.pathname === '/' && response.ok) {
                const clone = response.clone();
                caches.open(CACHE_NAME).then((cache) => cache.put('/', clone));
            }
            return response;
        }).catch(() => caches.match('/'))
    );
});
