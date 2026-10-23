// Codex's hooks question (#6409, Security's #6408 rank 4, from the phone first-run walk #6383): a new Codex agent that Coral starts stops on "Hooks need review" until the person answers, and the first message was
// written into a refusal (409 codex_hooks_review_pending). Now: the agent opens on that question (its card is the chat's content, not "No messages yet"), the box under it says "Answer Codex's question about Coral's hooks first"
// while it is open and has its own words back when it is answered (or for another agent), a message sent meanwhile is still refused by the server and the 409's words show as they did, nothing is answered for the person,
// and the New Agent step says once, when Codex is chosen on this computer, that Codex will ask. At 1280 and 390 px. A stand-in terminal lives in the page's fetch (the server side is in human_send_test.go).
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const HOOKS = { question: 'Hooks need review: 3 hooks are new or changed', options: [
    { n: 1, label: 'Review hooks', selected: true, action: 'select' }, { n: 2, label: 'Trust all and continue', action: 'select' }, { n: 3, label: 'Continue without trusting', action: 'select' }] };

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);

            console.log(`${tag}: the agent's chat while Codex asks about the hooks`);
            // the stand-in terminal: the chat reply of the Codex agent carries the hooks question until it is answered; a send is refused as the server refuses it
            await ev(`(() => {
                const inner = window.__innerFetch || (window.__innerFetch = window.fetch);
                window.__term = { open: true, sends: 0 };
                const json = (status, body) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }));
                window.fetch = (url, opts = {}) => {
                    const u = String(url);
                    if (/\\/api\\/sessions\\/live\\/codex-q[^/?]*\\/chat\\?/.test(u)) return inner(url, opts).then(async r => { const d = await r.clone().json().catch(() => null); if (!d) return r;
                        if (window.__term.open) d.prompt_options = ${JSON.stringify(HOOKS)}; d.pending = null; return new Response(JSON.stringify(d), { status: r.status, headers: { 'content-type': 'application/json' } }); });
                    if (/\\/api\\/sessions\\/live\\/[^/?]+\\/send(?:\\?|$)/.test(u) && (opts.method || 'GET') === 'POST') { window.__term.sends++;
                        return json(409, { code: 'codex_hooks_review_pending', error: "Answer Codex's question about Coral's hooks above first: your message was not sent. Trust them only if you want them to run outside Codex's sandbox.", not_sent: true }); }
                    return inner(url, opts);
                };
                return true; })()`);
            await ev(`window.__m.add('codex-q', 'A', 0, { sleeping: false, agent_type: 'codex' }); true`);
            await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
            await ev(`window.selectLiveSession('codex-q', 'codex', 'codex-q-id', 'local'); true`);
            await waitFor(`!!document.querySelector('#live-history-messages .chat-needs-input, #live-history-messages .cni')`, 'the hooks question card', 8000);
            const open1 = await ev(`(() => ({ card: document.querySelector('#live-history-messages .chat-needs-input')?.textContent.replace(/\\s+/g, ' ').trim().slice(0, 200) || '', empty: !!document.querySelector('#live-history-messages .chat-empty'), ph: document.getElementById('command-input').placeholder }))()`);
            check(`${tag}: the agent opens on the question: its card shows the three choices, not "No messages yet"`, /Review hooks/.test(open1.card) && /Trust all and continue/.test(open1.card) && /Continue without trusting/.test(open1.card) && !open1.empty, JSON.stringify(open1));
            await waitFor(`/Answer Codex/.test(document.getElementById('command-input').placeholder)`, 'the box saying so', 6000);
            const want = mobile ? "Answer Codex's hooks question first" : "Answer Codex's question about Coral's hooks first";
            check(`${tag}: the box says to answer it first`, (await ev(`document.getElementById('command-input').placeholder`)) === want, await ev(`document.getElementById('command-input').placeholder`));

            // a message typed anyway is refused by the server and the refusal shows (the 409 stays); nothing was answered for the person
            await ev(`(() => { const i = document.getElementById('command-input'); i.value = 'hello'; i.dispatchEvent(new Event('input', { bubbles: true })); i.focus(); return true; })()`);
            await ev(`document.getElementById('command-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); true`);
            await waitFor(`(document.getElementById('composer-error') && !document.getElementById('composer-error').hidden)`, 'the refusal', 6000);
            const sent = await ev(`({ sends: window.__term.sends, err: document.querySelector('#composer-error .composer-error-text').textContent, draft: document.getElementById('command-input').value })`);
            check(`${tag}: a message sent meanwhile is refused (409) and its words show; the draft stays; no answer was sent`, sent.sends === 1 && /Answer Codex's question about Coral's hooks/.test(sent.err) && sent.draft === 'hello', JSON.stringify(sent));

            // answered: the question goes from the screen, and the box has its own words back
            await ev(`window.__term.open = false; true`);
            await waitFor(`!/Answer Codex/.test(document.getElementById('command-input').placeholder)`, 'the box back to its words', 8000);
            const back = await ev(`document.getElementById('command-input').placeholder`);
            check(`${tag}: answered, the box has its own words back`, /^Message codex-q/.test(back), back);

            // the New Agent step: Codex chosen on this computer says it once; another agent says nothing
            console.log(`${tag}: New Agent`);
            await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && !!document.getElementById('launch-agent-next')`, 'the folder step', 8000);
            await ev(`(() => { const i = document.getElementById('launch-dir'); if (!i.value) i.value = '/tmp'; document.getElementById('launch-agent-next').click(); return true; })()`);
            await waitFor(`!document.getElementById('launch-agent-pick').hidden && getComputedStyle(document.getElementById('launch-agent-pick')).display !== 'none'`, 'the agent choice', 8000);
            const note = () => ev(`(() => { const n = document.getElementById('launch-codex-note'); return { shown: !!n && !n.hidden && n.getClientRects().length > 0, text: n ? n.textContent : '', count: document.querySelectorAll('#launch-modal .launch-codex-note').length }; })()`);
            await ev(`document.querySelector('#launch-agent-pick input[value="claude"]').click(); true`);
            check(`${tag}: Claude chosen: no note`, !(await note()).shown);
            await ev(`document.querySelector('#launch-agent-pick input[value="codex"]').click(); true`);
            const n = await note();
            check(`${tag}: Codex chosen: one line says Codex will ask about Coral's hooks on this computer, and where to answer`, n.shown && n.count === 1 && /Codex will ask you to trust Coral's hooks on this computer/.test(n.text) && /chat/.test(n.text), JSON.stringify(n));
            await ev(`document.querySelector('#launch-agent-pick input[value="pi"]').click(); true`);
            check(`${tag}: another agent chosen: the note goes`, !(await note()).shown);
            await ev(`window.hideLaunchModal(); true`);
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} Codex hooks question checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
