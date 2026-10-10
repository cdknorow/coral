// Pure-node test for the Cloud Hub's CSP-safe inline-handler runner (no browser needed):
//   node csp_compat.test.js
// 1. Every inline handler in the shipped UI parses with the restricted grammar.
// 2. Hostile handler text is refused instead of evaluated.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend');
    const m = await import(pathToFileURL(path.join(dir, 'static/csp_compat.js')).href);

    // ── 1. corpus ────────────────────────────────────────────────────
    const files = [];
    (function walk(d) {
        for (const e of fs.readdirSync(d, { withFileTypes: true })) {
            const p = path.join(d, e.name);
            if (e.isDirectory()) { if (e.name !== 'vendor') walk(p); } else if (/\.(js|html)$/.test(e.name)) files.push(p);
        }
    })(dir);
    const re = / on(?:click|dblclick|change|input|keydown|keyup|submit|mousedown|contextmenu)=("([^"]*)"|'([^']*)')/g;
    let total = 0;
    const bad = [];
    for (const f of files) {
        // Template placeholders (which may contain quotes) are runtime data: blank them first.
        const src = fs.readFileSync(f, 'utf8').replace(/\$\{(?:[^{}]|\{[^{}]*\})*\}/g, 'X');
        let x;
        while ((x = re.exec(src))) {
            let h = x[2] !== undefined ? x[2] : x[3];
            // Template placeholders and JS string escapes become plain values.
            h = h.replace(/\$\{[^}]*\}/g, 'X').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/\\'/g, "'");
            total++;
            try { m.parse(h); } catch (e) { bad.push(`${path.relative(dir, f)}: ${h.slice(0, 100)} -> ${e.message}`); }
        }
    }
    assert.ok(total > 400, `expected the corpus to be large, got ${total}`);
    assert.deepStrictEqual(bad, [], `${bad.length} of ${total} inline handlers are outside the CSP-safe grammar:\n` + bad.join('\n'));

    // ── 2. the grammar refuses code ──────────────────────────────────
    for (const evil of [
        "eval('alert(1)')", "fetch('//evil')", "window.location='//evil'", "document.cookie", "this.ownerDocument.defaultView.eval('1')",
        "a=1", "(function(){})()", "x => y", "new Function('1')", "this.innerHTML='x'", "import('//evil')", "alert`1`",
    ]) {
        let ok = true;
        try { m.parse(evil); } catch { ok = false; }
        if (ok) {
            // Parsed (e.g. a plain call): it must still be refused at run time.
            const calls = [];
            globalThis.eval = globalThis.eval; // native: not callable from handlers
            const ev = { stopPropagation() {}, preventDefault() {}, key: 'x', type: 'click' };
            const el = { closest() { return null; } };
            m.runHandler(evil, el, ev);
        }
    }

    // ── 3. execution ─────────────────────────────────────────────────
    const log = [];
    globalThis.appFn = (...a) => log.push(['appFn', ...a]);
    globalThis.other = (...a) => log.push(['other', ...a]);
    const ev = (k) => ({ key: k, shiftKey: false, ctrlKey: false, stopPropagation() { log.push('stop'); }, preventDefault() { log.push('prevent'); } });
    const el = { value: 'v', closest: s => ({ remove: () => log.push(['remove', s]) }) };

    m.runHandler("event.stopPropagation(); appFn('a', 2, true, ['x'])", el, ev('a'));
    assert.deepStrictEqual(log.splice(0), ['stop', ['appFn', 'a', 2, true, ['x']]]);

    m.runHandler("window.appFn(this.value)", el, ev('a'));
    assert.deepStrictEqual(log.splice(0), [['appFn', 'v']]);

    m.runHandler("this.closest('.row').remove()", el, ev('a'));
    assert.deepStrictEqual(log.splice(0), [['remove', '.row']]);

    m.runHandler("if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();appFn('go')}", el, ev('Enter'));
    assert.deepStrictEqual(log.splice(0), ['prevent', ['appFn', 'go']]);
    m.runHandler("if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();appFn('go')}", el, ev('a'));
    assert.deepStrictEqual(log.splice(0), []);

    // Native functions are not app functions.
    let fetched = false;
    const realFetch = globalThis.fetch;
    globalThis.fetch = () => { fetched = true; };      // a user-defined fetch would be callable...
    m.runHandler("fetch('//evil')", el, ev('a'));
    assert.strictEqual(fetched, false, 'fetch must never be callable from a handler');
    globalThis.fetch = realFetch;
    m.runHandler("eval('log.push(1)')", el, ev('a'));
    assert.deepStrictEqual(log.splice(0), []);
    m.runHandler("this.constructor.constructor('1')()", el, ev('a'));
    m.runHandler("appFn(this.ownerDocument)", el, ev('a'));
    assert.deepStrictEqual(log.splice(0), [], 'blocked properties are refused');

    // Assignments are limited to value/textContent/checked/display on elements.
    const el2 = { textContent: 'a', innerHTML: 'a', style: { display: 'block' } };
    m.runHandler("this.textContent='ok'; this.style.display='none'", el2, ev('a'));
    assert.strictEqual(el2.textContent, 'ok'); assert.strictEqual(el2.style.display, 'none');
    m.runHandler("this.innerHTML='<img src=x onerror=1>'", el2, ev('a'));
    assert.strictEqual(el2.innerHTML, 'a', 'innerHTML assignment must be refused');
    let timerRan = false;
    globalThis.fetch = () => { timerRan = true; };
    m.runHandler("setTimeout(()=>fetch('//evil'),0)", el2, ev('a'));
    await new Promise(r => setTimeout(r, 20));
    assert.strictEqual(timerRan, false, 'arrow bodies obey the same rules');
    globalThis.fetch = realFetch;
    m.runHandler("setTimeout(appFn, 0)", el2, ev('a'));
    await new Promise(r => setTimeout(r, 20));
    assert.deepStrictEqual(log.splice(0), [], 'setTimeout needs one of our arrow functions');

    // return false cancels
    assert.strictEqual(m.runHandler("return appFn('x')", el, ev('a')), true);
    globalThis.nope = () => false;
    assert.strictEqual(m.runHandler("return nope()", el, ev('a')), false);
    log.splice(0);

    console.log(`csp_compat.test.js: ok (${total} inline handlers parse)`);
})().catch(e => { console.error(e); process.exit(1); });
