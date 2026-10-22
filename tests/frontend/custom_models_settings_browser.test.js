// Settings > Agents > Custom models (#5480): the model ids a person adds per provider and the thinking levels each accepts. Against a stub of GET and PUT /api/custom-models: the section lists what the
// server has, an id is checked before it is added (never flag-shaped, no spaces, no duplicate in the provider), levels are offered only for a provider that takes them, edit and remove keep the keyboard
// where it was, nothing is saved until Apply (one PUT with the whole list), Apply makes every picker forget its cached model list, the section is hidden when the server has no such route, and it reads
// in both themes at 1280 and 390. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

const SEED = [{ id: 1, family: 'claude', value: 'global-opus-5-5', display_name: 'Global Opus 5.5', efforts: ['low', 'high'] }, { id: 2, family: 'pi', value: 'openai/gpt-5-mini', display_name: '', efforts: [] }, { id: 3, family: 'agy', value: 'img-test', display_name: '<img src=x onerror="window.__pwn=1">', efforts: [] }];
const LIMITS = { max_per_family: 50, max_total: 200, efforts: { claude: ['low', 'medium', 'high', 'xhigh', 'max'], codex: [], agy: [], pi: [] } };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const guard = await guardSettings();
    const STUB = (route) => `(() => { const R = window.fetch; window.__cm = { puts: [], store: ${JSON.stringify(SEED)}, route: ${route} };
        const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
        window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            if (p === '/api/custom-models') { if (!window.__cm.route) return json({ error: 'not found' }, 404); if (m === 'GET') { const body = { models: window.__cm.store, limits: ${JSON.stringify(LIMITS)} }; return window.__cm.hold ? window.__cm.hold.then(() => json(body)) : json(body); }
                if (m === 'PUT') { const b = JSON.parse(o.body); window.__cm.puts.push(b); if (window.__cm.fail) return json({ error: 'custom model 1 (codex "gpt-6-astra"), value: refused for the test', row: 0, family: 'codex', field: 'value', message: 'refused for the test' }, 400); window.__cm.store = b.models.map((m, i) => ({ id: i + 1, ...m })); return json({ models: window.__cm.store, limits: ${JSON.stringify(LIMITS)} }); } }
            return R(u, o); }; return true; })()`;
    const open = async (view, theme, route = true) => {
        await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
        await ev(STUB(route));
        await ev(`window.showSettingsModal('agents'); true`);
        if (route) await waitFor(`document.querySelectorAll('#settings-custom-models .cm-row').length === 3 && !!document.querySelector('#settings-custom-models [data-cm-add]')`, 'the list and its one Add button', 8000);
        else await waitFor(`!!document.getElementById('settings-custom-models-row')`, 'the row', 5000);
    };
    const addFor = async p => { await ev(`document.querySelector('#settings-custom-models [data-cm-add]').click(); true`); await ev(`(() => { const s = document.getElementById('cm-provider'); s.value = ${JSON.stringify(p)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); };
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    const type = async text => { for (const ch of text) await Input.dispatchKeyEvent({ type: 'char', text: ch }); };
    const focusId = () => ev(`(() => { const a = document.activeElement; return a ? (a.id || a.getAttribute('data-cm-add') || a.getAttribute('data-cm-edit') || a.textContent.trim()) : ''; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. the list, add, edit, remove (desktop, light)');
        await open({ w: 1280, h: 900, phone: false }, 'light');
        let t = await ev(`[...document.querySelectorAll('#settings-custom-models .cm-row, #settings-custom-models .cm-empty')].map(r => r.innerText.replace(/\\s+/g, ' ').trim())`);
        check(`one list of what the server has, each row naming its provider (${JSON.stringify(t)})`, t.length === 3 && /^Claude global-opus-5-5 Global Opus 5\.5 low high/.test(t[0]) && t[1].startsWith('Antigravity img-test <img src=x onerror="window.__pwn=1">') && /^Pi openai\/gpt-5-mini/.test(t[2]) && (await ev(`document.querySelectorAll('#settings-custom-models [data-cm-add]').length`)) === 1);
        check('a name with markup is shown as text and never becomes an element', (await ev(`document.querySelectorAll('#settings-custom-models img').length + (window.__pwn ? 1 : 0)`)) === 0);
        check('nothing is unsaved yet', (await ev(`document.querySelector('#settings-custom-models .cm-status').textContent`)) === '');
        // Codex takes no levels yet: the form says so and offers none
        await addFor('codex');
        check('the add form takes the keyboard on its provider box', (await focusId()) === 'cm-provider');
        check('Codex has no level boxes, and the form says why', (await ev(`document.querySelectorAll('#settings-custom-models .cm-form input[type=checkbox]').length`)) === 0 && /does not pass thinking levels to Codex yet/.test(await ev(`document.querySelector('#settings-custom-models .cm-form').textContent`)));
        const bad = [['', /Enter the model id/], ['-flag', /starting with a letter or number/], ['has space', /starting with a letter or number/], ['semi;colon', /starting with a letter or number/], ['x'.repeat(129), /up to 128 characters/]];
        for (const [v, re] of bad) {
            await ev(`(() => { const i = document.getElementById('cm-id'); i.value = ${JSON.stringify(v)}; document.querySelector('#settings-custom-models .cm-form').requestSubmit(); return true; })()`);
            const m = await ev(`(() => { const e = document.getElementById('cm-err'); return { shown: !e.hidden, text: e.textContent, invalid: document.getElementById('cm-id').getAttribute('aria-invalid'), role: e.getAttribute('role') }; })()`);
            check(`"${v.slice(0, 12)}" is refused with a reason, as an alert, and the box says it is invalid`, m.shown && re.test(m.text) && m.invalid === 'true' && m.role === 'alert', m.text);
        }
        await ev(`(() => { document.getElementById('cm-id').value = 'gpt-6-astra'; document.getElementById('cm-name').value = 'Astra'; document.querySelector('#settings-custom-models .cm-form').requestSubmit(); return true; })()`);
        t = await ev(`document.querySelector('#settings-custom-models .cm-row[data-provider="codex"]').innerText.replace(/\\s+/g, ' ')`);
        check(`a valid id is added as a Codex row (${t})`, /Codex gpt-6-astra Astra/.test(t));
        check('the keyboard lands on the new row\'s Edit button', (await focusId()) === 'codex:0');
        check('the page says the change is not saved yet', /Unsaved changes\. Apply to save\./.test(await ev(`document.querySelector('#settings-custom-models .cm-status').textContent`)));
        // a duplicate in the same provider is refused; the same id in another provider is fine
        await addFor('codex');
        await ev(`(() => { document.getElementById('cm-id').value = 'gpt-6-astra'; document.querySelector('#settings-custom-models .cm-form').requestSubmit(); return true; })()`);
        check('a duplicate in the same provider is refused', /already in the list for Codex/.test(await ev(`document.getElementById('cm-err').textContent`)));
        await ev(`document.getElementById('cm-id').dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
        check('Escape closes the form and not Settings', (await ev(`!document.querySelector('#settings-custom-models .cm-form') && getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`)) && (await focusId()) === '1');
        // Claude: levels are boxes from the server's limits; edit the seeded model
        await addFor('claude');
        check('Claude offers the five levels as boxes', (await ev(`[...document.querySelectorAll('#settings-custom-models .cm-form input[type=checkbox]')].map(c => c.value).join()`)) === 'low,medium,high,xhigh,max');
        await ev(`(() => { document.getElementById('cm-id').value = 'claude-opus-5-5'; document.querySelectorAll('#settings-custom-models .cm-form input[type=checkbox]').forEach(c => { if (c.value === 'medium' || c.value === 'max') c.checked = true; }); document.querySelector('#settings-custom-models .cm-form').requestSubmit(); return true; })()`);
        await click('[data-cm-edit="claude:0"]');
        await ev(`(() => { document.getElementById('cm-name').value = 'Global Opus'; document.querySelector('#settings-custom-models .cm-form').requestSubmit(); return true; })()`);
        await click('#settings-custom-models .cm-row[data-provider="pi"] button[aria-label^="Remove"]');
        check('removing a row puts the keyboard on the Add button', (await focusId()) === '1');
        const pay = await ev(`import(${S} + 'custom_models.js').then(m => m.customModelsPayload())`);
        const by = f => pay.models.filter(m => m.family === f);
        check(`the draft is claude 2, codex 1, agy 1, pi 0 (${JSON.stringify(pay).slice(0, 160)})`, by('claude').length === 2 && by('claude')[0].display_name === 'Global Opus' && by('claude')[1].efforts.join() === 'medium,max' && by('codex').length === 1 && by('agy').length === 1 && by('pi').length === 0);
        check('nothing was sent before Apply', (await ev(`window.__cm.puts.length`)) === 0);

        await ev(`import(${S} + 'custom_models.js').then(m => m.initCustomModels()).then(() => true)`);
        check('a refill of the dialog (the same open) keeps what the person has edited', (await ev(`import(${S} + 'custom_models.js').then(m => m.customModelsPayload().models.length)`)) === 4);
        console.log('2. Apply: one PUT with the whole list, and every picker forgets its list');
        await ev(`window.__cm.fail = true; import(${S} + 'settings_dialog.js').then(m => m.applySettings()).then(() => true)`);
        await waitFor(`[...document.querySelectorAll('.toast-message')].some(e => /custom model 1 \\(codex/.test(e.textContent))`, 'the toast naming the refused row', 8000);
        check('a refusal is shown with the server\'s words (the row and the field), the draft stays, nothing is marked saved', (await ev(`import(${S} + 'custom_models.js').then(m => m.customModelsDirty())`)) === true && /Unsaved changes/.test(await ev(`document.querySelector('#settings-custom-models .cm-status').textContent`)));
        await ev(`window.__cm.fail = false; window.__cm.puts.length = 0; true`);
        await ev(`localStorage.setItem('coral-models:v1:local:claude', JSON.stringify({ models: [{ value: 'old' }] })); true`);
        await ev(`import(${S} + 'settings_dialog.js').then(m => m.applySettings()).then(() => true)`);
        await waitFor(`window.__cm.puts.length >= 1`, 'the PUT', 8000);
        const puts = await ev(`window.__cm.puts`);
        check(`Apply sends exactly one PUT with the whole list, each row with its family (${puts.length})`, puts.length === 1 && Array.isArray(puts[0].models) && puts[0].models.length === 4 && puts[0].models.map(m => m.family).join() === 'claude,claude,codex,agy' && puts[0].models[1].value === 'claude-opus-5-5');
        check('the browser forgot its cached model lists', (await ev(`Object.keys(localStorage).filter(k => k.startsWith('coral-models:v1:')).length`)) === 0);

        // a refill whose read is still in flight when the person edits: the edit must survive the answer (#5480: dirty was decided before the read)
        {
            const count = () => ev(`import(${S} + 'custom_models.js').then(m => m.customModelsPayload().models.length)`);
            const before = await count();
            await ev(`window.__cm.hold = new Promise(r => { window.__cm.release = r; }); window.__cm.refill = import(${S} + 'custom_models.js').then(m => m.initCustomModels()); true`);
            await click('#settings-custom-models .cm-row[data-provider="claude"] button[aria-label^="Remove"]');
            const during = await count();
            await ev(`window.__cm.release(); window.__cm.hold = null; window.__cm.refill.then(() => true)`);
            check(`an edit made while the refill's read is in flight is kept (${before} rows, ${during} after the edit, ${await count()} after the answer)`, during === before - 1 && (await count()) === during);
        }

        console.log('3. no route on this server: the section is hidden');
        await open({ w: 1280, h: 900, phone: false }, 'light', false);
        await waitFor(`document.getElementById('settings-custom-models-row').hidden`, 'the row to hide', 5000);
        check('the row is hidden when GET /api/custom-models is a 404', true);

        console.log('4. both themes, desktop and phone');
        for (const [view, theme] of [[{ w: 1280, h: 900, phone: false }, 'dark'], [{ w: 390, h: 844, phone: true }, 'light'], [{ w: 390, h: 844, phone: true }, 'dark']]) {
            const tag = `${view.w}px ${theme}`;
            await open(view, theme);
            await addFor('claude');
            await ev(`document.getElementById('settings-custom-models').scrollIntoView({ block: 'center' }); true`);
            const g = await ev(`(() => { const h = document.getElementById('settings-custom-models'); const r = h.getBoundingClientRect(); const modal = document.querySelector('#settings-modal .modal-body, #settings-modal'); const small = [...h.querySelectorAll('button, input[type=checkbox]')].filter(e => e.getClientRects().length).map(e => { const b = e.closest('label') ? e.closest('label').getBoundingClientRect() : e.getBoundingClientRect(); return Math.round(Math.min(b.width, b.height)); }); return { over: h.scrollWidth - h.clientWidth, docOver: document.documentElement.scrollWidth - document.documentElement.clientWidth, minTarget: Math.min(...small), which: [...h.querySelectorAll('button, input[type=checkbox]')].filter(e => e.getClientRects().length).map(e => { const b = e.closest('label') ? e.closest('label').getBoundingClientRect() : e.getBoundingClientRect(); return e.tagName + ':' + (e.textContent || e.value) + ':' + Math.round(b.width) + 'x' + Math.round(b.height); }).join(' ') }; })()`);
            check(`${tag}: nothing wider than the section or the page (${g.over}, ${g.docOver})`, g.over <= 0 && g.docOver <= 0);
            if (view.phone) check(`${tag}: every button and level box is 44 px or more (${g.minTarget}; ${g.which})`, g.minTarget >= 44);
            if (process.env.CM_SHOT) require('node:fs').writeFileSync(`${process.env.CM_SHOT}/cm-${view.w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            const au = await auditRegion(ev, ['#settings-custom-models'], { focus: ['#cm-id'] });
            check(`${tag}: readable (4.5:1) and a focus ring (${au.checked} checked)`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await guard.restore(); await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); try { await guard.restore(); } catch (_) {} await client.close().catch(() => {}); process.exit(1); }
})().catch(e => { console.error(e); process.exitCode = 1; });
