/* Panels that are not on the landing view load on first use (#3083 phase 2):
 * Workflows, the token/cost dashboard, Docs, the theme configurator, the
 * template browser and chat search. app.js puts a stub for each of their
 * window functions in place (inline handlers and switchNavTab call them); the
 * first call imports the module, swaps the real functions onto window and runs.
 *
 * Loads go through importOnce (import_once.js): a failed import shows a toast
 * and is forgotten, so the next use tries again (as ?retry=N, because Chrome
 * caches the failed URL for the life of the page). Every importer of these
 * modules must use the loaders below or importOnce, never a plain import(). */

import { importOnce } from './import_once.js';

function panel(label, spec) {
    let pending = null;
    const load = () => {
        if (!pending) {
            pending = importOnce(spec, label).then(m => { load.module = m; load.loaded.forEach(f => f(m)); return m; }, err => {
                pending = null;
                throw err;
            });
        }
        return pending;
    };
    load.module = null;
    load.loaded = []; // called with the module once it loads (lazyFns swaps its stubs)
    return load;
}

export const loadWorkflows = panel('Workflows', './workflows.js');
export const loadCostDashboard = panel('the token dashboard', './cost_dashboard.js');
export const loadDocs = panel('Docs', './docs.js');
export const loadThemeConfig = panel('the theme editor', './theme_config.js');
export const loadTemplateBrowser = panel('the template browser', './template_browser.js');
export const loadChatSearch = panel('chat search', './chat_search.js');
// The launch, settings, resume, info and team dialogs (#3140). settings_core.js keeps
// what runs at startup (loadSettings, the theme) and inside links (trackSupporterClick).
export const loadModals = panel('this dialog', './modals.js');

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

/** Stubs for functions a module assigns to window itself when it loads (not
 *  exports). The load replaces the stubs on window; a stub called before that
 *  waits for the load and calls the real one. */
export function lazyWindowFns(load, names) {
    const stubs = {};
    for (const name of names) {
        stubs[name] = (...args) => load().then(() => {
            const real = window[name];
            if (typeof real === 'function' && real !== stubs[name]) return real(...args);
        }, () => undefined);
    }
    return stubs;
}
