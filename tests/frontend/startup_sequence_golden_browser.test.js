// The order the dashboard starts in (#4913, from #4893 proposal B): app.js's DOMContentLoaded handler is 375 lines that run the platform set-up, the fetch wrapper, the cloud and local-store set-up, about
// thirty feature initialisers, the filter and keyboard wiring, the first route, and say "initialized". Splitting it into named steps must change no order. This installs a recorder before any page script runs
// (fetches, WebSocket opens, listeners added to the window, the document and elements, timers, the DOMContentLoaded handlers' own start and end) and loads the real dashboard against the isolated server.
// Compared exactly with fixtures/startup_sequence_golden.json, section by section: what ran before the handler, the handler's own span in order (it is one synchronous run after its awaits), the window hooks the page defined, and the lazy modules it loaded. Per-phase sections name the phase that
// changed. CORAL_UPDATE_GOLDEN=1 rewrites the golden.
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { compareGolden } = require('./helpers/builder_golden');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'startup_sequence_golden.json');

const RECORDER = `(() => {
    const log = window.__boot = [], known = new Set(Object.getOwnPropertyNames(window));
    window.__bootKnown = known;
    let tell = () => {}; // set below: tells the document changes made so far before an event is logged, so the two are in the order they happened
    const push = s => { tell(); log.push(s); };
    const desc = t => t === window ? 'window' : t === document ? 'document' : t instanceof Element
        ? (t.id ? '#' + t.id : t.tagName.toLowerCase() + (typeof t.className === 'string' && t.className.trim() ? '.' + t.className.trim().split(/\\s+/).join('.') : '')) : ((t && t.constructor && t.constructor.name) || 'other');
    const pathOf = u => { try { const x = new URL(String(u && u.url ? u.url : u), location.href); return x.pathname; } catch { return String(u).slice(0, 80); } };
    // the page's own changes to the document, in the order they were made (the handler's DOM-only steps leave no request or listener to tell them by)
    const pending = []; const obs = new MutationObserver(recs => { pending.push(...recs); }); const ready = () => { observing = true; obs.observe(document, { subtree: true, childList: true, attributes: true }); };
    const mdesc = n => n.nodeType === 1 ? desc(n) : n.nodeType === 3 ? '#text' : '#' + n.nodeName;
    tell = () => { if (observing) flush('now'); }; let observing = false;
    const flush = why => { const recs = pending.splice(0).concat(obs.takeRecords()); for (const r of recs) {
        if (r.type === 'attributes') { const v = r.attributeName === 'style' ? '' : String(r.target.getAttribute(r.attributeName) || '').slice(0, 40); log.push('mutation ' + why + ' attr ' + desc(r.target) + ' ' + r.attributeName + '=' + v); }
        else log.push('mutation ' + why + ' children of ' + desc(r.target) + ' +' + [...r.addedNodes].map(mdesc).join(',') + ' -' + [...r.removedNodes].map(mdesc).join(',')); } };
    const add = EventTarget.prototype.addEventListener; let dcl = 0;
    EventTarget.prototype.addEventListener = function (type, fn, o) {
        if (type === 'DOMContentLoaded' && this === document && typeof fn === 'function') {
            const n = ++dcl; const orig = fn;
            fn = function (...a) { push('DOMContentLoaded handler ' + n + ' starts'); ready(); let r; try { r = orig.apply(this, a); } catch (e) { push('DOMContentLoaded handler ' + n + ' threw'); throw e; }
                flush('head'); Promise.resolve(r).then(() => { flush('tail'); push('DOMContentLoaded handler ' + n + ' ends'); }, () => { flush('tail'); push('DOMContentLoaded handler ' + n + ' failed'); }); return r; };
        }
        push('listen ' + desc(this) + ':' + type);
        return add.call(this, type, fn, o);
    };
    let cur = null; const rf = window.fetch; const mine = function (u, o) { push('fetch ' + (((o && o.method) || (u && u.method) || 'GET').toUpperCase()) + ' ' + pathOf(u)); return rf.apply(this, arguments); };
    // (a wrapper is told by the shape of its code, not its names: a build renames them)
    cur = mine; Object.defineProperty(window, 'fetch', { configurable: true, get: () => cur, set: v => { push('window.fetch is replaced by ' + String(v).replace(/[A-Za-z_$][\\w$]*/g, w => /^(async|function|await|let|const|var|return|if|else|new|this|throw|try|catch|true|false|null|undefined|typeof)$/.test(w) ? w : '_').replace(/\\s+/g, '').slice(0, 90)); cur = v; } });
    const RW = window.WebSocket; window.WebSocket = function (u, p) { push('websocket ' + pathOf(u)); return p === undefined ? new RW(u) : new RW(u, p); }; Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }); window.WebSocket.prototype = RW.prototype;
    const si = window.setInterval; window.setInterval = function (f, ms, ...r) { push('setInterval ' + ms); return si.call(this, f, ms, ...r); };
    const st = window.setTimeout; window.setTimeout = function (f, ms, ...r) { push('setTimeout ' + (ms || 0)); return st.call(this, f, ms, ...r); };
    window.addEventListener('coral:ready', () => push('coral:ready'));
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: RECORDER });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`window.__coralReady && window.__coralReady.parts && Object.values(window.__coralReady.parts).every(Boolean)`, 'the page to be ready', 20000);
        const got = await ev(`(() => {
            const log = window.__boot, text = a => a.join('\\n');
            const i1 = log.findIndex(l => l.startsWith('DOMContentLoaded handler') && l.endsWith('starts')), appStart = log.findIndex((l, i) => i >= i1 && /^fetch|^listen document:|^listen window:/.test(l));
            return { log, hooks: Object.getOwnPropertyNames(window).filter(k => !window.__bootKnown.has(k) && k !== '__boot' && k !== '__bootKnown').sort(),
                modules: performance.getEntriesByType('resource').map(e => new URL(e.name).pathname).filter(p => /\\.js$/.test(p) && p.includes('/static/')).map(p => p.replace(/^.*\\/static\\//, '')).sort() };
        })()`);
        // sections: the log is cut at the handlers' markers
        const log = got.log;
        const handlerStarts = log.map((l, i) => /^DOMContentLoaded handler \d+ starts$/.test(l) ? i : -1).filter(i => i >= 0);
        const handlerEnds = log.map((l, i) => /^DOMContentLoaded handler \d+ (ends|failed|threw)$/.test(l) ? i : -1).filter(i => i >= 0);
        const cases = {};
        cases['1 before the DOMContentLoaded handlers (modules evaluated)'] = log.slice(0, handlerStarts[0]).join('\n');
        // each handler's own span, in order (events from outside it that land during its awaits are not told apart: the span is what the page logged between its start and its end)
        handlerStarts.forEach((s, k) => { const e = handlerEnds.find(i => i > s); cases['2 handler ' + (k + 1) + ' (start to end, in order)'] = log.slice(s, e === undefined ? log.length : e + 1).join('\n'); });
        // (what follows the handler, the answers' own requests, comes in the network's order and varies from run to run: it is not compared)
        cases['3 window hooks the page defined (sorted)'] = got.hooks.join('\n');
        cases['4 modules requested (sorted, the bundle folder is named by its content hash)'] = got.modules.map(m => m.replace(/^bb\/[0-9a-f]+\//, 'bb/<hash>/')).join('\n');
        const problems = compareGolden(GOLDEN, cases, 'The order the dashboard starts in (before the DOMContentLoaded handlers, the handler span in order, the window hooks, the modules requested), for startup_sequence_golden_browser.test.js. CORAL_UPDATE_GOLDEN=1 rewrites it.');
        if (problems.length) { console.error(problems.join('\n')); process.exitCode = 1; }
        else console.log(`PASS startup sequence golden: ${Object.keys(cases).length} sections identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
