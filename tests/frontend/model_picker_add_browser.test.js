// "Add a model…" in the Model field of the launch and restart forms (#5938, the same form and the same catalog write as the model chip's menu): the list ends with Add a model… (after Other…); choosing
// it opens a small form under the list with the focus in the id while the list keeps showing what it showed; an empty, flag-shaped or existing id is refused in words (an alert) and what was
// typed stays; Escape and Cancel close the form and give the keyboard back to the list (the dialog around it stays open); a good id reads the custom models, writes the whole list back with ONE
// new row for the provider, asks for the list again and chooses the new model for this form; a read that failed is never written back; the form holds 4.5:1 and its targets are 44 px on a phone.
// Both themes, 1280 and 390. The routes are stubbed; the form is rendered on its own (as model_picker_browser does).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

const STUB = `(() => { const R = window.fetch; window.__asks = 0; window.__puts = []; window.__getStatus = 200; window.__putStatus = 200;
    window.__custom = [{ family: 'codex', value: 'codex-mine', display_name: 'Codex Mine', efforts: [] }];
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin), m = (o.method || 'GET').toUpperCase();
        if (url.pathname === '/api/agents/models') { window.__asks++; return json({ agent_type: 'claude', thinking: [], refreshing: false, models: [{ value: 'claude-a', display_name: 'Claude A' }, ...window.__custom.filter(c => c.family === 'claude').map(c => ({ value: c.value, display_name: c.display_name || c.value, custom: true }))] }); }
        if (url.pathname === '/api/custom-models') {
            if (m === 'GET') return window.__getStatus === 200 ? json({ models: window.__custom, limits: { max_per_family: 50, efforts: {} } }) : json({ error: 'busy' }, window.__getStatus);
            if (m === 'PUT') { const body = JSON.parse(o.body); window.__puts.push(body); if (window.__putStatus !== 200) return json({ error: 'refused' }, window.__putStatus); window.__custom = body.models; return json({ models: body.models, limits: { max_per_family: 50, efforts: {} } }); } }
        return R(u, o); };
    return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const key = async (k, code, vk) => { const b = { key: k, code, windowsVirtualKeyCode: vk }; const text = k === 'Enter' ? '\r' : undefined; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(text ? { text } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await client.Emulation.setFocusEmulationEnabled({ enabled: true });
        const box = () => ev(`(() => { const c = document.getElementById('acf-t'); const sel = c.querySelector('.acf-model-select'), f = c.querySelector('.acf-model-add form'); const err = c.querySelector('.model-chip-add-err');
            return { options: [...sel.options].map(o => o.textContent), value: sel.value, form: !!f && !c.querySelector('.acf-model-add').hidden, err: err && !err.hidden ? err.textContent : '', errRole: err ? err.getAttribute('role') : null, id: f ? f.querySelector('[name=id]').value : null, active: document.activeElement === sel ? 'select' : (document.activeElement.name || document.activeElement.className), model: window.__acf.getAgentConfig('acf-t').model }; })()`);
        const pick = value => ev(`(() => { const s = document.querySelector('#acf-t .acf-model-select'); s.value = ${JSON.stringify(value)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        const typeInto = (name, text) => ev(`(() => { const i = document.querySelector('#acf-t .acf-model-add form [name=${name}]'); i.focus(); i.value = ${JSON.stringify(text)}; return true; })()`);
        const submit = () => ev(`document.querySelector('#acf-t .acf-model-add form button[type=submit]').click(); true`);
        for (const [w, mobile] of [[1280, false], [390, true]]) for (const theme of ['dark', 'light']) {
            const tag = `${w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
            await ev(`(() => { for (const k of Object.keys(localStorage)) if (k.startsWith('coral-models:v1:')) localStorage.removeItem(k); return true; })()`);
            await ev(STUB);
            await ev(`const d = document.createElement('div'); d.id = 'acf-t'; document.body.appendChild(d);
                (async () => { const f = await import((window.__coralStatic || '/static/') + 'agent_config_form.js'); window.__acf = f; f.renderAgentConfigForm('acf-t', {}); return true; })()`);
            await waitFor(`!!document.querySelector('#acf-t .acf-model-select option[value="claude-a"]')`, `${tag}: the list`, 5000);
            let b = await box();
            assert.deepEqual(b.options, ['Default', 'Claude A', 'Other…', 'Add a model…'], `${tag}: Add a model… ends the list`);

            // choosing it opens the form; the list keeps showing what it showed
            await pick('__add');
            await waitFor(`!!document.querySelector('#acf-t .acf-model-add form')`, `${tag}: the form`, 3000);
            b = await box();
            assert.equal(b.value, '', `${tag}: the list still shows Default`);
            assert.equal(b.active, 'id', `${tag}: the focus is in the model id`);
            if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/picker-add-${w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            const au = await auditRegion(ev, ['#acf-t .acf-model-add']);
            assert.ok(au.checked > 0 && !au.fails.length, `${tag}: the form holds 4.5:1: ` + JSON.stringify(au.fails).slice(0, 300));
            if (mobile) assert.ok(await ev(`[...document.querySelectorAll('#acf-t .acf-model-add input, #acf-t .acf-model-add button')].every(e => e.getBoundingClientRect().height >= 44)`), `${tag}: 44 px targets`);

            // refusals in words, what was typed stays, nothing written
            await submit();
            await waitFor(`/Enter the model id/.test((document.querySelector('#acf-t .model-chip-add-err') || {}).textContent || '')`, `${tag}: empty is refused`, 3000);
            assert.equal((await box()).errRole, 'alert', `${tag}: the refusal is an alert`);
            await typeInto('id', '--flag'); await submit();
            await waitFor(`/Use letters, numbers/.test((document.querySelector('#acf-t .model-chip-add-err') || {}).textContent || '')`, `${tag}: flag-shaped is refused`, 3000);
            assert.equal((await box()).id, '--flag', `${tag}: what was typed stays`);
            await typeInto('id', 'claude-a'); await submit();
            await waitFor(`/already in the list/.test((document.querySelector('#acf-t .model-chip-add-err') || {}).textContent || '')`, `${tag}: an existing id is refused`, 3000);
            assert.equal(await ev(`window.__puts.length`), 0, `${tag}: nothing written for a refusal`);
            await typeInto('id', 'claude-new'); await ev(`window.__getStatus = 500; true`); await submit();
            await waitFor(`/Could not read the custom models/.test((document.querySelector('#acf-t .model-chip-add-err') || {}).textContent || '')`, `${tag}: an unreadable list is said`, 3000);
            assert.equal(await ev(`window.__puts.length`), 0, `${tag}: no write after a failed read`);
            await ev(`window.__getStatus = 200; true`);

            // Escape closes the form and gives the keyboard to the list
            await key('Escape', 'Escape', 27);
            await waitFor(`!document.querySelector('#acf-t .acf-model-add form')`, `${tag}: Escape closed the form`, 3000);
            b = await box(); assert.equal(b.active, 'select', `${tag}: the keyboard is on the list`); assert.equal(b.value, '');

            // a good one: one new row, the whole list back, the list asked again, the new model chosen
            await pick('__add');
            await waitFor(`!!document.querySelector('#acf-t .acf-model-add form')`, `${tag}: the form again`, 3000);
            await typeInto('id', 'claude-new'); await typeInto('name', 'Claude New');
            const asks = await ev(`window.__asks`);
            await submit();
            await waitFor(`!!document.querySelector('#acf-t .acf-model-select option[value="claude-new"]') && !document.querySelector('#acf-t .acf-model-add form')`, `${tag}: the model is in the list, the form is gone`, 8000);
            assert.deepEqual((await ev(`window.__puts[0]`)).models, [{ family: 'codex', value: 'codex-mine', display_name: 'Codex Mine', efforts: [] }, { family: 'claude', value: 'claude-new', display_name: 'Claude New', efforts: [] }], `${tag}: the whole list went back with one new row`);
            assert.ok((await ev(`window.__asks`)) > asks, `${tag}: the list was asked for again`);
            b = await box();
            assert.equal(b.value, 'claude-new', `${tag}: the new model is chosen`); assert.equal(b.model, 'claude-new', `${tag}: and it is the form's value`);
            assert.equal(b.active, 'select', `${tag}: the keyboard is on the list`);
            assert.ok(b.options.includes('Claude New (custom)'), `${tag}: ${JSON.stringify(b.options)}`);
            console.log(`   ok ${tag}`);
        }
        // the real launch dialog: the field is under More options, ends with Add a model…, and the form opens inside the dialog (Escape closes the form first, not the dialog)
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, 'dark');
        await ev(`(() => { for (const k of Object.keys(localStorage)) if (k.startsWith('coral-models:v1:')) localStorage.removeItem(k); return true; })()`);
        await ev(STUB);
        await ev(`(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); document.getElementById('launch-agent-more').dispatchEvent(new CustomEvent('sd-more-open', { bubbles: true })); return true; })()`);
        await waitFor(`!!document.querySelector('#launch-agent-acf .acf-model-select option[value="claude-a"]')`, 'launch dialog: the list', 8000);
        await ev(`[...document.querySelectorAll('#launch-modal [role=tab]')].find(b => /Agent\s*$/.test(b.textContent.trim())).click(); true`); // the Model field is in the Agent group
        await waitFor(`document.querySelector('#launch-agent-acf .acf-model-select').getClientRects().length > 0`, 'launch dialog: the Agent group shown', 3000);
        assert.deepEqual(await ev(`[...document.querySelectorAll('#launch-agent-acf .acf-model-select option')].map(o => o.textContent)`), ['Default', 'Claude A', 'Other…', 'Add a model…'], 'the launch dialog ends its list with Add a model…');
        await ev(`(() => { const s = document.querySelector('#launch-agent-acf .acf-model-select'); s.value = '__add'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await waitFor(`!!document.querySelector('#launch-agent-acf .acf-model-add form')`, 'launch dialog: the form', 3000);
        if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/picker-add-launch-dialog.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
        await key('Escape', 'Escape', 27);
        await waitFor(`!document.querySelector('#launch-agent-acf .acf-model-add form')`, 'launch dialog: Escape closed the form', 3000);
        assert.equal(await ev(`!!document.querySelector('#launch-modal.active, #launch-modal[style*="flex"]') || getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`), true, 'the dialog is still open');
        console.log('PASS the Model field adds a model from its own list');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
