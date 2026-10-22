// The Changes panel in a folder that is not a git repository (#6072): the files answer says is_git false, and the panel says "Not a git folder" with no comparison picker and no count, instead of
// "0 files changed vs HEAD~1". A git folder (an answer without is_git, as before) keeps its count and its picker. The route is stubbed.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

const SID = '0b2f6c1e-3d4a-4b5c-8d6e-000000006072';
const NAME = 'claude-' + SID;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const MOD = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__answer = 'git';
            const inner = window.fetch;
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            window.fetch = (url, opts = {}) => {
                const u = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, '');
                if (u.includes('/files')) return window.__answer === 'plain' ? json({ files: [], diff_mode: 'branch_point', is_git: false }) : json({ files: [{ filepath: 'src/a.go', status: 'M', additions: 2, deletions: 1 }], diff_mode: 'branch_point' });
                return inner(url, opts);
            };
        })();` });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady();
        const panel = () => ev(`(() => { const t = document.getElementById('changed-files-title'), l = document.getElementById('changed-files-list'); return { title: t && t.textContent.replace(/\\s+/g, ' ').trim(), pickers: document.querySelectorAll('#agentic-panel-files select[data-on-change="diff-mode"]').length, list: l && l.textContent.replace(/\\s+/g, ' ').trim(), count: (document.getElementById('files-bar-count') || {}).textContent }; })()`);
        const load = () => ev(`(async () => { const m = await import(${MOD} + 'changed_files.js'); await m.loadChangedFiles(${JSON.stringify(NAME)}, ${JSON.stringify(SID)}); return true; })()`);

        await load();
        let p = await panel();
        check('a git folder keeps its count and its comparison picker', /^1 file changed/.test(p.title) && p.pickers === 1 && p.count === '1', JSON.stringify(p));

        await ev(`window.__answer = 'plain'; true`); await load();
        p = await panel();
        check('a folder that is not a git repository says "Not a git folder"', p.title === 'Not a git folder', JSON.stringify(p));
        check('...with no comparison picker, no "0 files changed vs HEAD~1" and no count', p.pickers === 0 && !/changed/.test(p.title) && !p.count, JSON.stringify(p));
        check('...and a line saying what makes changes show', /git repository/.test(p.list), JSON.stringify(p));

        await ev(`window.__answer = 'git'; true`); await load();
        p = await panel();
        check('back in a git folder the count and the picker are back', /^1 file changed/.test(p.title) && p.pickers === 1, JSON.stringify(p));

        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        if (failed) process.exitCode = 1;
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
