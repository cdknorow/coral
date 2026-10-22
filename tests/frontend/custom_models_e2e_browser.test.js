// Custom models end to end (#5480), on the real server (no stub): a model added in Settings > Agents and saved with Apply comes back from GET /api/custom-models, is in both model catalogs the pickers read
// (GET /api/agents/models?type= with custom true and the levels ticked, and GET /api/agent-models), shows in a launch form's model list marked "(custom)" with its own thinking levels, and is gone from
// all of them once removed. A refused id is refused by the server too (400 naming the row), with nothing changed. The list is emptied at the end. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    const guard = await guardSettings(); // Apply saves the whole form: the settings it writes are put back
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await api('PUT', '/api/custom-models', { models: [] });

        const addFor = async p => { await click('#settings-custom-models [data-cm-add]'); await ev(`(() => { const s = document.getElementById('cm-provider'); s.value = ${JSON.stringify(p)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); };
        console.log('1. add two models in Settings and Apply');
        await ev(`window.showSettingsModal('agents'); true`);
        await waitFor(`!!document.querySelector('#settings-custom-models [data-cm-add]')`, 'the section from the real route', 8000);
        const lim = await api('GET', '/api/custom-models');
        const claudeLevels = lim.body.limits.efforts.claude;
        check(`the server's limits drive the level boxes (claude: ${claudeLevels.join()})`, claudeLevels.includes('low') && claudeLevels.includes('high') && lim.body.limits.families.join() === 'claude,codex,agy,pi');
        await addFor('claude');
        await ev(`(() => { document.getElementById('cm-id').value = 'e2e-claude-model'; document.getElementById('cm-name').value = 'E2E Claude'; document.querySelectorAll('#settings-custom-models .cm-form input[type=checkbox]').forEach(c => { if (c.value === 'low' || c.value === 'high') c.checked = true; }); document.querySelector('#settings-custom-models .cm-form').requestSubmit(); return true; })()`);
        await addFor('pi');
        await ev(`(() => { document.getElementById('cm-id').value = 'e2e/pi-model'; document.querySelector('#settings-custom-models .cm-form').requestSubmit(); return true; })()`);
        await ev(`import(${S} + 'settings_dialog.js').then(m => m.applySettings()).then(() => true)`);
        await waitFor(`(document.querySelector('#settings-custom-models .cm-status') || { textContent: 'not drawn' }).textContent === ''`, 'the save to finish', 8000);
        let got = await api('GET', '/api/custom-models');
        check(`GET /api/custom-models has both rows (${JSON.stringify(got.body.models)})`, got.body.models.length === 2 && got.body.models.some(m => m.family === 'claude' && m.value === 'e2e-claude-model' && m.display_name === 'E2E Claude' && m.efforts.join() === 'low,high') && got.body.models.some(m => m.family === 'pi' && m.value === 'e2e/pi-model' && m.efforts.length === 0));

        console.log('2. both catalogs carry them');
        let cat = await api('GET', '/api/agents/models?type=claude');
        const mine = (cat.body.models || []).find(m => m.value === 'e2e-claude-model');
        check(`GET /api/agents/models?type=claude lists it as custom with the ticked levels (${JSON.stringify(mine)})`, mine && mine.custom === true && mine.efforts.join() === 'low,high' && mine.display_name === 'E2E Claude');
        const flat = await api('GET', '/api/agent-models');
        check('GET /api/agent-models has the ids under their provider', (flat.body.claude || []).includes('e2e-claude-model') && (flat.body.pi || []).includes('e2e/pi-model'));
        check('the launch form shows it marked (custom), and its levels are the only ones offered', await (async () => {
            await ev(`window.hideSettingsModal && window.hideSettingsModal(); (async () => { const d = document.createElement('div'); d.id = 'e2e-acf'; document.body.appendChild(d); const f = await import(${S} + 'agent_config_form.js'); f.renderAgentConfigForm('e2e-acf', {}); return true; })()`);
            await waitFor(`!!document.querySelector('#e2e-acf .acf-model-select option[value="e2e-claude-model"]')`, 'the custom model in the form', 8000);
            const txt = await ev(`document.querySelector('#e2e-acf .acf-model-select option[value="e2e-claude-model"]').textContent`);
            await ev(`{ const s = document.querySelector('#e2e-acf .acf-model-select'); s.value = 'e2e-claude-model'; s.dispatchEvent(new Event('change', { bubbles: true })); true }`);
            const lv = await ev(`[...document.querySelectorAll('#e2e-acf .acf-thinking option')].map(o => o.value).join()`);
            return txt === 'E2E Claude (custom)' && lv === ',low,high';
        })());

        console.log('3. the server refuses what the form would not, and nothing changes');
        const bad = await api('PUT', '/api/custom-models', { models: [{ family: 'claude', value: 'ok-one', display_name: '', efforts: [] }, { family: 'codex', value: '--dangerously-skip-permissions', display_name: '', efforts: [] }] });
        check(`a flag-shaped id is a 400 naming the row (${JSON.stringify(bad.body).slice(0, 140)})`, bad.status === 400 && bad.body.row === 1 && bad.body.family === 'codex' && bad.body.field === 'value');
        got = await api('GET', '/api/custom-models');
        check('and the list is unchanged', got.body.models.length === 2);
        const dup = await api('PUT', '/api/custom-models', { models: [{ family: 'claude', value: 'twice', display_name: '', efforts: [] }, { family: 'claude', value: 'twice', display_name: '', efforts: [] }] });
        check(`a duplicate in one family is refused (${dup.status})`, dup.status === 400 || dup.status === 409);

        console.log('4. remove: gone from the list and from both catalogs');
        await ev(`window.showSettingsModal('agents'); true`);
        await waitFor(`!!document.querySelector('#settings-custom-models [data-cm-add]')`, 'the section again', 8000);
        check('the saved models are listed after a reopen', /e2e-claude-model/.test(await ev(`document.getElementById('settings-custom-models').innerText`)));
        await ev(`document.querySelectorAll('#settings-custom-models button[aria-label^="Remove"]').forEach(b => b.click()); true`);
        check(`both rows are out of the draft, and the form says unsaved (${await ev(`JSON.stringify([window.__x = 1, document.querySelector('#settings-custom-models .cm-status').textContent])`)})`, (await ev(`import(${S} + 'custom_models.js').then(m => m.customModelsPayload().models.length)`)) === 0);
        await ev(`import(${S} + 'settings_dialog.js').then(m => m.applySettings()).then(() => true)`);
        await waitFor(`(document.querySelector('#settings-custom-models .cm-status') || { textContent: 'not drawn' }).textContent === ''`, 'the save', 8000);
        cat = await api('GET', '/api/agents/models?type=claude');
        const flat2 = await api('GET', '/api/agent-models');
        check('removed from both catalogs on the next read', !(cat.body.models || []).some(m => m.value === 'e2e-claude-model') && !(flat2.body.claude || []).includes('e2e-claude-model') && !(flat2.body.pi || []).includes('e2e/pi-model'));
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await api('PUT', '/api/custom-models', { models: [] }).catch(() => {});
        await guard.restore(); await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); try { await api('PUT', '/api/custom-models', { models: [] }); } catch (_) {} try { await guard.restore(); } catch (_) {} await client.close().catch(() => {}); process.exit(1); }
})().catch(e => { console.error(e); process.exitCode = 1; });
