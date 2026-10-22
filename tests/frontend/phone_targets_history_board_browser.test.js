// A History chat's tab bar and a board message's Save and Delete are 44 px targets on a phone and on touch (#6094, from the Hub phone walk: they became reachable there with #6115), and are
// unchanged with a mouse on a wide window. The elements are built with the app's own classes in the page, so the stylesheet is what is measured.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [name, w, h, touch] of [['phone', 390, 844, true], ['desktop', 1280, 800, false]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: touch }); await Emulation.setTouchEmulationEnabled({ enabled: touch });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
            const r = await ev(`(() => { const host = document.createElement('div'); host.style.cssText = 'position:fixed;left:0;top:60px;width:100%;z-index:99999;background:var(--bg-primary)'; host.innerHTML =
                '<div class="history-tabs"><button class="history-tab-btn">Summary</button><button class="history-tab-btn">Chat</button></div>' +
                '<div class="mb-message"><div class="mb-message-header"><span class="mb-agent-name">Operator</span><button class="mb-save-btn" aria-label="Save"><svg viewBox="0 0 16 16"><path d="M3 2h10v12l-5-3-5 3z"/></svg></button><button class="mb-delete-msg-btn" aria-label="Delete">&times;</button></div></div>';
                document.body.append(host); const m = s => { const b = host.querySelector(s).getBoundingClientRect(); return { w: Math.round(b.width), h: Math.round(b.height) }; };
                const out = { tab: m('.history-tab-btn'), save: m('.mb-save-btn'), del: m('.mb-delete-msg-btn') }; host.remove(); return out; })()`);
            for (const k of ['tab', 'save', 'del']) {
                if (touch) assert.ok(r[k].w >= 44 && r[k].h >= 44, `${name}: ${k} is a 44 px target ${JSON.stringify(r[k])}`);
                else assert.ok(r[k].h < 44 || k === 'tab' && r[k].h < 44, `${name}: ${k} is unchanged with a mouse ${JSON.stringify(r[k])}`);
            }
            console.log(`ok ${name} ${JSON.stringify(r)}`);
        }
        console.log('PASS the History tab bar and the board message buttons are 44 px on a phone');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
