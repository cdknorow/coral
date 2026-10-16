// CSP stage 2, slice 3 (#3645): the History view, the activity filter dropdown (live and history)
// and the changed/starred files on delegated data-on-* actions.
//  - History: each tab button opens its tab and is the one marked active (switchHistoryTab now
//    reads data-tab, not the onclick text); an unknown tab does nothing; the summary editor opens
//    and cancels; the tag dropdown shows and hides; a branch chip's copy button copies its branch.
//  - The filter dropdown opens from either view's button; an item toggles its filter (and an
//    unknown key changes nothing); Toggle All; an item click doesn't close the dropdown.
//  - Files, with paths holding &, <, ' and a space: copy, star, preview, edit and the row get the
//    file's exact path (the old onclick chain passed it HTML-escaped); a diff row opens in diff
//    mode; the diff mode select sends only a mode it offers; nothing inline is left.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

const SID = '0b2f6c1e-3d4a-4b5c-8d6e-000000013645';
const NAME = 'claude-' + SID;
const PATHS = ["src/a&b <x>.go", "docs/it's here.md"];

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__req = []; window.__copied = [];
            const inner = window.fetch;
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            window.fetch = (url, opts = {}) => {
                const u = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, ''), m = (opts.method || 'GET').toUpperCase();
                if (u.includes('/files')) return json({ files: ${JSON.stringify(PATHS)}.map((p, i) => ({ filepath: p, status: i ? 'A' : 'M', additions: 1, deletions: 0 })) });
                if (u.includes('/file-content')) { window.__req.push(m + ' ' + u); return json({ content: 'x', exists: true }); }
                if (u.includes('/api/settings/git-diff-mode') || (u === '/api/settings' && m === 'PUT' && String(opts.body || '').includes('git_diff_mode'))) { window.__req.push(m + ' ' + u + ' ' + (opts.body || '')); return json({ ok: true }); }
                return inner(url, opts);
            };
            Object.defineProperty(navigator, 'clipboard', { value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } }, configurable: true });
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // ── History view ───────────────────────────────────────────────────
        await ev(`document.getElementById('history-session-view').style.display = ''; true`);
        const tabs = await ev(`(() => {
            const out = {};
            for (const b of document.querySelectorAll('#history-session-view [data-on-click="history-tab"]')) {
                if (b.style.display === 'none') b.style.display = '';
                b.click();
                const active = [...document.querySelectorAll('.history-tab-btn.active')].map(x => x.dataset.tab);
                out[b.dataset.tab] = active;
            }
            return out;
        })()`);
        const tabNames = Object.keys(tabs);
        check('every history tab button opens its tab and is the only one marked active',
            tabNames.length === 7 && tabNames.every(t => JSON.stringify(tabs[t]) === JSON.stringify([t])), JSON.stringify(tabs));
        const bogus = await ev(`(() => { const b = document.createElement('button'); b.className = 'history-tab-btn'; b.setAttribute('data-on-click', 'history-tab'); b.dataset.tab = 'nope';
            document.body.appendChild(b); const before = [...document.querySelectorAll('.history-tab-btn.active')].map(x => x.dataset.tab); b.click(); b.remove();
            return { before, after: [...document.querySelectorAll('.history-tab-btn.active')].map(x => x.dataset.tab) }; })()`);
        check('an unknown tab does nothing', JSON.stringify(bogus.before) === JSON.stringify(bogus.after), JSON.stringify(bogus));

        await ev(`document.querySelector('[data-on-click="history-tab"][data-tab="notes"]').click(); true`);
        await ev(`document.querySelector('[data-on-click="notes-edit"]').click(); true`);
        const editing = await ev(`getComputedStyle(document.getElementById('notes-edit-area')).display !== 'none'`);
        await ev(`document.querySelector('[data-on-click="notes-cancel"]').click(); true`);
        const cancelled = await ev(`getComputedStyle(document.getElementById('notes-edit-area')).display === 'none'`);
        check('the summary editor opens on Edit and closes on Cancel', editing && cancelled, `${editing}/${cancelled}`);

        await ev(`document.querySelector('[data-on-click="tag-dropdown-show"]').click(); true`);
        const shown = await until(`getComputedStyle(document.getElementById('tag-dropdown')).display !== 'none'`, 'the tag dropdown', 3000).then(() => ev(`getComputedStyle(document.getElementById('tag-dropdown')).display !== 'none'`));
        await ev(`document.querySelector('[data-on-click="tag-dropdown-hide"]').click(); true`);
        const hidden = await ev(`getComputedStyle(document.getElementById('tag-dropdown')).display === 'none'`);
        check('the tag dropdown shows on + Tag and hides on Close', shown && hidden, `${shown}/${hidden}`);

        await ev(`(() => { const chip = document.querySelector('#history-session-branch'); chip.querySelector('.branch-text').textContent = 'feature/x&y'; chip.querySelector('[data-on-click="copy-branch"]').click(); return true; })()`);
        await until(`window.__copied.includes('feature/x&y')`, 'the copied branch', 3000);
        check('a branch chip\'s copy button copies its branch', (await ev(`window.__copied`)).includes('feature/x&y'), JSON.stringify(await ev(`window.__copied`)));

        // ── The filter dropdown (history, then live) ───────────────────────
        const filt = await ev(`(async () => {
            const h = await import(${MOD} + 'history_tabs.js');
            const box = document.getElementById('history-event-filters');
            if (box && !box.querySelector('[data-on-click="filter-dropdown"]')) box.innerHTML = '<button class="filter-dropdown-btn" data-on-click="filter-dropdown" data-mode="history">Filter</button>';
            box.querySelector('[data-on-click="filter-dropdown"]').click();
            const dd = document.getElementById('filter-dropdown');
            if (!dd) return { opened: false };
            const item = dd.querySelector('[data-on-click="filter-toggle"]');
            const key = item.dataset.key, before = window.historyFilterHiddenRef.has(key);
            item.click();
            const toggled = window.historyFilterHiddenRef.has(key) !== before;
            const stillOpen = !!document.getElementById('filter-dropdown');
            const size = window.historyFilterHiddenRef.size;
            const bad = document.createElement('div'); bad.setAttribute('data-on-click', 'filter-toggle'); bad.dataset.mode = 'history'; bad.dataset.key = 'not-a-filter';
            document.body.appendChild(bad); bad.click(); bad.remove();
            const unknownIgnored = window.historyFilterHiddenRef.size === size && !window.historyFilterHiddenRef.has('not-a-filter');
            const n = document.querySelectorAll('#filter-dropdown [data-on-click="filter-toggle"]').length;
            document.getElementById('filter-dropdown').querySelector('[data-on-click="filter-toggle-all"]').click();
            const all = window.historyFilterHiddenRef.size === 0 || window.historyFilterHiddenRef.size === n;
            // Close it as a person does (an outside click), so its close listener goes too.
            await new Promise(r => setTimeout(r, 0));
            document.body.click();
            return { opened: true, mode: item.dataset.mode, toggled, stillOpen, unknownIgnored, all };
        })()`);
        check('history: the Filter button opens the dropdown, an item toggles its filter and keeps it open',
            filt.opened && filt.mode === 'history' && filt.toggled && filt.stillOpen, JSON.stringify(filt));
        check('history: an unknown filter key changes nothing; Toggle All toggles them all', filt.unknownIgnored && filt.all, JSON.stringify(filt));
        const live = await ev(`(async () => {
            const a = await import(${MOD} + 'agentic_state.js');
            const btn = document.createElement('div'); btn.id = 'event-filters'; btn.innerHTML = '<button class="filter-dropdown-btn" data-on-click="filter-dropdown" data-mode="live">Filter</button>';
            const had = document.getElementById('event-filters');
            const host = had || btn;
            if (!had) document.body.appendChild(btn); else if (!had.querySelector('[data-on-click="filter-dropdown"]')) had.innerHTML = btn.innerHTML;
            host.querySelector('[data-on-click="filter-dropdown"]').click();
            const dd = document.getElementById('filter-dropdown');
            const out = { opened: !!dd, mode: dd?.querySelector('[data-on-click="filter-toggle"]')?.dataset.mode };
            dd?.remove();
            if (!had) btn.remove();
            return out;
        })()`);
        check('live: the Filter button opens the same dropdown in live mode', live.opened && live.mode === 'live', JSON.stringify(live));

        // ── Changed and starred files ──────────────────────────────────────
        await ev(`(async () => {
            const { state } = await import(${MOD} + 'state.js');
            state.currentSession = { type: 'live', name: ${JSON.stringify(NAME)}, agent_type: 'claude', session_id: ${JSON.stringify(SID)} };
            const cf = await import(${MOD} + 'changed_files.js');
            await cf.loadChangedFiles(${JSON.stringify(NAME)}, ${JSON.stringify(SID)});
            return true;
        })()`);
        await until(`document.querySelectorAll('#changed-files-list [data-on-click="file-open"]').length === 2`, 'the changed files');
        const rowFor = p => `[...document.querySelectorAll('#changed-files-list [data-on-click="file-open"]')].find(r => r.dataset.path === ${JSON.stringify(p)})`;
        check('each row carries its file\'s exact path', await ev(`!!${rowFor(PATHS[0])} && !!${rowFor(PATHS[1])}`));

        await ev(`${rowFor(PATHS[0])}.querySelector('[data-on-click="file-copy"]').click(); true`);
        await until(`window.__copied.includes(${JSON.stringify(PATHS[0])})`, 'the copied path', 3000);
        check('Copy path copies the exact path (& and < included)', (await ev(`window.__copied`)).includes(PATHS[0]), JSON.stringify(await ev(`window.__copied`)));

        const tabOpen = p => ev(`[...document.querySelectorAll('#preview-tabs .preview-tab-name')].some(n => n.getAttribute('title') === ${JSON.stringify(p)})`);
        await ev(`${rowFor(PATHS[1])}.click(); true`);
        await until(`[...document.querySelectorAll('#preview-tabs .preview-tab-name')].some(n => n.getAttribute('title') === ${JSON.stringify(PATHS[1])})`, 'the preview tab', 5000);
        check('clicking a row previews that exact file (its quote included)', await tabOpen(PATHS[1]));
        const diffActive = await ev(`!!document.querySelector('.inline-preview-mode-btn.active[data-mode="diff"]')`);
        check('...in diff mode', diffActive);
        await ev(`${rowFor(PATHS[0])}.querySelector('[data-on-click="file-edit"]').click(); true`);
        await until(`[...document.querySelectorAll('#preview-tabs .preview-tab-name')].some(n => n.getAttribute('title') === ${JSON.stringify(PATHS[0])})`, 'the edit tab', 5000);
        check('Edit opens that exact file', await tabOpen(PATHS[0]));

        const before = await ev(`document.querySelectorAll('#preview-tabs .preview-tab').length`);
        await ev(`${rowFor(PATHS[0])}.querySelector('[data-on-click="file-star"]').click(); true`);
        // Stored as { files, at } since #3681 (a list untouched for 30 days ages out).
        const starred = await ev(`(v => Array.isArray(v) ? v : (v && v.files) || [])(JSON.parse(localStorage.getItem('coral-starred-files-${SID}') || '[]'))`);
        check('Star stars the exact path, and its click doesn\'t also open the row', JSON.stringify(starred) === JSON.stringify([PATHS[0]])
            && (await ev(`document.querySelectorAll('#preview-tabs .preview-tab').length`)) === before, JSON.stringify(starred));
        await until(`!!document.querySelector('.file-starred[data-on-click="file-open"]')`, 'the starred list', 3000);
        await ev(`window.__copied.length = 0; document.querySelector('.file-starred [data-on-click="file-copy"]').click(); true`);
        await until(`window.__copied.length > 0`, 'the starred copy', 3000);
        check('the starred list carries the same exact path', JSON.stringify(await ev(`window.__copied`)) === JSON.stringify([PATHS[0]]), JSON.stringify(await ev(`window.__copied`)));

        await ev(`window.__req.length = 0; true`);
        const sel = await ev(`(() => { const s = document.querySelector('select[data-on-change="diff-mode"]'); if (!s) return null;
            const cur = s.value, opts = [...s.options].map(o => o.value);
            const evil = document.createElement('option'); evil.value = 'evil'; s.appendChild(evil);
            s.value = 'evil'; s.dispatchEvent(new Event('change', { bubbles: true }));
            s.value = opts.find(v => v !== cur); s.dispatchEvent(new Event('change', { bubbles: true }));
            return { cur, opts, picked: s.value }; })()`);
        await until(`window.__req.some(r => r.includes('git_diff_mode') || r.includes('git-diff-mode'))`, 'the diff mode save', 3000);
        const modeReqs = (await ev(`window.__req`)).filter(r => r.includes('git_diff_mode') || r.includes('git-diff-mode'));
        check('the diff mode select saves a mode it offers, and never one it doesn\'t', sel && modeReqs.length === 1 && !modeReqs[0].includes('evil') && modeReqs[0].includes(sel.picked), JSON.stringify({ sel, modeReqs }));

        const inline = await ev(`[...document.querySelectorAll('#history-session-view *, #changed-files-list *, #changed-files-title *, .starred-files *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + '.' + el.className + ' ' + a.name))`);
        check('no inline handler left in the History view or the file lists', inline.length === 0, JSON.stringify(inline.slice(0, 5)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
