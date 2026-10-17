/* Retry-safe dynamic imports (#3116). Every import() in the frontend goes
 * through importOnce (import_hygiene.test.js enforces it).
 *
 * Chrome caches a failed module fetch for the life of the page: the same URL is
 * never requested again, so one dropped request (for example over the Cloud Hub
 * tunnel) would break that feature until a reload. A failed load is therefore
 * forgotten and the next call imports the module with a ?retry=N suffix. A
 * successful load is shared by every caller, so a module retried in under a new
 * URL is still one instance.
 *
 * Specifiers resolve against this file's URL (the static root), so the
 * build-versioned /static/b/<build>/ prefix is kept. They are root-relative,
 * not caller-relative: a module in a subdirectory still writes './dir/x.js'.
 * Modules loaded only through here must import (statically) only modules
 * already in app.js's graph: a failed dependency is cached too, and a retried
 * parent would hit it again.
 * import_hygiene.test.js checks that as well.
 *
 * This module has no imports of its own, so any module can use it. */

const loads = new Map(); // resolved URL -> { pending, attempt }

/* Modules the bundle (#4228) already holds: app.js's startup graph and the modules startup always imports. The bundle registers their
 * namespaces here when it starts, so importOnce answers for them with no request: a bundled module's URL is only a stub that re-exports
 * the bundle's instance, and fetching it cost a round trip on the way to the first paint (sessions.js asked for render.js there, the
 * startup tasks for terminal_backend.js and its five stubs, #4326). Empty when the page is not bundled (the pop-out, CORAL_NO_BUNDLE=1). */
const bundled = new Map(); // './render.js' -> module namespace

export function registerBundled(namespaces) {
    for (const [name, ns] of Object.entries(namespaces)) bundled.set('./' + name, ns);
}

/** Import `spec` ('./name.js'), sharing one load per module. On failure the
 *  promise rejects, the attempt is forgotten, and, when `label` is given (a
 *  load the user asked for), a toast says what could not be loaded. Background
 *  callers omit it. */
export function importOnce(spec, label) {
    if (bundled.has(spec)) return Promise.resolve(bundled.get(spec));
    const url = new URL(spec, import.meta.url).href;
    let entry = loads.get(url);
    if (!entry) loads.set(url, entry = { pending: null, attempt: 0 });
    if (!entry.pending) {
        const suffix = entry.attempt ? `?retry=${entry.attempt}` : '';
        entry.attempt++;
        entry.pending = import(url + suffix).catch(err => {
            entry.pending = null;
            console.error('[coral]', err);
            if (label) {
                importOnce('./utils.js').then(u => u.showToast(`Could not load ${label}. Check the connection to Coral, then try again.`, true)).catch(() => {});
            }
            throw err;
        });
    }
    return entry.pending;
}
