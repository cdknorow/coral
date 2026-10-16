// Resume keeps the session's agent type (#3093):  run via run_cdp_suites.mjs
// A history session opened by link (not in the loaded history list) whose
// /resume-info lookup fails is resumed as the type its transcript reported
// (codex here), never the old 'claude' default. With no type from any source
// the Resume dialog refuses with an error and nothing is launched. The history
// list entry still wins when there is one.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        // The page loads its own history list at startup; if that lands after a
        // fixture entry below it replaces it. Flag when it has been applied (as
        // history_resume_ui does, #3062).
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const real = window.fetch.bind(window);
            window.fetch = (url, opts) => {
                const p = real(url, opts);
                if (new URL(String(url), location.origin).pathname === '/api/sessions/history') {
                    p.then(r => r.clone().json()).catch(() => null).finally(() => setTimeout(() => { window.__historyListLoaded = true; }, 50));
                }
                return p;
            };
        })();` });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        for (let i = 0; i < 100 && !(await ev('window.__historyListLoaded === true')); i++) await new Promise(r => setTimeout(r, 50));
        assert.equal(await ev('window.__historyListLoaded === true'), true, "the page's own history list loaded before the fixtures go in");
        await ev(`new Promise(r => { const w = () => typeof window.showResumeModal === 'function' ? r() : setTimeout(w, 50); w(); })`);
        await ev(`(async () => {
            const S = window.__coralStatic || '/static/';
            window.T = { state: (await import(S + 'state.js')).state, selectHistorySession: (await import(S + 'sessions.js')).selectHistorySession };
            // historyType: what GET /api/sessions/history/<id> reports (null: the load fails).
            // /resume-info always fails; /api/sessions/launch is recorded, not run.
            window.launches = []; window.historyType = {};
            const orig = window.fetch;
            window.fetch = async (url, opts) => {
                const u = new URL(String(url), location.origin);
                const m = u.pathname.match(/^\\/api\\/sessions\\/history\\/([^/]+)(\\/resume-info)?$/);
                if (m && m[1] in historyType) {
                    if (m[2] || historyType[m[1]] === null) return new Response('{"error":"boom"}', { status: 500 });
                    return new Response(JSON.stringify({ session_id: m[1], agent_type: historyType[m[1]], messages: [{ type: 'user', content: 'hello' }, { type: 'assistant', content: 'hi' }], total: 2, start: 0 }));
                }
                if (u.pathname === '/api/sessions/launch') { launches.push(JSON.parse(opts.body)); return new Response(JSON.stringify({ error: 'recorded by test' })); }
                return orig(url, opts);
            };
        })()`);
        // Open a session, then press Resume (and Resume in the dialog when it opens).
        const resume = id => ev(`(async () => {
            launches.length = 0;
            document.getElementById('resume-modal').style.display = 'none';
            await T.selectHistorySession(${JSON.stringify(id)});
            await showResumeModal();
            const modal = document.getElementById('resume-modal');
            const open = modal.style.display === 'flex';
            if (open) { document.getElementById('resume-dir').value = '/tmp/x'; await resumeLaunchNew(); }
            const toasts = Array.from(document.querySelectorAll('.toast, [class*=toast]')).map(t => t.textContent).join(' | ');
            return { open, type: modal.dataset.agentType, launched: launches.map(p => p.agent_type), button: document.getElementById('btn-resume-session').style.display !== 'none', toasts };
        })()`);

        // 1. Not in the list, /resume-info fails: the transcript's type (codex) is used.
        await ev(`historyType['link-codex'] = 'codex'; T.state.historySessionsList = T.state.historySessionsList.filter(s => s.session_id !== 'link-codex')`);
        const codex = await resume('link-codex');
        assert.ok(codex.button, 'Resume is offered for the linked codex session');
        assert.ok(codex.open, 'the Resume dialog opens');
        assert.equal(codex.type, 'codex', 'the dialog carries the transcript type');
        assert.deepEqual(codex.launched, ['codex'], 'it resumes as codex, not claude');
        console.log('PASS not in list + resume-info failing: resumes as codex');

        // 2. No type from anywhere: an error, no dialog, nothing launched.
        await ev(`historyType['link-unknown'] = null`);
        const unknown = await resume('link-unknown');
        assert.equal(unknown.open, false, 'the dialog does not open without a known type');
        assert.deepEqual(unknown.launched, [], 'nothing is launched');
        assert.match(unknown.toasts, /agent type is unknown/, 'an error says why');
        console.log('PASS no known type: error, nothing launched');

        // 3. A list entry still decides the type when /resume-info fails.
        await ev(`historyType['listed'] = 'claude'; T.state.historySessionsList = [...T.state.historySessionsList, { session_id: 'listed', source_type: 'codex' }]`);
        const listed = await resume('listed');
        assert.deepEqual(listed.launched, ['codex'], 'the list entry type is used');
        console.log('PASS list entry: its source_type is used');
    } finally { await client.close(); }
})().catch(e => {
    console.error('FAIL ' + e.message);
    process.exitCode = 1;
});
