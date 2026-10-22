// Custom models through Coral Hub (#5996): GET and PUT /api/custom-models are the machine owner's alone through the hub, so a person the machine refuses (403 owner_only) is not offered
// "Add a model…" in the model chip's menu or in the launch and restart forms' list; the owner, and this computer, are (the read is answered). A server that did not answer or has no such route keeps the
// item (its own error says what went wrong). Held at 1280 and 390 px. The routes are stubbed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

const STUB = `(() => { const R = window.fetch; window.__asks = 0; window.__gets = 0; window.__puts = []; window.__getStatus = 200; window.__putStatus = 200;
    window.__custom = [{ family: 'claude', value: 'claude-mine', display_name: 'Claude Mine', efforts: ['low'] }];
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin), m = (o.method || 'GET').toUpperCase();
        if (url.pathname === '/api/agents/models') { window.__asks++; const type = url.searchParams.get('type');
            return json({ agent_type: type, thinking: [], refreshing: false, models: [{ value: 'gpt-x', display_name: 'GPT X' }, ...window.__custom.filter(c => c.family === 'codex').map(c => ({ value: c.value, display_name: c.display_name || c.value, custom: true }))] }); }
        if (url.pathname === '/api/custom-models') {
            if (m === 'GET') { window.__gets++; return window.__getStatus === 200 ? json({ models: window.__custom, limits: { max_per_family: 50, efforts: {} } }) : json(window.__getStatus === 403 ? { error: 'owner_only', message: 'through Coral Hub only the account this computer is linked to can add or change custom models' } : { error: 'busy' }, window.__getStatus); }
            if (m === 'PUT') { const body = JSON.parse(o.body); window.__puts.push(body); if (window.__putStatus !== 200) return json({ error: 'refused' }, window.__putStatus); window.__custom = body.models; return json({ models: body.models, limits: { max_per_family: 50, efforts: {} } }); } }
        return R(u, o); };
    window.__m.add('ad-codex', 'C', 3, { sleeping: false, agent_type: 'codex' }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`;

const PSTUB = `(() => { const R = window.fetch; window.__asks = 0; window.__puts = []; window.__getStatus = 200; window.__putStatus = 200;
    window.__custom = [{ family: 'codex', value: 'codex-mine', display_name: 'Codex Mine', efforts: [] }];
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin), m = (o.method || 'GET').toUpperCase();
        if (url.pathname === '/api/agents/models') { window.__asks++; return json({ agent_type: 'claude', thinking: [], refreshing: false, models: [{ value: 'claude-a', display_name: 'Claude A' }, ...window.__custom.filter(c => c.family === 'claude').map(c => ({ value: c.value, display_name: c.display_name || c.value, custom: true }))] }); }
        if (url.pathname === '/api/custom-models') {
            if (m === 'GET') return window.__getStatus === 200 ? json({ models: window.__custom, limits: { max_per_family: 50, efforts: {} } }) : json(window.__getStatus === 403 ? { error: 'owner_only' } : { error: 'busy' }, window.__getStatus);
            if (m === 'PUT') { const body = JSON.parse(o.body); window.__puts.push(body); if (window.__putStatus !== 200) return json({ error: 'refused' }, window.__putStatus); window.__custom = body.models; return json({ models: body.models, limits: { max_per_family: 50, efforts: {} } }); } }
        return R(u, o); };
    return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const { Input } = client;
    const key = async (k, code, vk) => { const b = { key: k, code, windowsVirtualKeyCode: vk }; const text = k === 'Enter' ? '\r' : k === ' ' ? ' ' : undefined; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(text ? { text } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await client.Emulation.setFocusEmulationEnabled({ enabled: true });
        const fresh = async (tag, theme) => {
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
            await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
            await ev(STUB);
            await select('ad-codex');
            await ev(`T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(window.__m.agents['ad-codex-id']), agent_type: 'codex', model: 'gpt-x', thinking_actual: 'low' }] }); true`);
            await waitFor(`!document.getElementById('model-chip-bar').hidden && /gpt.x/i.test(document.getElementById('model-chip-text').textContent)`, `${tag}: the chip`, 8000);
        };
        const menu = () => ev(`(() => { const m = document.getElementById('model-chip-menu'); const items = [...m.querySelectorAll('.model-chip-item')]; const f = m.querySelector('form.model-chip-add-form'); const err = m.querySelector('.model-chip-add-err');
            return { open: !m.hidden, labels: items.map(i => i.querySelector('.model-chip-item-label').textContent.trim()), last: items.length ? items[items.length - 1].hasAttribute('data-add') : false, form: !!f, err: err ? err.textContent : '', errRole: err ? err.getAttribute('role') : null, id: f ? f.querySelector('[name=id]').value : null, active: document.activeElement && (document.activeElement.name || (document.activeElement.hasAttribute('data-add') ? 'add-item' : document.activeElement.dataset.model || document.activeElement.className)) }; })()`);
        const openIt = () => ev(`document.getElementById('model-chip').click(); true`);
        const typeInto = (name, text) => ev(`(() => { const i = document.querySelector('#model-chip-menu form [name=${name}]'); i.focus(); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        const submit = () => ev(`document.querySelector('#model-chip-menu form button[type=submit]').click(); true`);


        // The chip's menu: the owner (and this computer) has the item, a refused person does not.
        for (const [w, mobile] of [[1280, false], [390, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            for (const [status, expected, why] of [[200, ['GPT X', 'Add a model…'], 'answered: the item is there'], [403, ['GPT X'], 'refused (owner_only): no item'], [500, ['GPT X', 'Add a model…'], 'not answered: the item stays']]) {
                const tag = `${w}px chip, GET custom-models ${status} (${why})`;
                await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
                await ev(`window.__coralReady.then(() => true)`);
                await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
                await ev(STUB); await ev(`window.__getStatus = ${status}; true`);
                await select('ad-codex');
                await ev(`T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(window.__m.agents['ad-codex-id']), agent_type: 'codex', model: 'gpt-x', thinking_actual: 'low' }] }); true`);
                await waitFor(`!document.getElementById('model-chip-bar').hidden && /gpt.x/i.test(document.getElementById('model-chip-text').textContent)`, `${tag}: the chip`, 8000);
                await openIt();
                await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length >= 1 && window.__gets >= 1`, `${tag}: the list and the read`, 8000);
                await waitFor(`(() => { const n = document.querySelectorAll('#model-chip-menu .model-chip-add').length; return ${expected.length === 2} ? n === 1 : n === 0; })()`, `${tag}: the item settles`, 4000);
                assert.deepEqual((await menu()).labels, expected, `${tag}: ${why}`);
            }
        }
        console.log('PASS the model chip offers "Add a model…" only to a person the machine lets add models');

        // The launch and restart forms' list: the same.
        for (const [w, mobile] of [[1280, false], [390, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            for (const [status, expected, why] of [[200, ['Default', 'Claude A', 'Other…', 'Add a model…'], 'answered: the item is there'], [403, ['Default', 'Claude A', 'Other…'], 'refused (owner_only): no item'], [500, ['Default', 'Claude A', 'Other…', 'Add a model…'], 'not answered: the item stays']]) {
                const tag = `${w}px form list, GET custom-models ${status} (${why})`;
                await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
                await ev(`window.__coralReady.then(() => true)`);
                await ev(`(() => { for (const k of Object.keys(localStorage)) if (k.startsWith('coral-models:v1:')) localStorage.removeItem(k); return true; })()`);
                await ev(PSTUB); await ev(`window.__getStatus = ${status}; true`);
                await ev(`const d = document.createElement('div'); d.id = 'acf-t'; document.body.appendChild(d);
                    (async () => { const f = await import((window.__coralStatic || '/static/') + 'agent_config_form.js'); window.__acf = f; f.renderAgentConfigForm('acf-t', {}); return true; })()`);
                await waitFor(`!!document.querySelector('#acf-t .acf-model-select option[value="claude-a"]')`, `${tag}: the list`, 5000);
                await waitFor(`(() => { const n = [...document.querySelectorAll('#acf-t .acf-model-select option')].map(o => o.textContent); return n.length === ${expected.length}; })()`, `${tag}: the list settles`, 4000);
                const options = await ev(`[...document.querySelectorAll('#acf-t .acf-model-select option')].map(o => o.textContent)`);
                assert.deepEqual(options, expected, `${tag}: ${why}`);
            }
        }
        console.log('PASS the launch and restart forms offer "Add a model…" only to a person the machine lets add models');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
