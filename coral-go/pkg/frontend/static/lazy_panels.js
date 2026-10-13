/* Panels that are not on the landing view load on first use (#3083 phase 2):
 * Workflows, the token/cost dashboard, Docs, the theme configurator, the
 * template browser and chat search. app.js puts a stub for each of their
 * window functions in place (inline handlers and switchNavTab call them); the
 * first call imports the module, swaps the real functions onto window and runs.
 *
 * A failed import shows a toast and is forgotten, so the next use tries again.
 * Chrome caches a failed module fetch for the life of the page (the same URL
 * is never requested again), so a retry imports the module with a ?retry=N
 * suffix. Every importer of these modules must go through the loaders below:
 * a plain import('./docs.js') elsewhere would get the cached failure. */

function panel(label, importer) {
    let pending = null;
    let attempt = 0;
    const load = () => {
        if (!pending) {
            const suffix = attempt ? `?retry=${attempt}` : '';
            attempt++;
            pending = importer(suffix).then(m => { load.module = m; load.loaded.forEach(f => f(m)); return m; }, err => {
                pending = null;
                console.error('[coral]', err);
                import('./utils.js').then(u => u.showToast(`Could not load ${label}. Check the connection to Coral, then try again.`, true)).catch(() => {});
                throw err;
            });
        }
        return pending;
    };
    load.module = null;
    load.loaded = []; // called with the module once it loads (lazyFns swaps its stubs)
    return load;
}

export const loadWorkflows = panel('Workflows', s => import('./workflows.js' + s));
export const loadCostDashboard = panel('the token dashboard', s => import('./cost_dashboard.js' + s));
export const loadDocs = panel('Docs', s => import('./docs.js' + s));
export const loadThemeConfig = panel('the theme editor', s => import('./theme_config.js' + s));
export const loadTemplateBrowser = panel('the template browser', s => import('./template_browser.js' + s));
export const loadChatSearch = panel('chat search', s => import('./chat_search.js' + s));

/** Stubs for `names` exported by the module `load` imports. A stub loads the
 *  module and calls through. However the module loads (a stub, or switchNavTab
 *  through its loader), every stub of it on window becomes the real function,
 *  so later calls are direct and synchronous. A failed
 *  load resolves to undefined: the toast already told the user. */
export function lazyFns(load, names) {
    const stubs = {};
    for (const name of names) {
        stubs[name] = (...args) => load().then(m => m[name](...args), () => undefined);
    }
    load.loaded.push(m => {
        for (const n of names) if (window[n] === stubs[n] && typeof m[n] === 'function') window[n] = m[n];
    });
    return stubs;
}
