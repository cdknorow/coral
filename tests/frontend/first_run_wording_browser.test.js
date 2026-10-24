// First-run wording (#6200, from Codex Test's first-run walk): (a) Codex's hooks question held back a first send: the error under the composer is one short sentence, and answering the question clears it and says the
// message is still there; (b) the Claude sign-in line is only where Claude's background work is (checked in signin_line_browser); (c) a chat in History is titled by what it says, not the start of a reply with a path in
// it, and its header is that title with the id behind "Details", not "Session: <uuid>". Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const SID = '22531c16-39ef-94af-f501-2d792b5410b1';
const HISTORY = `(() => {
    const inner = window.fetch;
    const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
    window.fetch = (url, opts = {}) => {
        const u = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, ''), path = u.split('?')[0], m = (opts.method || 'GET').toUpperCase();
        if (m === 'GET') {
            if (path === '/api/sessions/history') return json({ sessions: [
                { session_id: '${SID}', summary_title: '', summary: "I'm working in \`/private/tmp/ct6194-audit/browser-home/my-first-project\`.", source_type: 'codex', agent_name: 'my-first-project' },
                { session_id: 'titled-chat', summary_title: 'Release notes chat', first_user_message: 'typed after the title', summary: 'x', source_type: 'claude' },
                { session_id: 'typed-chat', summary_title: '', first_user_message: 'Fix the login redirect in /work/acme/web-app/src', summary: 'I changed the router.', source_type: 'claude', agent_name: 'web-agent', repo_name: 'coral', branch: 'feat/jobs-page', last_timestamp: '${new Date().toISOString()}', duration_sec: 1260 }] });
            if (path === '/api/sessions/history/${SID}') return json({ agent_type: 'codex', messages: [{ type: 'user', content: 'Hello' }, { type: 'assistant', text: 'Hi there.' }], total: 2, start: 0 });
            if (new RegExp('^/api/sessions/history/${SID}/(notes|git|tags|events|tasks|agent-notes|files)$').test(path) || path === '/api/tags') return json(path.endsWith('/notes') ? { notes_md: '' } : []);
        }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: HISTORY });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        const S = `(window.__coralStatic || '/static/')`;

        // (a) the hooks question
        const a = await ev(`(async () => { const g = await import(${S} + 'terminal_gone.js'); const text = "Answer Codex's question about Coral's hooks above first: your message was not sent. Trust them only if you want them to run outside Codex's sandbox.";
            g.reportSendError({ name: 'x', display_name: 'x' }, text, 'codex_hooks_review_pending');
            const box = document.getElementById('composer-error'); const shown = !box.hidden, words = box.querySelector('.composer-error-text').textContent.trim().split(/\\s+/).length;
            g.noteHooksAnswered(); const gone = box.hidden === true; await new Promise(r => setTimeout(r, 100));
            const toast = [...document.querySelectorAll('.toast')].map(t => t.textContent).join(' ');
            // an ordinary error is not cleared by an answer
            g.reportSendError({ name: 'x' }, 'The agent is busy.', 'agent_busy'); g.noteHooksAnswered(); const kept = !document.getElementById('composer-error').hidden;
            return { shown, words, gone, toast, kept }; })()`);
        check('the hooks question error is one short sentence pair (28 words at most) under the composer', a.shown && a.words <= 28, JSON.stringify(a));
        check('answering the question clears it and says the message is still in the box', a.gone && /still in the box: send it again/.test(a.toast), JSON.stringify(a));
        check('an answer does not clear an unrelated send error', a.kept);

        // (c) History
        await ev(`window.switchNavTab('history'); true`);
        await waitFor(`document.querySelectorAll('#history-sessions-list [data-on-click="history-open"]').length === 3`, 'the chats', 10000);
        const labels = await ev(`[...document.querySelectorAll('#history-sessions-list .session-label')].map(e => e.textContent)`);
        check('a chat with no title leads with its first reply, the path cut to its last folder, no backticks', labels[0] === "I'm working in my-first-project." && !/\/private|`/.test(labels[0]), JSON.stringify(labels));
        check('a chat with a title keeps it, whatever was typed first', labels[1] === 'Release notes chat', JSON.stringify(labels));
        check('a chat with no title leads with what the person typed first (path cut to its last folder), not the reply (#6251)', labels[2] === 'Fix the login redirect in src' && labels[0] === "I'm working in my-first-project.", JSON.stringify(labels));
        // a row is its title, then one line of facts (#6580): who, the branch (not the usual one), when in words and how long it ran; the provider only where it is not Claude
        const rows = await ev(`[...document.querySelectorAll('#history-sessions-list li[data-session-id]')].map(li => ({ first: li.firstElementChild.className, badge: [...li.querySelectorAll('.badge')].map(b => b.textContent), facts: (li.querySelector('.chat-row-facts') || {}).textContent || '', time: !!li.querySelector('time[datetime][title]'), lines: li.querySelectorAll('.session-row-top, .sidebar-source-type').length }))`);
        check('a Claude chat shows no provider (no badge, no "claude" line) and the chat from Codex shows a CODEX badge', rows[2].badge.length === 0 && !/claude/i.test(rows[2].facts) && rows[0].badge.join() === 'codex' && rows.every(r => r.lines === 0), JSON.stringify(rows));
        check('the row leads with its title and its facts read "web-agent · feat/jobs-page · today 14:32, ran 21 min": no repo name, the time of day in text (#6961)', rows[2].first.includes('chat-row-title') && /^web-agent\s*·\s*feat\/jobs-page\s*·\s*(today|yesterday) \d\d:\d\d, ran 21 min$/.test(rows[2].facts.replace(/\s+/g, ' ').trim()) && rows[2].time && !/coral|\d\d\/\d\d\/\d\d/.test(rows[2].facts), JSON.stringify(rows[2]));
        const words = await ev(`import(${S} + 'history_list.js').then(m => { const now = new Date(2026, 9, 23, 15, 0); const w = (d, s) => m.chatWhen(d.toISOString(), s, now);
            return [w(new Date(2026, 9, 23, 9, 12), 1260), w(new Date(2026, 9, 22, 23, 50), 7500), w(new Date(2026, 9, 21, 8, 0), 20), w(new Date(2025, 11, 31, 8, 0), 3600)].map(x => x.day + '|' + x.ran).concat([String(m.chatWhen('', 5, now))]); })`);
        check('a chat is dated in words (today 09:12, yesterday 23:50, Oct 21, Dec 31, 2025: the time of day for today and yesterday, #6961) with "ran N min" for how long it went on', JSON.stringify(words) === JSON.stringify(['today 09:12|ran 21 min', 'yesterday 23:50|ran 2 h 5 min', 'Oct 21|ran under a minute', 'Dec 31, 2025|ran 1 h', 'null']), JSON.stringify(words));
        await ev(`document.querySelector('#history-sessions-list [data-session-id="${SID}"]').click(); true`);
        await waitFor(`document.getElementById('history-messages').textContent.includes('Hi there')`, 'the chat', 10000);
        const h = await ev(`(() => { const t = document.getElementById('history-session-title').textContent, id = document.getElementById('history-session-id'), b = document.getElementById('history-session-details'); return { t, idShown: id.getClientRects().length > 0, expanded: b.getAttribute('aria-expanded'), label: b.textContent }; })()`);
        check('the header is the title, not "Session: <id>", and the id is not on screen', h.t === "I'm working in my-first-project." && !h.t.includes('Session:') && !h.t.includes('22531c16') && !h.idShown && h.expanded === 'false', JSON.stringify(h));
        await ev(`document.getElementById('history-session-details').click(); true`);
        const o = await ev(`(() => { const id = document.getElementById('history-session-id'), b = document.getElementById('history-session-details'); return { text: id.textContent, shown: id.getClientRects().length > 0, expanded: b.getAttribute('aria-expanded'), label: b.textContent }; })()`);
        check('Details shows the id (and says Hide details); the button has aria-expanded', o.shown && o.text === SID && o.expanded === 'true' && o.label === 'Hide details', JSON.stringify(o));
        // the chat's header has one loud button at most (Resume), and Generate Summary is a button of the Summary tab (#6580)
        const hd = await ev(`(() => { const g = document.getElementById('history-generate-summary-btn'); const prim = [...document.querySelectorAll('#history-session-view .session-header .btn-primary')].filter(b => getComputedStyle(b).display !== 'none' && b.getClientRects().length > 0 && !b.closest('#tag-dropdown'));
            return { inNotes: !!g && !!g.closest('#history-tab-notes'), inHeader: !!g && !!g.closest('.session-header'), primaries: prim.map(b => b.id || b.textContent.trim()) }; })()`);
        check('Generate Summary is in the Summary tab, not the header, and the header has no more than one primary button', hd.inNotes && !hd.inHeader && hd.primaries.length <= 1, JSON.stringify(hd));
    } finally { /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`first_run_wording: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
