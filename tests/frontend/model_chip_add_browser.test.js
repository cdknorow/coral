// "Add a model…" at the bottom of the model chip's menu (#5938): a person whose model is missing adds it where they are looking instead of going to Settings. Held, at 1280 and 390 px in both themes:
// the item is last in the menu and reachable with the keyboard; it opens a small form (the model id, an optional name) with the focus in the id; an empty, a flag-shaped and an existing id are
// refused in words (a status the screen reader hears) and what was typed stays; a good one reads the custom models, adds ONE row for the agent's provider (efforts none) and writes the whole list
// back, then the menu shows the model with the focus on it (it is picked by the person, not applied); a list that could not be read is never written back; a refusal is said ("Only the
// Operator…"); Escape goes back to the list and then closes; the text holds 4.5:1; and on a short phone (the keyboard up) the form is still on screen. The routes are stubbed.
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
            if (m === 'GET') { window.__gets++; return window.__getStatus === 200 ? json({ models: window.__custom, limits: { max_per_family: 50, efforts: {} } }) : json({ error: 'busy' }, window.__getStatus); }
            if (m === 'PUT') { const body = JSON.parse(o.body); window.__puts.push(body); if (window.__putStatus !== 200) return json({ error: 'refused' }, window.__putStatus); window.__custom = body.models; return json({ models: body.models, limits: { max_per_family: 50, efforts: {} } }); } }
        return R(u, o); };
    window.__m.add('ad-codex', 'C', 3, { sleeping: false, agent_type: 'codex' }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`;

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

        for (const [w, mobile] of [[1280, false], [390, true]]) for (const theme of ['dark', 'light']) {
            const tag = `${w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await fresh(tag, theme);

            // 1. the item is last, and the keyboard reaches it
            await openIt();
            await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length === 2`, `${tag}: the list and the add item`, 8000);
            let m = await menu();
            assert.deepEqual(m.labels, ['GPT X', 'Add a model…'], `${tag}: "Add a model…" is last`);
            assert.equal(m.last, true);
            await key('End', 'End', 35);
            assert.equal((await menu()).active, 'add-item', `${tag}: End reaches the add item`);
            await key('Enter', 'Enter', 13);
            await waitFor(`!!document.querySelector('#model-chip-menu form.model-chip-add-form')`, `${tag}: the form`, 3000);
            m = await menu();
            assert.equal(m.active, 'id', `${tag}: the focus is in the model id`);

            // 2. contrast with the form open, then the refusals in words, what is typed stays
            if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/model-add-form-${w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            const au = await auditRegion(ev, ['#model-chip-menu']);
            assert.ok(au.checked > 0 && !au.fails.length, `${tag}: the menu with its form holds 4.5:1: ` + JSON.stringify(au.fails).slice(0, 300));
            await submit();
            await waitFor(`/Enter the model id/.test((document.querySelector('#model-chip-menu .model-chip-add-err') || {}).textContent || '')`, `${tag}: empty is refused`, 3000);
            assert.equal((await menu()).errRole, 'alert', `${tag}: the refusal is an alert`);
            await typeInto('id', '--flag'); await submit();
            await waitFor(`/Use letters, numbers/.test((document.querySelector('#model-chip-menu .model-chip-add-err') || {}).textContent || '')`, `${tag}: a flag-shaped id is refused`, 3000);
            assert.equal((await menu()).id, '--flag', `${tag}: what was typed stays`);
            await typeInto('id', 'gpt-x'); await submit();
            await waitFor(`/already in the list/.test((document.querySelector('#model-chip-menu .model-chip-add-err') || {}).textContent || '')`, `${tag}: an existing id is refused`, 3000);
            assert.equal(await ev(`window.__puts.length`), 0, `${tag}: nothing was written for a refusal`);
            await typeInto('id', 'gpt-x');
            await key('ArrowDown', 'ArrowDown', 40); await key('Home', 'Home', 36);
            assert.equal((await menu()).active, 'id', `${tag}: arrows and Home move the caret in the field, not out of it`);

            // 3. a list that could not be read is never written back; a refusal is said
            await typeInto('id', 'gpt-new');
            await ev(`window.__getStatus = 500; true`); await submit();
            await waitFor(`/Could not read the custom models/.test((document.querySelector('#model-chip-menu .model-chip-add-err') || {}).textContent || '')`, `${tag}: an unreadable list is said`, 3000);
            assert.equal(await ev(`window.__puts.length`), 0, `${tag}: no write after a failed read (the route replaces the whole list)`);
            await ev(`window.__getStatus = 200; window.__putStatus = 403; true`); await submit();
            await waitFor(`/Only the Operator/.test((document.querySelector('#model-chip-menu .model-chip-add-err') || {}).textContent || '')`, `${tag}: a refusal is said`, 3000);
            await ev(`window.__putStatus = 200; window.__puts.length = 0; true`);

            // 4. a good one: one row for the provider, the whole list written back, the menu shows it with the focus on it
            await typeInto('name', 'My New');
            await submit();
            await waitFor(`!!document.querySelector('#model-chip-menu .model-chip-item[data-model="gpt-new"]')`, `${tag}: the model is in the menu`, 8000);
            const put = await ev(`window.__puts[0]`);
            assert.deepEqual(put.models, [{ family: 'claude', value: 'claude-mine', display_name: 'Claude Mine', efforts: ['low'] }, { family: 'codex', value: 'gpt-new', display_name: 'My New', efforts: [] }], `${tag}: the whole list went back with one new row`);
            m = await menu();
            assert.equal(m.active, 'gpt-new', `${tag}: the new model has the keyboard`);
            assert.ok(m.labels.includes('My New (custom)') && m.labels[m.labels.length - 1] === 'Add a model…', `${tag}: ${JSON.stringify(m.labels)}`);
            assert.ok((await ev(`T.state.liveSessions.find(s => s.name === 'ad-codex').model`)) === 'gpt-x', `${tag}: it was added, not applied`);

            // 5. Escape: the form goes back to the list first, then the menu closes
            await key('End', 'End', 35); await key('Enter', 'Enter', 13);
            await waitFor(`!!document.querySelector('#model-chip-menu form.model-chip-add-form')`, `${tag}: the form again`, 3000);
            await key('Escape', 'Escape', 27);
            await waitFor(`!document.querySelector('#model-chip-menu form.model-chip-add-form')`, `${tag}: Escape closed the form`, 3000);
            m = await menu(); assert.equal(m.open, true, `${tag}: the menu stays open`); assert.equal(m.active, 'add-item', `${tag}: the keyboard is back on the item`);
            await key('Escape', 'Escape', 27);
            assert.equal((await menu()).open, false, `${tag}: the second Escape closes the menu`);
            console.log(`   ok ${tag}`);
        }

        // 6. a short phone (the keyboard up): the form is still on the screen
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 420, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await fresh('short phone', 'dark');
        await ev(`document.getElementById('model-chip-bar').hidden = false; true`);
        await openIt();
        await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length === 2`, 'short: the list', 8000);
        await ev(`document.querySelector('#model-chip-menu .model-chip-add').click(); true`);
        await waitFor(`!!document.querySelector('#model-chip-menu form.model-chip-add-form')`, 'short: the form', 3000);
        const r = await ev(`(() => { const f = document.querySelector('#model-chip-menu form.model-chip-add-form'), i = f.querySelector('[name=id]'); const ir = i.getBoundingClientRect(); return { shown: f.getClientRects().length > 0 && getComputedStyle(f).display !== 'none', inView: ir.top >= 0 && ir.bottom <= innerHeight, h: innerHeight }; })()`);
        assert.ok(r.shown && r.inView, `a short phone keeps the add form on screen: ${JSON.stringify(r)}`);
        console.log('PASS the model chip adds a model from its own menu');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
