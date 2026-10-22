// A jsdom-backed global environment that lets the REAL UI modules (render.js, tags.js, ...)
// be imported and run in node, with the real app-shell HTML as the document.
import { JSDOM } from 'jsdom';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
export const FRONTEND = path.resolve(here, '../../../coral-go/pkg/frontend');
export const STATIC = path.join(FRONTEND, 'static') + '/';

/** The app shell as served: index.html with its template includes inlined and actions blanked. */
export function appShellHtml() {
    const read = p => fs.readFileSync(path.join(FRONTEND, 'templates', p), 'utf8');
    let html = read('index.html');
    html = html.replace(/\{\{\s*template\s+"([^"]+)"\s*\.\s*\}\}/g, (_, name) => read(name));
    html = html.replace(/\{\{[^}]*\}\}/g, '');
    return html;
}

let installed = null;

/** Install DOM globals once; returns the JSDOM instance. */
export function installDom({ mode = 'cloud' } = {}) {
    if (installed) return installed;
    const html = appShellHtml()
        .replace(/<script\b[^>]*src=[^>]*><\/script>/g, '') // external scripts are imported by the test
        .replace(/<link\b[^>]*>/g, '');
    const dom = new JSDOM(html, { url: 'https://hub.example/', pretendToBeVisual: true, runScripts: 'outside-only' });
    const w = dom.window;
    w.matchMedia = w.matchMedia || (() => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }));
    w.requestAnimationFrame = w.requestAnimationFrame || (cb => setTimeout(cb, 0));
    w.ResizeObserver = w.ResizeObserver || class { observe() {} unobserve() {} disconnect() {} };
    w.IntersectionObserver = w.IntersectionObserver || class { observe() {} unobserve() {} disconnect() {} };
    w.scrollTo = () => {};
    w.Element.prototype.scrollIntoView = function () {};
    w.HTMLElement.prototype.scrollTo = function () {};
    if (!w.navigator.clipboard) Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async () => {} }, configurable: true });
    const boot = w.document.getElementById('coral-boot');
    if (boot) boot.textContent = JSON.stringify({ mode });
    for (const k of Object.getOwnPropertyNames(w)) {
        if (k in globalThis && !['window', 'document', 'navigator', 'location', 'localStorage', 'sessionStorage'].includes(k)) continue;
        try { globalThis[k] = w[k]; } catch { try { Object.defineProperty(globalThis, k, { value: w[k], configurable: true, writable: true }); } catch { /* skip */ } }
    }
    for (const k of ['window', 'document', 'navigator', 'location', 'localStorage', 'sessionStorage']) {
        Object.defineProperty(globalThis, k, { value: w[k], configurable: true, writable: true });
    }
    globalThis.window = w;
    globalThis.fetch = async (url) => ({ ok: true, status: 200, json: async () => ({}), text: async () => '', headers: new Map(), url: String(url) });
    w.fetch = globalThis.fetch;
    globalThis.WebSocket = w.WebSocket = class { constructor() { this.readyState = 0; } send() {} close() {} addEventListener() {} };
    globalThis.marked = w.marked = { parse: s => String(s), setOptions() {}, use() {} };
    installed = dom;
    return dom;
}

export async function importUi(name) {
    return import(pathToFileURL(STATIC + name + '.js').href);
}
