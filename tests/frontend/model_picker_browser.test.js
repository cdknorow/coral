// The model picker and the model on the agent (#4066), against a stubbed /api/agents/models (Backend's contract: {agent_type, models:
// [{value, display_name, description?, efforts?}], fetched_at, refreshing, source, error?}; POST .../refresh is Operator-only, 202 or 429):
//   - the launch form's Model field is a list: Default, the models by display name (the id in small text), Other… (free text);
//   - the last list is cached and shown at once, then the server's replaces it; the agent type switches the list; no list: free text;
//   - what is chosen or typed is what the form returns; values set from code show in the list (or under Other…);
//   - the small refresh button posts, polls while the server says refreshing, and says why when refused (429, 403);
//   - the agent's row and the terminal header show the real model: "Claude · Opus 4.1" (display name from the list, else from the id).
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the form's async loads and a poll interval have no completion event
    const OPUS = { value: 'claude-opus-4-1-20250805', display_name: 'Opus 4.1', description: 'Most capable' };
    const SONNET = { value: 'claude-sonnet-4-5-20250929', display_name: 'Sonnet 4.5' };
    const HAIKU = { value: 'claude-haiku-4-5', display_name: 'Haiku 4.5' };
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // Nothing cached by an earlier suite: the first scene seeds its own cache, the others must start from none.
        await ev(`(() => { for (const k of Object.keys(localStorage)) if (k.startsWith('coral-models:v1:')) localStorage.removeItem(k); return true; })()`);
        // acf_model_field_test leaves a default model in the server's settings, which the form would pre-fill: start from none.
        await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ default_model_claude: '', default_model_codex: '', default_model_agy: '', default_model_terminal: '' }) }).then(r => r.ok)`);
        await ev(`(() => { const R = window.fetch; const OPUS = ${JSON.stringify(OPUS)}, SONNET = ${JSON.stringify(SONNET)};
            window.__calls = []; window.__serve = { claude: { models: [OPUS, SONNET], refreshing: false }, codex: { models: [], refreshing: false } }; window.__delay = 0; window.__refresh = { status: 202 };
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase();
                if (url.pathname === '/api/agents/models' || url.pathname === '/api/agents/models/refresh') {
                    const type = url.searchParams.get('type'); window.__calls.push(m + ' ' + url.pathname + '?type=' + type);
                    const body = () => JSON.stringify({ agent_type: type, fetched_at: '2026-10-16T20:00:00Z', source: 'account', ...(window.__serve[type] || { models: [] }) });
                    const answer = () => url.pathname.endsWith('/refresh') && window.__refresh.status !== 202
                        ? new Response(JSON.stringify({ error: 'no', code: 'x' }), { status: window.__refresh.status, headers: { 'content-type': 'application/json', ...(window.__refresh.status === 429 ? { 'X-Coral-Limit': 'action' } : {}) } })
                        : new Response(body(), { status: url.pathname.endsWith('/refresh') ? 202 : 200, headers: { 'content-type': 'application/json' } });
                    return new Promise(r => setTimeout(() => r(answer()), window.__delay)); }
                return R(u, o); };
            return true; })()`);
        const box = () => ev(`(() => { const c = document.getElementById('acf-t'); const sel = c.querySelector('.acf-model-select'), inp = c.querySelector('.acf-model'), w = c.querySelector('.acf-model-picker');
            return { picker: !w.hidden, options: [...sel.options].map(o => o.textContent), value: sel.value, inputShown: !inp.hidden && getComputedStyle(inp).display !== 'none', inputValue: inp.value, idText: c.querySelector('.acf-model-id').textContent, note: c.querySelector('.acf-model-note').textContent }; })()`);
        const formModel = () => ev(`window.__acf.getAgentConfig('acf-t').model`);

        console.log('1. the cached list shows at once, the server\'s replaces it');
        await ev(`localStorage.setItem('coral-models:v1:local:claude', JSON.stringify({ models: [${JSON.stringify(OPUS)}], fetched_at: null, source: 'account' })); window.__delay = 600;
            const d = document.createElement('div'); d.id = 'acf-t'; document.body.appendChild(d);
            (async () => { const f = await import((window.__coralStatic || '/static/') + 'agent_config_form.js'); window.__acf = f; f.renderAgentConfigForm('acf-t', {}); return true; })()`);
        await waitFor(`!!document.querySelector('#acf-t .acf-model-select option[value="claude-opus-4-1-20250805"]')`, 'the cached list', 3000);
        let b = await box();
        assert.equal(b.picker, true, 'the picker shows from the cache before the server answers');
        assert.deepEqual(b.options, ['Default', 'Opus 4.1', 'Other…', 'Add a model…']);
        await waitFor(`document.querySelectorAll('#acf-t .acf-model-select option').length === 5`, 'the server list', 4000);
        b = await box();
        assert.deepEqual(b.options, ['Default', 'Opus 4.1', 'Sonnet 4.5', 'Other…', 'Add a model…'], 'the server\'s list replaced the cached one');
        assert.equal(b.value, '', 'Default is selected for a blank model'); assert.equal(b.inputShown, false, 'the text box is behind Other…');

        console.log('2. choosing, Other…, Default');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = ${JSON.stringify(SONNET.value)}; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        assert.equal(await formModel(), SONNET.value, 'the form returns the chosen model id');
        assert.equal((await box()).idText, SONNET.value, 'the id shows in small text');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = ${JSON.stringify(OPUS.value)}; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        assert.equal((await box()).idText, 'claude-opus-4-1-20250805 · Most capable', 'the id and the description');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = '__other'; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        b = await box(); assert.equal(b.inputShown, true, 'Other… shows the text box');
        assert.equal(await ev(`document.activeElement.classList.contains('acf-model')`), true, 'and focuses it');
        await ev(`{ const i = document.querySelector('#acf-t .acf-model'); i.value = 'my-private-model'; i.dispatchEvent(new Event('input', { bubbles: true })); true }`);
        assert.equal(await formModel(), 'my-private-model', 'typed text is the model'); assert.equal((await box()).value, '__other', 'the list stays on Other… while typing');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = ''; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        assert.equal(await formModel(), '', 'Default sends no model');

        console.log('3. values set from code');
        await ev(`window.__acf.setAgentConfig('acf-t', { agentType: 'claude', model: ${JSON.stringify(OPUS.value)} }); true`); await sleep(300); // sleep-ok: the form's async loads have no completion event
        b = await box(); assert.equal(b.value, OPUS.value, 'a known model is selected'); assert.equal(b.inputShown, false);
        await ev(`window.__acf.setAgentConfig('acf-t', { agentType: 'claude', model: 'something-else' }); true`); await sleep(300); // sleep-ok: the form's async loads have no completion event
        b = await box(); assert.equal(b.value, 'something-else', 'a model the list does not name shows as itself, not under Other… (#5044)');
        assert.deepEqual(b.options, ['Default', 'Opus 4.1', 'Sonnet 4.5', 'something-else', 'Other…', 'Add a model…'], 'in an option of its own, before Other…');
        assert.equal(b.inputValue, 'something-else'); assert.equal(b.inputShown, false, 'and the text box stays hidden');
        assert.equal(await formModel(), 'something-else', 'the form still returns it');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = '__other'; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        b = await box(); assert.equal(b.value, '__other', 'Other… is the user\'s choice and is still there'); assert.equal(b.inputShown, true); assert.equal(b.inputValue, 'something-else', 'with the text kept to edit');
        assert.deepEqual(b.options, ['Default', 'Opus 4.1', 'Sonnet 4.5', 'Other…', 'Add a model…'], 'the option of its own is gone while the text is edited');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = ${JSON.stringify(OPUS.value)}; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        b = await box(); assert.equal(b.value, OPUS.value); assert.deepEqual(b.options, ['Default', 'Opus 4.1', 'Sonnet 4.5', 'Other…', 'Add a model…'], 'choosing a listed model leaves no extra option');
        await ev(`window.__acf.setAgentConfig('acf-t', { agentType: 'claude', model: 'something-else' }); true`); await sleep(300); // sleep-ok: the form's async loads have no completion event
        b = await box(); assert.equal(b.value, 'something-else', 'set from code again: shown as itself');
        await ev(`window.__acf.setAgentConfig('acf-t', { agentType: 'claude', model: '' }); true`); await sleep(300); // sleep-ok: the form's async loads have no completion event
        b = await box(); assert.equal(b.value, '', 'a blank model is Default'); assert.deepEqual(b.options, ['Default', 'Opus 4.1', 'Sonnet 4.5', 'Other…', 'Add a model…'], 'and the option for the earlier model is gone');

        console.log('4. the agent type switches the list; no list means free text');
        await ev(`{ const t = document.querySelector('#acf-t .acf-agent-type'); t.value = 'codex'; t.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        await waitFor(`window.__calls.some(c => c === 'GET /api/agents/models?type=codex')`, 'the codex list request', 4000);
        await sleep(900); // sleep-ok: the form's async loads have no completion event
        b = await box(); assert.equal(b.picker, false, 'an empty list hides the picker'); assert.equal(b.inputShown, true, 'the plain text box is back');

        console.log('5. refresh');
        await ev(`{ const t = document.querySelector('#acf-t .acf-agent-type'); t.value = 'claude'; t.dispatchEvent(new Event('change', { bubbles: true })); window.__delay = 0; true }`);
        await waitFor(`document.querySelector('#acf-t .acf-model-select').options.length === 5`, 'the claude list again', 4000);
        await ev(`window.__calls.length = 0; window.__serve.claude = { models: [${JSON.stringify(OPUS)}, ${JSON.stringify(SONNET)}], refreshing: true }; document.querySelector('#acf-t .acf-model-refresh').click(); true`);
        await waitFor(`window.__calls.includes('POST /api/agents/models/refresh?type=claude')`, 'the refresh POST', 3000);
        await ev(`setTimeout(() => { window.__serve.claude = { models: [${JSON.stringify(OPUS)}, ${JSON.stringify(SONNET)}, ${JSON.stringify(HAIKU)}], refreshing: false }; }, 4000); true`);
        await waitFor(`document.querySelector('#acf-t .acf-model-note').textContent === 'Updating the list…'`, 'the note while the server refreshes', 4000);
        await waitFor(`document.querySelectorAll('#acf-t .acf-model-select option').length === 6`, 'the list to update after polling', 12000);
        assert.ok((await ev(`window.__calls.filter(c => c.startsWith('GET')).length`)) >= 2, 'it polled while the server said refreshing');
        for (const [status, text] of [[429, /just refreshed/i], [403, /only the operator/i]]) {
            await ev(`window.__refresh = { status: ${status} }; document.querySelector('.toast') && document.querySelectorAll('.toast').forEach(t => t.remove()); document.querySelector('#acf-t .acf-model-refresh').click(); true`);
            await waitFor(`!!document.querySelector('.toast')`, `the ${status} toast`, 3000);
            assert.match(await ev(`document.querySelector('.toast').textContent`), text, `${status} says why`);
        }

        console.log('6. the model on the agent row and in the header');
        await ev(`document.getElementById('acf-t').remove(); window.__m.add('opus-bot', 'A', 3, { sleeping: false }); window.__m.add('sonnet-bot', 'B', 3, { sleeping: false });
            for (const s of T.state.liveSessions) { if (s.name === 'opus-bot') s.model = ${JSON.stringify(OPUS.value)}; if (s.name === 'sonnet-bot') s.model = 'claude-sonnet-4-5-20250929'; }
            localStorage.setItem('coral-show-agent-type', 'true'); localStorage.setItem('coral-models:v1:local:claude', JSON.stringify({ models: [${JSON.stringify(OPUS)}], fetched_at: null, source: 'account' }));
            T.render.renderLiveSessions(T.state.liveSessions); true`);
        await waitFor(`document.querySelectorAll('.session-model').length >= 2`, 'the model lines', 4000);
        const rows = await ev(`Object.fromEntries([...document.querySelectorAll('#live-sessions-list li[data-session-id]')].map(li => [li.dataset.sessionId, li.querySelector('.session-model') && li.querySelector('.session-model').textContent]))`);
        assert.equal(rows['opus-bot-id'], 'Claude · Opus 4.1', 'the display name from the list');
        assert.equal(rows['sonnet-bot-id'], 'Claude · Sonnet 4.5', 'a short name from the id when the list has none');
        await ev(`window.selectLiveSession('opus-bot', 'claude', 'opus-bot-id', 'local'); true`);
        await waitFor(`document.getElementById('terminal-header-label') && document.getElementById('terminal-header-label').textContent.length > 0`, 'the header', 5000);
        // The model is in the input toolbar's chip and the row's tooltip, not in the agent header (#5142).
        assert.equal(await ev(`!!document.getElementById('terminal-header-model') || /Opus 4\\.1/.test(document.querySelector('#live-session-view .terminal-header').textContent)`), false, 'the header does not repeat the model');
        console.log('7. a custom model (#5480) is marked, and its own levels drive the thinking picker');
        await ev(`window.__serve.claude = { thinking: ['low', 'medium', 'high', 'xhigh', 'max'], models: [${JSON.stringify(OPUS)}, { value: 'global-opus-5-5', display_name: 'Global Opus 5.5', custom: true, efforts: ['low', 'high'] }, { value: 'global-plain', display_name: '<img src=x onerror="window.__pwn=1">', custom: true, efforts: [] }], refreshing: false }; localStorage.removeItem('coral-models:v1:local:claude');
            (() => { const old = document.getElementById('acf-t'); if (old) old.remove(); const host = document.createElement('div'); host.id = 'acf-t'; document.body.appendChild(host); window.__acf.renderAgentConfigForm('acf-t', {}); })(); true`);
        await waitFor(`!!document.querySelector('#acf-t .acf-model-select option[value="global-opus-5-5"]')`, 'the custom model in the list', 4000);
        b = await box();
        assert.deepEqual(b.options, ['Default', 'Opus 4.1', 'Global Opus 5.5 (custom)', '<img src=x onerror="window.__pwn=1"> (custom)', 'Other…', 'Add a model…'], 'a custom model says so in the list, and a name with markup is text');
        assert.equal(await ev(`document.querySelectorAll('#acf-t img').length + (window.__pwn ? 1 : 0)`), 0, 'a custom model\'s name never becomes markup');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = 'global-opus-5-5'; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        assert.equal(await formModel(), 'global-opus-5-5', 'the form returns the custom id like any other');
        const levels = await ev(`[...document.querySelectorAll('#acf-t .acf-thinking-select option, #acf-t select[aria-label*="hinking"] option')].map(o => o.value).join()`);
        assert.ok(/low/.test(levels) && /high/.test(levels) && !/xhigh|max|medium/.test(levels), 'the thinking picker offers only the levels the custom model accepts (' + levels + ')');
        await ev(`{ const s = document.querySelector('#acf-t .acf-model-select'); s.value = 'global-plain'; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
        assert.equal(await ev(`document.querySelector('#acf-t .acf-thinking-wrap').hidden`), true, 'a custom model with no level ticked offers no thinking level');
        const sw = await ev(`import((window.__coralStatic || '/static/') + 'model_chip.js').then(m => { const d = { thinking: ['low', 'medium', 'high'], models: [{ value: 'a', custom: true, efforts: ['low', 'high'] }, { value: 'b', custom: true, efforts: [] }, { value: 'c', efforts: [] }, { value: 'd', efforts: ['medium'] }] }; return ['a', 'b', 'c', 'd', 'zz'].map(v => m.levelsFor(d, v).join('+')); })`);
        assert.deepEqual(sw, ['low+high', '', 'low+medium+high', 'medium', 'low+medium+high'], 'the running agent\'s switcher follows the same rule: a custom model offers exactly its ticked levels, none means none, a built-in model with no list takes every level');
        console.log('\nPASS model picker and model labels');
    } finally {
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
})().catch(e => { console.error(e); process.exitCode = 1; });
