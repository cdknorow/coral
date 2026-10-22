// #6047: the existing analytics identity is copyable in Settings even with analytics off.
// Missing/error states cannot copy a stale ID; copying is local and changes no settings.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { swapTheme } = require('./helpers/theme');
const { framesJs } = require('./helpers/wait');
const fs = require('node:fs');
const path = require('node:path');
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const id = '11111111-2222-4333-8444-555555555555';
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await ev(`(() => {
            const original = window.fetch; window.__idMode='available'; window.__copiedIDs=[]; window.__idWrites=[];
            window.fetch = (url, options={}) => {
                if (options.method && options.method !== 'GET') window.__idWrites.push(String(url));
                if (String(url).endsWith('/api/system/telemetry')) return Promise.resolve(new Response(JSON.stringify({install_id_status:window.__idMode,install_id:window.__idMode==='available'?${JSON.stringify(id)}:''}),{status:window.__idMode==='error'?500:200,headers:{'Content-Type':'application/json'}}));
                return original(url,options);
            };
            Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.__copiedIDs.push(text)}}});

        })()`);
        await ev(`import((window.__coralStatic||'/static/')+'state.js').then(m=>{m.state.settings.telemetry_enabled=false})`);
        for (const [width,theme] of [[1280,'dark'],[1280,'light'],[390,'dark'],[390,'light']]) {
            await Emulation.setDeviceMetricsOverride({width,height:900,deviceScaleFactor:1,mobile:width===390});
            await ev(`window.__idMode='available';window.showSettingsModal('security')`);
            await swapTheme(ev, theme);
            assert.equal(await ev(`document.getElementById('settings-telemetry-enabled').checked`), false);
            assert.equal(await ev(`document.getElementById('settings-telemetry-install-id').textContent`), id);
            await ev(`document.getElementById('settings-telemetry-install-copy').click();true`);
            await waitFor(`document.getElementById('settings-telemetry-install-status').textContent==='Install ID copied.'`, 'copy feedback');
            assert.equal(await ev(`window.__copiedIDs.at(-1)`), id);
            assert.equal(await ev(`document.getElementById('settings-unsaved').hidden`),true,'copy does not mark Settings changed');
            assert.match(await ev(`document.getElementById('settings-telemetry-install-hint').textContent`),/deletion request/);
            assert.equal(await ev(`document.documentElement.scrollWidth>innerWidth`),false,'no page overflow');
            if (process.env.SHOT_DIR) {
                fs.mkdirSync(process.env.SHOT_DIR,{recursive:true});
                await ev(`document.getElementById('settings-telemetry-install-copy').scrollIntoView({block:'center'});${framesJs(2)}`);
                fs.writeFileSync(path.join(process.env.SHOT_DIR,`install-id-${width}-${theme}.png`),Buffer.from((await Page.captureScreenshot()).data,'base64'));
            }
            await ev(`window.hideSettingsModal();true`);
        }
        for (const mode of ['missing','error']) {
            await ev(`window.__idMode=${JSON.stringify(mode)};window.showSettingsModal('security')`);
            assert.equal(await ev(`document.getElementById('settings-telemetry-install-copy').disabled`),true);
            assert.notEqual(await ev(`document.getElementById('settings-telemetry-install-id').textContent`),id);
            await ev(`window.hideSettingsModal();true`);
        }
        assert.deepEqual(await ev(`window.__idWrites`),[],'read/copy never writes settings or analytics state');
        console.log('PASS install ID: available while off, exact copy, no settings writes, missing/error no stale copy, desktop/phone');
    } finally { await client.close(); }
})().catch(error=>{console.error(error);process.exit(1)});
