// The search, filter and sort bar of Team Artifacts (#4744): buildBar (team_artifact_bar.js) is what the other list panels are to share, so it is
// pinned in every state first and moved onto the shared list bar after: the same markup, the same calls, the same focus. This builds the bar in a page, puts it through
// each state (empty, counts, filtered with chips, a cut list, the open Filters menu, typing, every control, Clear all, Escape) and records its markup, the filters it handed to
// onChange and where the focus is. Compared with fixtures/list_bar_golden.json (sha256 per state, with a readable line). CORAL_UPDATE_GOLDEN=1 rewrites it.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const GOLDEN = path.join(__dirname, 'fixtures', 'list_bar_golden.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { const base = window.__coralStatic || '/static/'; const m = await import(base + 'team_artifact_bar.js'); const host = document.createElement('div'); host.id = 'bar-host'; document.body.append(host);
            window.__calls = []; window.__bar = m.buildBar(f => { window.__calls.push(JSON.stringify(f)); }); host.append(window.__bar.node); return true; })()`);
        const snap = async name => ev(`(() => { const a = document.activeElement; return { html: window.__bar.node.outerHTML, calls: window.__calls.splice(0).join(' ; '), focus: a && window.__bar.node.contains(a) && a.getClientRects().length ? (a.id || a.className) : (a === document.body || (a && window.__bar.node.contains(a)) ? 'body' : 'elsewhere'), // a focused control that was just hidden counts as body: the browser lets go of it a moment later
            values: [...window.__bar.node.querySelectorAll('input, select')].map(e => e.type === 'checkbox' ? e.checked : e.value).join('|'), menuHidden: window.__bar.node.querySelector('.list-bar-menu').hidden }; })()`);
        const key = (k, vk) => client.Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: vk }).then(() => client.Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: vk }));
        const type = async (sel, text) => { await ev(`document.querySelector(${JSON.stringify(sel)}).focus(); true`); await client.Input.insertText({ text }); };
        const wait = ms => ev(`new Promise(r => setTimeout(r, ${ms}))`); // sleep-ok: the search waits 300 ms after the last key; there is no event for it
        const out = {};
        const rec = async (name, fn) => { await fn(); out[name] = await snap(); };
        await rec('built, nothing set', async () => {});
        await rec('set, counts of 12', () => ev(`window.__bar.set({ q: '', kind: '', origin: '', by: '', kept: false, from: '', to: '', sort: 'newest' }, { matched: 12, all: 12 }); true`));
        await rec('set, one artifact', () => ev(`window.__bar.set({ sort: 'newest' }, { matched: 1, all: 1 }); true`));
        await rec('set, a cut list', () => ev(`window.__bar.set({ sort: 'newest' }, { matched: 500, all: 500, truncated: true }); true`));
        await rec('set, filtered: a search, a type, a date range', () => ev(`window.__bar.set({ q: 'plan', kind: 'pdf', from: '2026-10-01', to: '2026-10-09', sort: 'name' }, { matched: 3, all: 40 }); true`));
        await rec('set, filtered and cut', () => ev(`window.__bar.set({ q: 'plan', kind: 'pdf', sort: 'size' }, { matched: 3, all: 500, truncated: true }); true`));
        await rec('set, one filter only (no Clear all chip)', () => ev(`window.__bar.set({ by: 'Dev', sort: 'oldest' }, { matched: 2, all: 9 }); true`));
        await rec('names offered', () => ev(`window.__bar.names(['Dev', 'QA', 'Operator']); window.__bar.set({ sort: 'newest' }, { matched: 12, all: 12 }); true`));
        await rec('Filters opened by click', () => ev(`window.__bar.node.querySelector('.list-bar-filter-btn').click(); true`));
        await rec('Escape closes it and returns the focus', async () => { await key('Escape', 27); });
        await rec('Filters opened again, a type chosen', async () => { await ev(`window.__bar.node.querySelector('.list-bar-filter-btn').click(); true`); await ev(`(() => { const s = document.getElementById('ta-kind'); s.value = 'image'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); });
        await rec('an origin, kept only and two dates', async () => { await ev(`(() => { for (const [id, v] of [['ta-origin', 'received']]) { const s = document.getElementById(id); s.value = v; s.dispatchEvent(new Event('change', { bubbles: true })); }
            const k = document.getElementById('ta-kept'); k.checked = true; k.dispatchEvent(new Event('change', { bubbles: true }));
            for (const [id, v] of [['ta-from', '2026-10-05'], ['ta-to', '2026-10-02']]) { const e = document.getElementById(id); e.value = v; e.dispatchEvent(new Event('change', { bubbles: true })); } return true; })()`); });
        await rec('the sort changed', () => ev(`(() => { const s = document.getElementById('ta-sort'); s.value = 'size'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`));
        await rec('typing in the search (not yet sent)', async () => { await type('#ta-search', 'abc'); });
        await rec('after the pause the search is sent', async () => { await wait(450); }); // sleep-ok: the search is sent 300 ms after the last key; there is no event for it
        await rec('Enter sends at once', async () => { await type('#ta-search', 'd'); await key('Enter', 13); });
        await rec('the search cleared by its x', () => ev(`document.querySelector('.list-bar-clear-q').click(); true`));
        await rec('a chip removed', async () => { await ev(`window.__bar.set({ q: 'x', kind: 'pdf', sort: 'newest' }, { matched: 1, all: 5 }); document.querySelector('.list-bar-chip').click(); true`); });
        await rec('Clear all by its chip', async () => { await ev(`window.__bar.set({ q: 'x', kind: 'pdf', by: 'Dev', sort: 'name' }, { matched: 1, all: 5 }); document.querySelector('.list-bar-chip-all').click(); true`); });
        await rec('Clear all in the menu keeps the sort', async () => { await ev(`window.__bar.set({ kind: 'pdf', sort: 'size' }, { matched: 1, all: 5 }); window.__bar.node.querySelector('.list-bar-filter-btn').click(); document.querySelector('.list-bar-clear-all').click(); true`); });
        await rec('a click outside closes the open menu', async () => { await ev(`window.__bar.node.querySelector('.list-bar-filter-btn').click(); document.body.click(); true`); });
        await rec('a bad value is cleaned', () => ev(`window.__bar.set({ q: 'a\\u0000b', kind: 'weird', from: '2026-13-45', sort: 'nope' }, { matched: 1, all: 1 }); true`));
        await rec('a field being typed in is left as typed', async () => { await ev(`document.getElementById('ta-search').focus(); document.getElementById('ta-search').value = 'half'; window.__bar.set({ q: '', sort: 'newest' }, null); true`); });
        await rec('focusSearch', () => ev(`document.activeElement.blur(); window.__bar.focusSearch(); true`));

        const hashes = {}, lines = {};
        for (const [name, s] of Object.entries(out)) {
            hashes[name] = sha(JSON.stringify(s));
            const text = s.html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
            lines[name] = `${text} | values ${s.values} | calls ${s.calls} | focus ${s.focus} | menu ${s.menuHidden ? 'closed' : 'open'}`;
        }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what the Team Artifacts search bar is in each state, for list_bar_golden_browser.test.js: sha256 of its markup, the filters it sent and its focus, and a readable line. CORAL_UPDATE_GOLDEN=1 rewrites it.', hashes, lines }, null, 1) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' states');
        } else {
            const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
            assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same states');
            for (const k of Object.keys(want.hashes)) if (hashes[k] !== want.hashes[k]) assert.fail(`${k}: changed.\n  now:  ${lines[k]}\n  was:  ${want.lines[k]}`);
        }
        console.log(`PASS list bar: ${Object.keys(hashes).length} states identical`);
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
