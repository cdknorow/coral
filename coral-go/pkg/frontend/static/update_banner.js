/* A dashboard tab left open across a server update (#3751). The tab keeps running the scripts it loaded
 * while the server runs new ones, so it can call routes the update removed (v1.4.57 dropped GET
 * /pending-tool: 405). The server names its static build in X-Coral-Build on every /api/ answer; the
 * page knows the build it loaded from (/static/b/<build>/). When they differ, a banner says Coral was
 * updated and offers Reload, and a tab with nothing unsent reloads by itself after a few seconds.
 * Text typed and not sent, and file edits not saved, are never reloaded over: the banner stays until
 * they are sent or saved (or the user reloads). Not on the Cloud Hub, which serves its own pages. */
import { state } from './state.js';
import { registerActions } from './actions.js';
import { browserSession } from './safe_storage.js';

 const BUILD_HEADER = 'X-Coral-Build';
const RELOAD_DELAY_MS = 5000;

/** The build id in a static prefix like /static/b/<build>/ or the bundled /static/bb/<build>/ (#4228), or '' (plain /static/, the hub). */
 function buildOf(staticBase) {
    const m = /^\/static\/bb?\/([^/]+)\/$/.exec(staticBase || '');
    return m ? m[1] : '';
}

/** Whether an answer's build header says this page is out of date. Only a header that names one build counts: a value with
 *  a comma is two headers joined (a proxy that appended a second one), and says nothing about this page's build. */
export function isStale(ownBuild, headerValue) {
    return !!ownBuild && !!headerValue && !headerValue.includes(',') && headerValue !== ownBuild;
}

/** Whether a request URL is an answer from another machine relayed by this hub (/api/remote/<server>/...): its build is that
 *  machine's, never the build of the page this hub serves (#3990). */
function isRelayed(url, base = location.href) {
    try { return /^\/api\/remote\//.test(new URL(url, base).pathname); } catch { return false; }
}

/** Whether a request URL is an artifact's bytes (/api/artifacts/<64 hex>): the browser keeps that answer for a year, headers included, so its build header names the build that
 *  sent it long ago, not the server now (#5312). The server leaves the header off; an older server's cached answers still carry one, and none of them says anything about this page. */
export function isArtifactContent(url, base = location.href) {
    try { return /^\/api\/artifacts\/[0-9a-f]{64}\/?$/i.test(new URL(url, base).pathname); } catch { return false; }
}

const unsavedChecks = [];
/** A module with work a reload would lose registers a function that says whether it has some. */
export function registerUnsavedWork(fn) { unsavedChecks.push(fn); }

// Boxes the user typed into. A box that only holds a default (the launch folder, a cron line, a number) is
// not unsent work, so only what a person entered counts: a trusted input event marks the box.
const typed = new WeakSet();
if (typeof document !== 'undefined') document.addEventListener('input', e => { if (e.isTrusted && e.target) typed.add(e.target); }, true);

/** Unsent text the user typed in any box, a draft kept for an agent, or work a module reports. */
 function hasUnsavedWork() {
    for (const el of document.querySelectorAll('textarea, input, [contenteditable=""], [contenteditable="true"]')) {
        if (!typed.has(el) || el.closest('.cm-editor')) continue;
        if (String('value' in el ? el.value : el.textContent || '').trim()) return true;
    }
    if (Object.values(state.sessionInputText || {}).some(t => t && String(t).trim())) return true;
    return unsavedChecks.some(fn => { try { return !!fn(); } catch { return false; } });
}

let _updatedTo = '';
let _timer = 0;

function _banner() { return document.getElementById('update-banner'); }

const DETAIL = {
    soon: 'Reloading in a few seconds.',
    unsaved: 'Your unsent text is kept: reload when you are ready.',
    manual: 'Reload to use the new version.',
};

function _show(kind) {
    const b = _banner();
    const t = document.getElementById('update-banner-text');
    if (!b || !t) return;
    // The status is always in the page and only its words change (a status that appears with its words is often not read); the Reload button comes with them.
    if (t.dataset.kind !== kind) {
        const bold = document.createElement('b'); bold.textContent = 'Coral was updated.';
        const d = document.createElement('span'); d.id = 'update-banner-detail'; d.textContent = DETAIL[kind];
        t.replaceChildren(bold, ' ', d);
        t.dataset.kind = kind;
    }
    const r = b.querySelector('button');
    if (r && r.hidden) r.hidden = false;
}

function _delay() { return Number(window.__coralUpdateReloadDelay) > 0 ? Number(window.__coralUpdateReloadDelay) : RELOAD_DELAY_MS; }

function _tick() {
    _timer = 0;
    if (!_updatedTo) return;
    if (hasUnsavedWork()) { _show('unsaved'); _timer = setTimeout(_tick, _delay()); return; }
    // Once per build: a reload that still sees a different build (a half-finished update) must not loop.
    try {
        if (browserSession.get('coral-update-reloaded') === _updatedTo) { _show('manual'); return; }
        browserSession.set('coral-update-reloaded', _updatedTo);
    } catch { /* storage blocked: reload anyway, once per page load */ }
    reloadNow();
}

 function reloadNow() { window.location.reload(); }

function _updated(build) {
    if (_updatedTo === build) return;
    _updatedTo = build;
    _show(hasUnsavedWork() ? 'unsaved' : 'soon');
    clearTimeout(_timer);
    _timer = setTimeout(_tick, _delay());
}

function _mode() {
    try { return JSON.parse(document.getElementById('coral-boot').textContent).mode || 'local'; } catch { return 'local'; }
}

function _install() {
    const own = buildOf(window.__coralStatic);
    if (!own || _mode() === 'cloud' || typeof window.fetch !== 'function') return;
    const inner = window.fetch;
    window.fetch = function () {
        const p = inner.apply(this, arguments);
        p.then(resp => {
            try {
                if (!resp || !resp.headers || (resp.url && new URL(resp.url, location.href).origin !== location.origin) || isRelayed(resp.url) || isArtifactContent(resp.url)) return;
                const v = resp.headers.get(BUILD_HEADER);
                if (isStale(own, v)) _updated(v);
            } catch { /* never let the watch break a request */ }
        }, () => {});
        return p;
    };
}

registerActions({ 'update-reload': () => reloadNow() });
if (typeof document !== 'undefined' && typeof window !== 'undefined') _install();
