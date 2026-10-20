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
// The team view and the team's settings (with its Health check tab): opened from a team's button, not needed to start (#5364).
export const loadTeamContext = panel('the team view', './team_context.js');
export const loadTeamSettings = panel('the team settings', './team_working_mode.js');
// The task detail dialog and its editing, completing and cancelling (#5364): opened from a task row.
export const loadTaskDetail = panel('the task', './task_detail.js');
// The launch, settings, resume, info and team dialogs (#3140). settings_core.js keeps
// what runs at startup (loadSettings, the theme).
export const loadModals = panel('this dialog', './modals.js');
// The Resume and Default Prompts dialogs are modules of their own (#3836), so opening one does not load the rest of modals.js.
export const loadResumeDialog = panel('this dialog', './resume_dialog.js');
export const loadDefaultPromptsDialog = panel('this dialog', './default_prompts_dialog.js');

/** Stubs for `names` exported by the module `load` imports. A stub loads the
 *  module and calls through. However the module loads (a stub, or switchNavTab
 *  through its loader), every stub of it on window becomes the real function,
 *  so later calls are direct and synchronous. A failed
 *  load resolves to undefined: the toast already told the user. */
// The first use of a lazy module waits for its request. On a slow link that is a pause with nothing to say it is working: after BUSY_AFTER_MS the control that was clicked is marked busy
// (aria-busy, and .lazy-loading: a progress cursor and a dimmed row, no animation) until the module has loaded or failed. On a fast link it never shows. The control is the click's own
// [data-on-click] element: the stub runs inside the click's dispatch, so window.event is the click (a call from elsewhere has no control to mark and marks none).
export const BUSY_AFTER_MS = 150;
function markBusyWhile(promise) {
    const ev = window.event;
    const host = ev && ev.target && ev.target.closest ? ev.target.closest('[data-on-click]') : null;
    if (!host || host.hasAttribute('aria-busy')) return;
    const timer = setTimeout(() => { host.classList.add('lazy-loading'); host.setAttribute('aria-busy', 'true'); }, BUSY_AFTER_MS);
    const done = () => { clearTimeout(timer); host.classList.remove('lazy-loading'); host.removeAttribute('aria-busy'); };
    promise.then(done, done);
}

export function lazyFns(load, names) {
    const stubs = {};
    for (const name of names) {
        // once the module is loaded a call goes straight to it, synchronously, as when the module was a static import; the first call waits for the load
        stubs[name] = (...args) => {
            if (load.module) return load.module[name](...args);
            const loading = load();
            markBusyWhile(loading);
            return loading.then(m => m[name](...args), () => undefined);
        };
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
