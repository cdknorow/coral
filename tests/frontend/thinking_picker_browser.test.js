// The Thinking select of the agent config form (#4969): beside the Model on the launch form and the restart form.
//   - it offers the levels the models reply lists for the agent type (`thinking`), narrowed to the chosen model's own `efforts`;
//   - it is hidden for a type that lists none (codex today) and for a terminal;
//   - the form's value is undefined until the user chooses or the form is given a level, so a restart that was not asked to change
//     the level leaves the agent's alone; "Default" is "" (sent, and clears it);
//   - a level the newly chosen model does not take falls back to Default and says so;
//   - the restart request carries `thinking` only when it was chosen. Desktop and a 390x844 phone.
// Against a stubbed /api/agents/models; run against an isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the form's async loads have no completion event
    const LEVELS = ['low', 'medium', 'high', 'xhigh', 'max'];
    const OPUS = { value: 'opus-x', display_name: 'Opus X', efforts: LEVELS };
    const SONNET = { value: 'sonnet-x', display_name: 'Sonnet X', efforts: ['low', 'medium', 'high'] };
    const PLAIN = { value: 'plain-x', display_name: 'Plain X' }; // lists no efforts: the type's levels apply
    const guard = await require('./helpers/settings_guard').guardSettings();
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { for (const k of Object.keys(localStorage)) if (k.startsWith('coral-models:v1:')) localStorage.removeItem(k); return true; })()`);
        await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ default_model_claude: '', default_model_codex: '' }) }).then(() => true)`);
        await ev(`(() => { const R = window.fetch;
            window.__restarts = [];
            window.__serve = { claude: { models: [${JSON.stringify(OPUS)}, ${JSON.stringify(SONNET)}, ${JSON.stringify(PLAIN)}], thinking: ${JSON.stringify(LEVELS)} }, codex: { models: [{ value: 'gpt-x', display_name: 'GPT X', efforts: ['low', 'high'] }] } };
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase();
                if (url.pathname === '/api/agents/models') { const type = url.searchParams.get('type');
                    return Promise.resolve(new Response(JSON.stringify({ agent_type: type, fetched_at: '2026-10-19T00:00:00Z', refreshing: false, source: 'account', ...(window.__serve[type] || { models: [] }) }), { status: 200, headers: { 'content-type': 'application/json' } })); }
                const rm = url.pathname.match(/\\/api\\/sessions\\/live\\/[^/]+\\/restart$/);
                if (rm && m === 'POST') { window.__restarts.push(JSON.parse(o.body)); return Promise.resolve(new Response(JSON.stringify({ ok: true, session_id: 'th-a-new', session_name: 'th-a', agent_name: 'th-a' }), { status: 200, headers: { 'content-type': 'application/json' } })); }
                if (/\\/api\\/sessions\\/live\\/[^/?]+\\?session_id=/.test(url.pathname + url.search)) return Promise.resolve(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'content-type': 'application/json' } }));
                return R(u, o); };
            const d = document.createElement('div'); d.id = 'acf-t'; document.body.appendChild(d);
            return import((window.__coralStatic || '/static/') + 'agent_config_form.js').then(f => { window.__acf = f; f.renderAgentConfigForm('acf-t', {}); return true; }); })()`);
        const th = () => ev(`(() => { const c = document.getElementById('acf-t'); const w = c.querySelector('.acf-thinking-wrap'), s = c.querySelector('.acf-thinking');
            const r = w.getBoundingClientRect();
            return { shown: !w.hidden && r.width > 0, options: [...s.options].map(o => o.textContent), value: s.value, note: c.querySelector('.acf-thinking-note').textContent, config: window.__acf.getAgentConfig('acf-t').thinking, width: Math.round(r.width) }; })()`);
        const choose = (sel, value) => ev(`{ const s = document.querySelector(${JSON.stringify(sel)}); s.value = ${JSON.stringify(value)}; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);

        const run = async (label) => {
            console.log(`${label}: the levels of the type, and nothing sent until it is chosen`);
            await waitFor(`!document.querySelector('#acf-t .acf-thinking-wrap').hidden`, 'the Thinking select', 4000);
            let t = await th();
            assert.deepEqual(t.options, ['Default', 'Low', 'Medium', 'High', 'Extra high', 'Max']);
            assert.equal(t.config, undefined, 'not chosen: the form returns no level');
            assert.ok(t.shown && t.width > 120, 'it has room: ' + t.width);
            await choose('#acf-t .acf-thinking', 'high');
            assert.equal((await th()).config, 'high');
            await choose('#acf-t .acf-thinking', '');
            assert.equal((await th()).config, '', 'Default is chosen: "" (sent, so a restart clears the level)');
            await shot('thinking-picker-' + label.split(' ')[0].toLowerCase());

            console.log(`${label}: a model's own efforts narrow the levels, and a level it does not take falls back to Default`);
            await choose('#acf-t .acf-thinking', 'max');
            await choose('#acf-t .acf-model-select', 'sonnet-x');
            t = await th();
            assert.deepEqual(t.options, ['Default', 'Low', 'Medium', 'High'], 'Sonnet X takes up to high');
            assert.equal(t.value, '', 'max is not offered: back to Default');
            assert.match(t.note, /Max is not offered for sonnet-x: back to Default/);
            await choose('#acf-t .acf-thinking', 'medium');
            await choose('#acf-t .acf-model-select', 'opus-x');
            t = await th();
            assert.equal(t.value, 'medium', 'a level the new model takes is kept');
            assert.equal(t.note, '');
            await choose('#acf-t .acf-model-select', 'plain-x');
            assert.deepEqual((await th()).options, ['Default', 'Low', 'Medium', 'High', 'Extra high', 'Max'], 'a model that lists no efforts: the type\'s levels');

            console.log(`${label}: another type that takes no level, a terminal, and a level given from code`);
            await choose('#acf-t .acf-agent-type', 'codex');
            await waitFor(`document.querySelector('#acf-t .acf-thinking-wrap').hidden`, 'the select to go for codex', 4000);
            assert.equal((await th()).config, undefined, 'hidden: no level');
            await choose('#acf-t .acf-agent-type', 'terminal');
            await waitFor(`!document.querySelector('#acf-t .acf-model-wrap') || getComputedStyle(document.querySelector('#acf-t .acf-model-wrap')).display === 'none'`, 'the terminal form', 4000);
            assert.equal((await th()).shown, false);
            assert.equal((await th()).config, undefined);
            await choose('#acf-t .acf-agent-type', 'claude');
            await waitFor(`!document.querySelector('#acf-t .acf-thinking-wrap').hidden`, 'the select back for claude', 4000);
            await ev(`window.__acf.setAgentConfig('acf-t', { agentType: 'claude', thinking: 'xhigh' }); true`);
            await waitFor(`document.querySelector('#acf-t .acf-thinking').value === 'xhigh'`, 'the level given from code', 4000);
            t = await th();
            assert.equal(t.value, 'xhigh');
            assert.equal(t.config, 'xhigh', 'a level given from code is the form\'s');
            await ev(`window.__acf.setAgentConfig('acf-t', { agentType: 'claude' }); true`);
            await waitFor(`window.__acf.getAgentConfig('acf-t').thinking === undefined`, 'the form to forget the level', 4000);
            assert.equal((await th()).config, undefined, 'a form given none forgets the earlier choice');
        };

        await run('Desktop');

        console.log('The restart request carries the level only when it was chosen');
        await ev(`document.getElementById('acf-t').remove(); window.__m.add('th-a', 'A', 3, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        await select('th-a');
        const restartPayload = async (level) => {
            await ev(`window.__restarts.length = 0; (async () => { await window.restartSession(); return true; })()`);
            await waitFor(`!!document.querySelector('#restart-acf .acf-thinking-wrap') && !document.querySelector('#restart-acf .acf-thinking-wrap').hidden`, 'the restart form', 5000);
            if (level !== undefined) await choose('#restart-acf .acf-thinking', level);
            await ev(`(() => { window.confirmRestart(); return true; })()`);
            await waitFor(`window.__restarts.length === 1`, 'the restart request', 5000);
            return ev(`window.__restarts[0]`);
        };
        let body = await restartPayload(undefined);
        assert.equal('thinking' in body, false, 'not chosen: the request leaves the agent\'s level alone: ' + JSON.stringify(body));
        body = await restartPayload('high');
        assert.equal(body.thinking, 'high');
        body = await restartPayload('');
        assert.equal(body.thinking, '', 'Default clears it');
        // An agent that was started with a level shows it on the restart form, and the restart keeps it.
        await ev(`T.state.currentSession.thinking = 'max'; true`);
        await ev(`window.__restarts.length = 0; (async () => { await window.restartSession(); return true; })()`);
        await waitFor(`!!document.querySelector('#restart-acf .acf-thinking') && document.querySelector('#restart-acf .acf-thinking').value === 'max'`, 'the restart form to show the level it started with', 5000);
        await ev(`(() => { window.confirmRestart(); return true; })()`);
        await waitFor(`window.__restarts.length === 1`, 'the restart request', 5000);
        assert.equal((await ev(`window.__restarts[0]`)).thinking, 'max', 'the level it started with is kept');
        console.log('   ✓');

        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await sleep(300); // sleep-ok: the phone layout settles
        await ev(`(() => { for (const id of ['acf-t']) document.getElementById(id)?.remove(); const d = document.createElement('div'); d.id = 'acf-t'; document.body.appendChild(d); window.__acf.renderAgentConfigForm('acf-t', {}); return true; })()`);
        await run('Phone 390x844');
        await Emulation.clearDeviceMetricsOverride();
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        console.log('\nPASS thinking picker');
    } finally {
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
})().catch(e => { console.error(e); process.exitCode = 1; });
