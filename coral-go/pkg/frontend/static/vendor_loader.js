/* On-demand vendor libraries (#3083, #3117). The editor (CodeMirror, ~1 MB),
 * the terminal (xterm.js and its addons, ~430 KB) and the code highlighter
 * (highlight.js, ~120 KB) are no longer loaded by the page <head>: most visits never open a file editor, and chat is the default view,
 * so they load the first time they are needed. Each URL loads once and every
 * caller shares the same promise. URLs are relative to this module, so they
 * follow a versioned static prefix (/static/b/<build>/). Same-origin script
 * and link tags only: nothing here needs a CSP change.
 *
 * A URL comes only from import.meta.url and the fixed list below, never from
 * page data, settings, the hash or the query. A failed load is forgotten, so a
 * retry fetches again instead of reusing the rejected promise. */

const VENDOR_FILES = new Set([
    'codemirror/codemirror-bundle.js',
    'xterm/xterm.min.css',
    'xterm/xterm.min.js',
    'xterm/addon-fit.min.js',
    'xterm/addon-web-links.min.js',
    'xterm/addon-unicode-graphemes.min.js',
    'xterm/addon-canvas.min.js',
    'hljs/highlight.min.js',
    'hljs/github-dark.min.css',
]);

const _loads = new Map();
const _loaded = new Set(); // hrefs whose load finished

function vendorUrl(rel) {
    if (!VENDOR_FILES.has(rel)) throw new Error(`Not a vendor file: ${rel}`);
    return new URL(`./vendor/${rel}`, import.meta.url).href;
}

/** Forget a load that finished but left no usable library, so the next call fetches again. */
function forget(rel) {
    const href = vendorUrl(rel);
    _loads.delete(href);
    _loaded.delete(href);
    document.head.querySelectorAll('script[src], link[href]').forEach(el => {
        if (el.src === href || el.href === href) el.remove();
    });
}

/** Load a vendor script once (in order with other loads started earlier). */
export function loadScriptOnce(rel) {
    const href = vendorUrl(rel);
    if (_loads.has(href)) return _loads.get(href);
    const p = new Promise((resolve, reject) => {
        const s = document.createElement('script');
        s.src = href;
        s.async = false; // keep execution order (an addon after its library)
        s.onload = () => resolve();
        s.onerror = () => { _loads.delete(href); s.remove(); reject(new Error(`Could not load ${rel}`)); };
        document.head.appendChild(s);
    });
    _loads.set(href, p);
    return p;
}

/** Load a vendor stylesheet once. */
 function loadStyleOnce(rel) {
    const href = vendorUrl(rel);
    if (_loads.has(href)) return _loads.get(href);
    const p = new Promise((resolve, reject) => {
        const l = document.createElement('link');
        l.rel = 'stylesheet';
        l.href = href;
        l.onload = () => { _loaded.add(href); resolve(); };
        l.onerror = () => { _loads.delete(href); l.remove(); reject(new Error(`Could not load ${rel}`)); };
        document.head.appendChild(l);
    });
    _loads.set(href, p);
    return p;
}

/** CodeMirror 6 (window.CoralCM). Resolves to the module. */
export async function loadCodeMirror() {
    if (window.CoralCM) return window.CoralCM;
    await loadScriptOnce('codemirror/codemirror-bundle.js');
    if (!window.CoralCM) { forget('codemirror/codemirror-bundle.js'); throw new Error('CodeMirror did not initialise'); }
    return window.CoralCM;
}

/** highlight.js and its theme (window.hljs). Resolves to hljs. */
export async function loadHighlight() {
    await Promise.all([loadStyleOnce('hljs/github-dark.min.css'), loadScriptOnce('hljs/highlight.min.js')]);
    if (!window.hljs) { forget('hljs/highlight.min.js'); throw new Error('highlight.js did not initialise'); }
    return window.hljs;
}

// Each addon file and the global it defines (xterm_renderer.js uses all four).
const XTERM_ADDONS = {
    'xterm/addon-fit.min.js': 'FitAddon',
    'xterm/addon-web-links.min.js': 'WebLinksAddon',
    'xterm/addon-unicode-graphemes.min.js': 'UnicodeGraphemesAddon',
    'xterm/addon-canvas.min.js': 'CanvasAddon',
};

/** xterm.js with its addons and stylesheet (window.Terminal and the addon globals). */
export async function loadXterm() {
    // Every file every time: the ones already loaded return their cached promise,
    // so a retry after one file failed fetches only that file. The stylesheet and
    // the scripts load in parallel; the addons run after xterm (async=false keeps order).
    await Promise.all([loadStyleOnce('xterm/xterm.min.css'), loadScriptOnce('xterm/xterm.min.js'), ...Object.keys(XTERM_ADDONS).map(loadScriptOnce)]);
    if (typeof window.Terminal === 'undefined') { forget('xterm/xterm.min.js'); throw new Error('xterm.js did not initialise'); }
    const missing = Object.entries(XTERM_ADDONS).filter(([, name]) => typeof window[name] === 'undefined');
    if (missing.length) {
        missing.forEach(([rel]) => forget(rel));
        throw new Error(`xterm addon did not initialise: ${missing.map(([, name]) => name).join(', ')}`);
    }
    return window.Terminal;
}

/** True once xterm.js, its addons and its stylesheet are loaded (a synchronous check for code that cannot wait). */
export function xtermReady() {
    return _loaded.has(vendorUrl('xterm/xterm.min.css')) && typeof window.Terminal !== 'undefined' && Object.values(XTERM_ADDONS).every(name => typeof window[name] !== 'undefined');
}

/** Show "<what> could not load" with a Retry button in `host`. Returns the
 *  notice element; it removes itself before calling `onRetry`. */
export function showVendorLoadError(host, what, onRetry) {
    host.querySelector(':scope > .vendor-load-notice')?.remove();
    const box = document.createElement('div');
    box.className = 'vendor-load-notice vendor-load-error';
    box.setAttribute('role', 'alert');
    const msg = document.createElement('span');
    msg.textContent = `${what} could not load. Check the connection to Coral, then try again.`;
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-small';
    btn.textContent = 'Retry';
    btn.addEventListener('click', () => { box.remove(); onRetry(); });
    box.append(msg, btn);
    host.appendChild(box);
    return box;
}

/** Show "Loading <what>…" in `host` until the returned element is removed. */
export function showVendorLoading(host, what) {
    host.querySelector(':scope > .vendor-load-notice')?.remove();
    const box = document.createElement('div');
    box.className = 'vendor-load-notice';
    box.setAttribute('role', 'status');
    box.textContent = `Loading ${what}…`;
    host.appendChild(box);
    return box;
}
