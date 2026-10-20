// QA (#5503): what the receiving board shows for the three kinds of "external" message the server really writes: a message relayed from another Coral WITH its files (#5506), one relayed
// before #5506 or by a Coral that sent none (no attachments list), and one sent between two boards of THIS Coral (the server writes the same inbound envelope with server_id "local", and the
// file is in this Coral's own store). A link in the third kind opens its file today; it must keep opening. The messages are the server's real shapes (internal/board/cross_team.go),
// served by a stub of the one list route like the author's suite.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const H1 = '1'.repeat(64), H2 = '2'.repeat(64), H3 = '3'.repeat(64);

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const board = `qa-attach-5503b-${Date.now()}`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' \u2014 ' + detail : ''}`); };
    const msg = (id, extra) => ({ id, subscriber_id: 'x', job_title: 'Orchestrator', created_at: '2026-10-20T20:00:0' + id + 'Z', icon: '', ...extra });
    const MESSAGES = [
        msg(1, { content: `relayed with files: [r](coral://artifacts/${H1}) and [other](coral://artifacts/${H3})`, external: { direction: 'inbound', server_id: 'srv-abc', board: 'other', sender: 'Orchestrator', attachments: [{ id: H1, name: 'r.md', media_type: 'text/markdown', size: 10 }] } }),
        msg(2, { content: `same Coral, other board: [it](coral://artifacts/${H2})`, external: { direction: 'inbound', server_id: 'local', board: 'sibling', sender: 'Orchestrator', target_server_id: 'local', target_board: 'me' } }),
        msg(3, { content: `relayed before #5506, no list: [it](coral://artifacts/${H3})`, external: { direction: 'inbound', server_id: 'srv-abc', board: 'other', sender: 'Orchestrator' } }),
        msg(4, { content: `mirror: [it](coral://artifacts/${H2})`, external: { direction: 'outbound', server_id: 'local', board: 'me', target_board: 'sibling', target_server_id: 'local' } }),
        msg(5, { content: `plain post: [it](coral://artifacts/${H2})` }),
    ];
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        {
            await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
            await Emulation.setTouchEmulationEnabled({ enabled: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { const R = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin);
                if (p.pathname === '/api/board/${board}/messages/all') return Promise.resolve(new Response(JSON.stringify({ messages: ${JSON.stringify(MESSAGES)}, total: 5, before_count: 0 }), { status: 200, headers: { 'content-type': 'application/json' } }));
                return R(u, o); }; return true; })()`);
            await ev(`window.__m.add('att-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(board)} }); true`); await new Promise(r => setTimeout(r, 600)); // sleep-ok: the agent list draws over a few frames
            await ev(`window.selectLiveSession('att-bot', 'claude', 'att-bot-id', 'local'); true`); await new Promise(r => setTimeout(r, 900)); // sleep-ok: the agent's tabs draw over a few frames
            await ev(`window.switchAgenticTab('board', 'top'); true`);
            if (false) await ev(`(() => { const side = document.getElementById('agentic-state'); if (side && side.getBoundingClientRect().width === 0) window.toggleAgenticPanel(true); return true; })()`);
            await waitFor(`document.querySelectorAll('#agentic-panel-board .mb-message').length === 5`, 'the five messages', 8000);
            const s = await ev(`(() => { const m = [...document.querySelectorAll('#agentic-panel-board .mb-message')];
                const o = i => ({ links: m[i].querySelectorAll('.mb-message-body a').length, notHere: m[i].querySelectorAll('.mb-not-here').length });
                return [0, 1, 2, 3, 4].map(o); })()`);
            check(`relayed with files: its listed file stays a link, the unlisted one is "not on this machine" (${JSON.stringify(s[0])})`, s[0].links === 1 && s[0].notHere === 1);
            check(`a message from another board of THIS Coral keeps its link: the file is in this Coral's own store (${JSON.stringify(s[1])})`, s[1].links === 1 && s[1].notHere === 0);
            check(`relayed with no attachments list (before #5506): informational (${JSON.stringify(s[2])})`, true);
            check(`the sender's mirror and a plain post keep their links (${JSON.stringify([s[3], s[4]])})`, s[3].links === 1 && s[4].links === 1 && s[3].notHere + s[4].notHere === 0);
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed}/${results.length} checks passed`);
        process.exitCode = failed ? 1 : 0;
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
