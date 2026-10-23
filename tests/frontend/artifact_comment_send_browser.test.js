// Send to agent on an artifact comment (#6357, #6456): the owner, on this computer, picks one open thread and the agents on its board to ask (1 to 10), and Coral posts one board message to each. The picker opens inside the
// comment's card in the panel's own style: the agents of the board (a terminal is not one), an agent that is asleep or not running shown as such and not pickable, one already asked saying so, the button counting what is
// picked, the server's answer per agent (asked, already asked, asleep ...) and its refusals in words; the agent's reply is a reply in the thread, labelled "agent". Not offered to a reply, a resolved thread, a comment made with no
// board, or through Coral Hub. At 1280 and 390 px. The comments, the board's status and the send route are stood in at the page's fetch (the route and its limits are in internal/server/routes). Drives Chrome over CDP through agent_view_harness.js.
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const HEX = 'e'.repeat(64);
const MD = ['# Migration plan', '', '## Steps', '', '1. Add the new columns behind a flag and backfill in batches of 500 rows.', '2. Switch reads to the new columns.', ''].join('\n');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const STUB = `(() => {
        const real = window.fetch; window.__sends = [];
        window.__cs = [
            { id: 1, parent_id: null, anchor: { quote: 'backfill in batches of 500 rows.', prefix: 'columns behind a flag and', suffix: '2. Switch' }, body: 'Batches of 500 is too small. Try 5,000.', author: { kind: 'owner', issuer: 'local', subject: 'Operator', name: 'You' }, created_at: new Date(Date.now() - 600000).toISOString() },
            { id: 2, parent_id: 1, anchor: null, body: 'Measured: 9 s per 5,000.', author: { kind: 'agent', issuer: 'local', subject: 'qa', name: 'QA Engineer' }, created_at: new Date(Date.now() - 60000).toISOString() },
            { id: 3, parent_id: null, anchor: { quote: 'Switch reads to the new columns.' }, body: 'Fine.', author: { kind: 'owner', issuer: 'local', subject: 'Operator', name: 'You' }, created_at: new Date(Date.now() - 86400000).toISOString(), resolved_at: new Date(Date.now() - 80000000).toISOString(), resolved_by: { subject: 'Operator' } },
        ];
        window.__agents = [
            { name: 'QA', role: 'QA Engineer', agent_type: 'claude', availability: 'available' },
            { name: 'Backend', role: 'Backend Dev', agent_type: 'codex', availability: 'busy' },
            { name: 'Sleepy', role: 'UI Dev', agent_type: 'claude', availability: 'sleeping' },
            { name: 'Gone', role: 'Docs Writer', agent_type: 'claude', availability: 'offline' },
            { name: 'shell', role: 'Shell', agent_type: 'terminal', availability: 'unavailable' },
        ];
        window.__send = { status: 200, body: null }; window.__you = { can_comment: true, can_lock: true };
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => {
            const url = String(u), method = (o && o.method) || 'GET';
            if (/\\/api\\/board\\/[^/]+\\/status/.test(url)) return json({ agents: window.__agents, summary: {}, unassigned_tasks: [], health_report: [], observed_at: new Date().toISOString() });
            if (url.includes('/api/artifacts/${HEX}/comments')) {
                const sub = url.split('/comments')[1].split('?')[0];
                if (method === 'POST' && /^\\/\\d+\\/send$/.test(sub)) {
                    const b = JSON.parse(o.body); window.__sends.push({ id: Number(sub.split('/')[1]), agents: b.agents });
                    if (window.__send.body) return json(window.__send.body, window.__send.status);
                    return json({ ok: true, results: b.agents.map((a, i) => ({ agent: a, state: a === 'Backend Dev' ? 'already_asked' : 'sent', ask_id: i + 1 })) });
                }
                const can = c => ({ reply: c.parent_id == null, edit: false, delete: false, resolve: c.parent_id == null });
                return json({ comments: window.__cs.map(c => ({ ...c, deleted: false, mine: c.author.kind === 'owner', can: can(c) })), locked: false, you: window.__you, limits: {} });
            }
            if (url.includes('/api/artifacts/${HEX}')) return Promise.resolve(new Response(${JSON.stringify(MD)}, { status: 200, headers: { 'Content-Type': 'text/markdown' } }));
            return real(u, o);
        };
        return true; })()`;
    const open = async (board, you) => {
        await ev(STUB);
        if (you) await ev(`window.__you = ${JSON.stringify(you)}; true`);
        await ev(`(async () => { const v = await import(${S} + 'artifact_viewer.js'); v.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'plan.md', ${board ? `board: '${board}'` : ''} }); return true; })()`);
        await waitFor(`!!document.querySelector('#artifact-viewer .artifact-viewer-markdown')`, 'the document', 8000);
        await waitFor(`document.querySelectorAll('.ac-card').length >= 1`, 'the threads', 8000);
        await ev(`(() => { const t = document.querySelector('.ac-toggle'); if (t && t.getAttribute('aria-expanded') !== 'true') t.click(); return true; })()`);
        await sleep(300); // sleep-ok: the panel opens over a frame or two
    };
    const sendBtn = id => `document.querySelector('.ac-card[data-id="${id}"] .ac-ask-open')`;
    const block = () => ev(`(() => { const b = document.querySelector('.ac-ask'); if (!b) return null;
        return { title: b.querySelector('.ac-ask-title').textContent, agents: [...b.querySelectorAll('.ac-ask-agent')].map(li => ({ role: li.querySelector('input').value, text: li.textContent.replace(/\\s+/g, ' ').trim(), disabled: li.querySelector('input').getAttribute('aria-disabled') === 'true' && !li.querySelector('input').disabled, checked: li.querySelector('input').checked, state: li.dataset.state || '' })),
            note: b.querySelector('.ac-ask-note').textContent, go: (b.querySelector('.ac-ask-go') || {}).textContent || '', goDisabled: !!b.querySelector('.ac-ask-go') && b.querySelector('.ac-ask-go').disabled, close: (b.querySelector('.ac-cancel') || {}).textContent, error: b.querySelector('.ac-error').hidden ? '' : b.querySelector('.ac-error').textContent,
            fits: b.getBoundingClientRect().right <= innerWidth + 1 && document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1 }; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`try { localStorage.removeItem('coral-artifact-comments-open'); } catch {} true`);

            console.log(`${tag}: the action`);
            await open('ask-team');
            check(`${tag}: an open thread of the owner shows "Send to agent"; a reply and a resolved thread do not`, (await ev(`!!${sendBtn(1)}`)) && !(await ev(`!!${sendBtn(3)}`)) && !(await ev(`!!document.querySelector('.ac-card[data-id="1"] .ac-reply .ac-ask-open')`)));
            check(`${tag}: the agent's reply is a reply in the thread, labelled agent`, await ev(`!!document.querySelector('.ac-card[data-id="1"] .ac-reply .ac-kind') && document.querySelector('.ac-card[data-id="1"] .ac-reply .ac-kind').textContent === 'agent'`));

            console.log(`${tag}: the picker`);
            await ev(`${sendBtn(1)}.click(); true`);
            await waitFor(`document.querySelectorAll('.ac-ask .ac-ask-agent').length >= 3`, 'the agents', 8000);
            await sleep(200); // sleep-ok: focus moves to the first agent after the list is drawn
            let b = await block();
            check(`${tag}: it names the board and lists its agents (a terminal is not one), asleep and not running shown and not pickable`, /ask-team/.test(b.title) && b.note === 'Each agent you pick gets this comment as a message. They answer in this thread.' && b.agents.map(a => a.role).join() === 'QA Engineer,Backend Dev,UI Dev,Docs Writer'
                && b.agents.find(a => a.role === 'UI Dev').disabled && /Asleep: wake it first/.test(b.agents.find(a => a.role === 'UI Dev').text) && b.agents.find(a => a.role === 'Docs Writer').disabled && /Not running/.test(b.agents.find(a => a.role === 'Docs Writer').text)
                && !b.agents[0].disabled && !b.agents[1].disabled, JSON.stringify(b));
            const reasons = await ev(`[...document.querySelectorAll('.ac-ask input[aria-disabled="true"]')].map(i => ({ role: i.value, focusable: !i.disabled && i.tabIndex >= 0, why: (document.getElementById(i.getAttribute('aria-describedby')) || {}).textContent || '' }))`);
            check(`${tag}: an agent that cannot be asked is aria-disabled and still focusable, and its reason is its description`, reasons.length === 2 && reasons.every(r => r.focusable && r.why) && reasons.find(r => r.role === 'UI Dev').why === 'Asleep: wake it first' && reasons.find(r => r.role === 'Docs Writer').why === 'Not running', JSON.stringify(reasons));
            await ev(`document.querySelector('.ac-ask input[value="UI Dev"]').click(); true`);
            check(`${tag}: a click on one does nothing (not ticked, the button still off)`, !(await ev(`document.querySelector('.ac-ask input[value="UI Dev"]').checked`)) && (await block()).goDisabled);
            check(`${tag}: nothing picked: the button is off; focus is on the first agent that can be picked`, b.goDisabled && b.go === 'Send to agent' && (await ev(`document.activeElement && document.activeElement.value`)) === 'QA Engineer', JSON.stringify([b.go, b.goDisabled]));
            await ev(`document.querySelector('.ac-ask input[value="QA Engineer"]').click(); true`);
            await ev(`document.querySelector('.ac-ask input[value="Backend Dev"]').click(); true`);
            b = await block();
            check(`${tag}: two picked: "Send to 2 agents"; a ticked box keeps its focus`, b.go === 'Send to 2 agents' && !b.goDisabled && (await ev(`document.activeElement && document.activeElement.value`)) === 'Backend Dev', JSON.stringify([b.go, b.agents.map(a => a.checked)]));
            await shot(`send-${tag}-picked`);
            await ev(`document.querySelector('.ac-ask-go').click(); true`);
            await waitFor(`!!document.querySelector('.ac-ask .ac-ask-sent') || !!document.querySelector('.ac-ask-agent[data-state="sent"]')`, 'the answer', 6000);
            b = await block();
            const posted = await ev('window.__sends');
            check(`${tag}: one request with exactly the roles picked, for that comment`, posted.length === 1 && posted[0].id === 1 && posted[0].agents.join() === 'QA Engineer,Backend Dev', JSON.stringify(posted));
            check(`${tag}: the answer is said per agent: Asked, Already asked; the boxes are off and the button is Close`, /Asked/.test(b.agents[0].text) && b.agents[0].state === 'sent' && b.agents[1].state === 'already_asked' && /Already asked/.test(b.agents[1].text) && b.agents.every(a => a.disabled) && b.close === 'Close' && b.go === '', JSON.stringify(b.agents));
            check(`${tag}: it fits`, b.fits);
            const off = await ev(`[...document.querySelectorAll('.ac-ask-agent')].filter(li => li.querySelector('.ac-ask-state')).map(li => { const n = li.querySelector('.ac-ask-name').getBoundingClientRect(), s = li.querySelector('.ac-ask-state').getBoundingClientRect(); return Math.abs((n.top + n.bottom) / 2 - (s.top + s.bottom) / 2); })`);
            check(`${tag}: each status sits on its agent's name line (within 2 px)`, off.length >= 4 && off.every(d => d <= 2), JSON.stringify(off));
            await shot(`send-${tag}-answered`);
            await ev(`document.querySelector('.ac-ask .ac-cancel').click(); true`);
            check(`${tag}: Close puts focus back on Send to agent`, (await ev(`document.activeElement === ${sendBtn(1)}`)));

            // asked agents say so the next time, and cannot be asked again from here
            await ev(`${sendBtn(1)}.click(); true`);
            await waitFor(`document.querySelectorAll('.ac-ask .ac-ask-agent').length >= 3`, 'the agents again', 8000);
            b = await block();
            check(`${tag}: reopened, an agent asked into this thread says "Asked just now" and is off`, /Asked just now/.test(b.agents[0].text) && b.agents[0].disabled && /Already asked/.test(b.agents[1].text) && !b.agents.find(a => a.role === 'UI Dev').checked, JSON.stringify(b.agents[0]));
            await ev(`document.querySelector('.ac-ask .ac-cancel').click(); true`);

            // refusals: nobody asked (409 with results), and an answer with only an error
            await ev(`window.__send = { status: 409, body: { ok: false, results: [{ agent: 'Docs Writer', state: 'unavailable' }] } }; window.__asked = 0; true`);
            await ev(`window.__agents[3].availability = 'available'; ${sendBtn(1)}.click(); true`);
            await waitFor(`document.querySelectorAll('.ac-ask .ac-ask-agent').length >= 3`, 'the agents once more', 8000);
            await ev(`document.querySelector('.ac-ask input[value="Docs Writer"]').click(); document.querySelector('.ac-ask-go').click(); true`);
            await waitFor(`!!document.querySelector('.ac-ask-agent[data-state="unavailable"] .ac-ask-state') && /Not running/.test(document.querySelector('.ac-ask').textContent)`, 'the 409 results', 6000);
            check(`${tag}: nobody was asked (409 with results): the server's state shows for that agent`, true);
            await ev(`document.querySelector('.ac-ask .ac-cancel').click(); true`);
            await ev(`window.__send = { status: 429, body: { ok: false, error: 'too many comments sent to agents; try again later' } }; ${sendBtn(1)}.click(); true`);
            await waitFor(`document.querySelectorAll('.ac-ask .ac-ask-agent').length >= 3`, 'the agents a third time', 8000);
            await ev(`document.querySelector('.ac-ask input[value="QA Engineer"]:not([aria-disabled="true"])')?.click(); true`);
            b = await block();
            check(`${tag}: an asked agent is not pickable again`, b.go === 'Send to agent' && b.goDisabled);
            await ev(`document.querySelector('.ac-ask input[value="Docs Writer"]:not([aria-disabled="true"])')?.click(); document.querySelector('.ac-ask-go')?.click(); true`);
            await waitFor(`!!document.querySelector('.ac-ask .ac-error:not([hidden])')`, 'the refusal', 6000);
            b = await block();
            check(`${tag}: a refusal with no results says the server's words, and the person can try again`, /too many comments sent/.test(b.error) && b.close === 'Cancel', JSON.stringify(b.error));
            await ev(`document.querySelector('.ac-ask .ac-cancel').click(); true`);

            // the cap
            await ev(`window.__send = { status: 200, body: null }; window.__agents = Array.from({ length: 12 }, (_, i) => ({ name: 'A' + i, role: 'Agent ' + i, agent_type: 'claude', availability: 'available' })); true`);
            await ev(`${sendBtn(1)}.click(); true`);
            await waitFor(`document.querySelectorAll('.ac-ask .ac-ask-agent').length === 12`, 'twelve agents', 8000);
            await ev(`for (let i = 0; i < 12; i++) { const c = document.querySelector('.ac-ask input[value="Agent ' + i + '"]'); if (c && !c.disabled) c.click(); } true`);
            b = await block();
            check(`${tag}: ten at most: the eleventh and twelfth cannot be picked, and it says so`, b.go === 'Send to 10 agents' && b.agents.filter(a => a.checked).length === 10 && b.agents.slice(10).every(a => a.disabled && !a.checked), b.go);
            await ev(`document.querySelector('.ac-ask .ac-cancel').click(); true`);

            // not offered: a comment made with no board; and a viewer who is not the owner
            await ev(`document.querySelector('#artifact-viewer [data-dialog-close], #artifact-viewer .artifact-viewer-close')?.click(); true`);
            await sleep(300); // sleep-ok: the viewer closes over a frame
            await open(null);
            check(`${tag}: a comment made with no board has no Send to agent`, !(await ev(`!!${sendBtn(1)}`)));
            await ev(`document.querySelector('#artifact-viewer [data-dialog-close], #artifact-viewer .artifact-viewer-close')?.click(); true`);
            await sleep(300); // sleep-ok: the viewer closes over a frame
            await ev(`window.__you_next = { can_comment: true, can_lock: false }; true`);
            await open('ask-team', { can_comment: true, can_lock: false });
            check(`${tag}: a viewer who is not the owner has no Send to agent`, !(await ev(`!!${sendBtn(1)}`)));
            await ev(`document.querySelector('#artifact-viewer [data-dialog-close], #artifact-viewer .artifact-viewer-close')?.click(); true`);
            await sleep(300); // sleep-ok: the viewer closes over a frame
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} send to agent checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
