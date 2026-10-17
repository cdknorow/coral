// The chat's step bars and "Load older messages" are 44 px tap targets on a phone or touch screen and keep their compact 32 px on a mouse screen
// (#4327, #4276 finding 5). Covers a plain steps group, a roll-up bar (#4264) and the load-more button; opened groups and the rolled-up members
// keep working. Run against an isolated dev server. SHOT_DIR=<dir> saves a phone screenshot.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

const call = (id, label) => [{ type: 'assistant', text: '', tool_uses: [{ name: 'Bash', tool_use_id: id, input_summary: label, description: label }] }, { type: 'tool_result', tool_use_id: id, tool_name: 'Bash', content: 'ok' }];
const NOTICE = "You have 2 unread messages on the message board. Run 'coral-board read' to see them.";
const turn = (n, label, reply) => [{ type: 'user', content: NOTICE }, ...call('c' + n, label), { type: 'assistant', text: reply }];
const fixture = [{ type: 'user', content: 'Watch' }, ...call('p1', 'List files'), { type: 'assistant', text: 'Found them, a reply that stays visible and ends the run of steps. '.repeat(6) + '\n\nSecond paragraph.' },
    ...turn(1, 'first', 'One.'), ...turn(2, 'second', 'Two.')];

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const ok = name => console.log(`   ✓ ${name}`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => { const b = document.createElement('div'); b.id = 'tt-box'; b.className = 'chat-messages chat-prose';
            b.style.cssText = 'position:fixed;left:0;top:0;width:100%;height:100%;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
            document.body.appendChild(b); const more = document.createElement('button'); more.type = 'button'; more.className = 'load-more-btn'; more.textContent = 'Load older messages'; b.appendChild(more);
            m.renderTranscript(${JSON.stringify(fixture)}, b, 'claude'); return true; })`);
        const sizes = () => ev(`(() => { const h = el => Math.round(el.getBoundingClientRect().height * 10) / 10; const q = s => [...document.querySelectorAll('#tt-box ' + s)].filter(e => e.checkVisibility({ checkVisibilityCSS: true }));
            return { group: q('.work-group > summary').map(h), rollup: q('.work-rollup > summary').map(h), more: q('.load-more-btn').map(h), coarse: matchMedia('(pointer: coarse)').matches, w: innerWidth }; })()`);

        let s = await sizes();
        assert.ok(s.group.length >= 1 && s.rollup.length >= 1 && s.more.length === 1, 'the fixture has a step bar, a roll-up bar and the load-more button: ' + JSON.stringify(s));
        assert.ok([...s.group, ...s.rollup, ...s.more].every(h => h >= 30 && h < 40), 'on a mouse screen they keep the compact height: ' + JSON.stringify(s));
        ok('with a mouse at 1280 px they keep their compact height (' + JSON.stringify(s) + ')');

        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await sleep(500); // sleep-ok: layout settles after the emulation change
        s = await sizes();
        if (process.env.SHOT_DIR) fs.writeFileSync(`${process.env.SHOT_DIR}/chat-targets-phone.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
        assert.ok([...s.group, ...s.rollup, ...s.more].every(h => h >= 44), 'on a phone every step bar and the load-more button is at least 44 px: ' + JSON.stringify(s));
        ok('on a phone (390 px, touch) the step bar, the roll-up bar and "Load older messages" are at least 44 px: ' + JSON.stringify(s));

        // they still work: a tap opens the roll-up bar and shows its groups
        await ev(`document.querySelector('#tt-box .work-rollup').open = true; true`);
        await waitFor(`document.querySelector('#tt-box .work-rollup + .work-group').checkVisibility({ checkVisibilityCSS: true })`, 'the roll-up to open');
        s = await sizes();
        assert.ok(s.group.length >= 3 && s.group.every(h => h >= 44), 'the groups inside an opened roll-up are 44 px too: ' + JSON.stringify(s));
        ok('the groups inside an opened roll-up bar are 44 px as well');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
