/* The one way the dashboard reads and writes browser storage (#4116, Security's storage audit #4113). A browser that blocks site
 * storage (a privacy setting, a locked-down profile, some embedded views) throws a SecurityError on the first access of
 * window.localStorage or sessionStorage, and a module-level read (utils.js, tasks.js) then took the whole dashboard down at start.
 *
 *   import { browserLocal, browserSession } from './safe_storage.js';
 *   browserLocal.get(key) -> string | null   .set(key, value)   .remove(key)   .keys() -> string[]   (browserSession the same)
 *
 * Each call is in a try/catch. When the storage can't be reached, or a write fails (blocked, full), the value is kept in memory for
 * this page load instead, so the dashboard behaves for the session and starts from its defaults at the next load. A value that
 * is in memory wins over the stored one for that key, and a write that succeeds drops the in-memory copy.
 *
 * test:cloud (storage_lint.test.js) fails on a direct localStorage or sessionStorage use anywhere else; theme_init.js is the one
 * exception (a classic script in <head>, before any module, with its own try/catch). */

function area(kind) {
    try { return kind === 'session' ? globalThis.sessionStorage : globalThis.localStorage; } catch { return null; }
}

function makeStore(kind) {
    const mem = new Map();
    return {
        get(key) {
            if (mem.has(key)) return mem.get(key);
            try { const s = area(kind); return s ? s.getItem(key) : null; } catch { return null; }
        },
        set(key, value) {
            const v = String(value);
            try {
                const s = area(kind);
                if (s) { s.setItem(key, v); mem.delete(key); return; }
            } catch { /* blocked or full: keep it for this page load */ }
            mem.set(key, v);
        },
        remove(key) {
            mem.delete(key);
            try { area(kind)?.removeItem(key); } catch { /* blocked */ }
        },
        /** Every key held, stored or in memory. */
        keys() {
            const out = new Set(mem.keys());
            try {
                const s = area(kind);
                if (s) for (let i = 0; i < s.length; i++) { const k = s.key(i); if (k != null) out.add(k); }
            } catch { /* blocked */ }
            return [...out];
        },
    };
}

export const browserLocal = makeStore('local');
export const browserSession = makeStore('session');
