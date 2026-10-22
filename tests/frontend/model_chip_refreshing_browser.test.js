// The model chip's menu when the server is still asking the CLI for its models (the picker of a CLI that lists nothing at first, like Codex, said "No models to choose from" for good).
// GET /api/agents/models answers with no models and refreshing: true at first; that first answer used to be kept for the session. Held, at 1280 and 390 px in both themes: the menu says
// "Loading models…" (one status, not redrawn by the feed's ticks), asks again, and shows the list when it arrives, without a reload; a final empty answer says there are no models and is NOT
// kept (the next open asks again); a list that arrived is kept (no new request); the loading words hold 4.5:1. The route is stubbed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

const STUB = answers => `(() => { const R = window.fetch; window.__asks = 0; window.__answers = ${JSON.stringify(answers)};
    const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin);
        if (url.pathname === '/api/agents/models') { window.__asks++; const a = window.__answers.length > 1 ? window.__answers.shift() : window.__answers[0]; return Promise.resolve(json({ agent_type: url.searchParams.get('type'), thinking: [], ...a })); }
        return R(u, o); };
    window.__m.add('rf-codex', 'C', 3, { sleeping: false, agent_type: 'codex' }); T.render.renderLiveSessions(T.state.liveSessions);
    T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(window.__m.agents['rf-codex-id']), agent_type: 'codex', model: 'gpt-x' }] }); return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const fresh = async (tag, theme, answers) => {
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
            await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
            await ev(STUB(answers));
            await select('rf-codex');
            await ev(`T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(window.__m.agents['rf-codex-id']), agent_type: 'codex', model: 'gpt-x', thinking_actual: 'low' }] }); true`);
            await waitFor(`!document.getElementById('model-chip-bar').hidden && /gpt.x/i.test(document.getElementById('model-chip-text').textContent)`, `${tag}: the chip`, 8000);
        };
        const menu = () => ev(`(() => { const m = document.getElementById('model-chip-menu'); const e = m.querySelector('.model-chip-empty'); return { open: !m.hidden, items: [...m.querySelectorAll('.model-chip-item-label')].map(i => i.textContent.trim()), empty: e ? e.textContent : '', role: e ? e.getAttribute('role') : null }; })()`);
        const openIt = () => ev(`document.getElementById('model-chip').click(); true`);
        const closeIt = () => ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
        const LIST = { models: [{ value: 'gpt-x', display_name: 'GPT X' }, { value: 'gpt-y', display_name: 'GPT Y' }], refreshing: false };
        for (const [w, mobile] of [[1280, false], [390, true]]) for (const theme of ['dark', 'light']) {
            const tag = `${w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile });

            // 1. refreshing and empty twice, then the list: Loading models… while it waits, then the list, no reload
            await fresh(tag, theme, [{ models: [], refreshing: true }, { models: [], refreshing: true }, LIST]);
            await openIt();
            await waitFor(`/Loading models/.test((document.querySelector('#model-chip-menu .model-chip-empty') || {}).textContent || '')`, `${tag}: Loading models… is said`, 5000);
            let m = await menu();
            assert.equal(m.role, 'status', `${tag}: the loading line is a status`);
            if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/model-chip-loading-${w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            const au = await auditRegion(ev, ['#model-chip-menu']);
            assert.ok(au.checked > 0 && !au.fails.length, `${tag}: the loading words hold 4.5:1: ` + JSON.stringify(au.fails).slice(0, 200));
            await ev(`window.__loadingNode = document.querySelector('#model-chip-menu .model-chip-empty'); T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(window.__m.agents['rf-codex-id']), agent_type: 'codex', model: 'gpt-x', thinking_actual: 'low' }] }); true`);
            assert.ok(await ev(`window.__loadingNode === document.querySelector('#model-chip-menu .model-chip-empty')`), `${tag}: a feed tick does not redraw the loading line (it is announced once)`);
            await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length === 2`, `${tag}: the list arrives without a reload`, 12000);
            m = await menu();
            assert.deepEqual(m.items, ['GPT X', 'GPT Y'], `${tag}: the list`);
            assert.equal(await ev(`window.__asks`), 3, `${tag}: it asked until the list came`);
            await closeIt(); await openIt();
            await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length === 2`, `${tag}: reopened`, 5000);
            assert.equal(await ev(`window.__asks`), 3, `${tag}: a list that arrived is kept (no new request)`);

            // 2. a final empty answer: says there are no models, is not kept, and the next open asks again
            await fresh(tag, theme, [{ models: [], refreshing: false, error: 'the CLI listed nothing' }]);
            await openIt();
            await waitFor(`/No models to choose from/.test((document.querySelector('#model-chip-menu .model-chip-empty') || {}).textContent || '')`, `${tag}: a final empty says so`, 5000);
            assert.equal(await ev(`window.__asks`), 1);
            await closeIt();
            await ev(`window.__answers = [{ models: [{ value: 'gpt-z', display_name: 'GPT Z' }], refreshing: false }]; true`);
            await openIt();
            await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length === 1`, `${tag}: the next open asks again and shows the list`, 5000);
            assert.equal(await ev(`window.__asks`), 2, `${tag}: the empty answer was not kept`);
            console.log(`   ok ${tag}`);
        }
        console.log('PASS the model chip waits for a refreshing list');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
