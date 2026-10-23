// The agent header at 200% text on a phone (#4327, #4276 finding 4): the identity row (Chat/Terminal toggle, dot, name, branch, buttons) used to share
// one row that could not wrap, so the name shrank to "al…". Now the name stays whole (the header wraps), and the small texts the audit found at fixed
// pixel sizes (the team, the branch, "Load older messages") grow with the root font. SHOT_DIR=<dir> saves the phone screenshots.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { settle } = require('./helpers/wait');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const ok = name => console.log(`   ✓ ${name}`);
    const shot = async n => { if (process.env.SHOT_DIR) fs.writeFileSync(`${process.env.SHOT_DIR}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const NAME = 'alpha-1';
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add(${JSON.stringify(NAME)}, 'A', 60, { sleeping: false, session_id: 'h200-sid', board_project: 'coral-go-team' }); true`);
        await settle(ev, 600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession(${JSON.stringify(NAME)}, 'claude', 'h200-sid', 'local'); true`);
        await waitFor(`!!document.getElementById('terminal-header-label') && document.getElementById('terminal-header-label').textContent.length > 0`, 'the header', 8000);
        await ev(`T.render.updateSessionBranch('fix-4327-header-wrap', 'coral', {}); true`); // a realistic branch beside the name, as the audit's agent had
        await settle(ev, 500); // sleep-ok: layout settles after the selection
        const read = () => ev(`(() => { const l = document.getElementById('terminal-header-label'); const lr = l.getBoundingClientRect(); const h = l.closest('.terminal-header'); const hr = h.getBoundingClientRect();
            const fs = sel => { const e = document.querySelector(sel); return e ? parseFloat(getComputedStyle(e).fontSize) : null; };
            const kids = [...h.children].map(c => Math.round(c.getBoundingClientRect().bottom - hr.top));
            const br = document.querySelector('.terminal-branch'); const branchShown = !!br && !br.hidden && getComputedStyle(br).display !== 'none' && br.getBoundingClientRect().width > 0;
            return { branchShown, kidsBottom: Math.max(...kids), text: l.textContent, whole: l.scrollWidth <= l.clientWidth + 1, w: Math.round(lr.width), headerH: Math.round(hr.height), over: document.documentElement.scrollWidth - innerWidth,
                team: fs('.terminal-context-team'), branch: fs('.terminal-branch-text'), more: fs('.load-more-btn'), root: parseFloat(getComputedStyle(document.documentElement).fontSize) }; })()`);

        const at100 = await read();
        await shot('header-phone-100');
        await ev(`document.documentElement.style.fontSize = '32px'; true`); await settle(ev, 500); // sleep-ok: layout settles after the font-size change
        const at200 = await read();
        await shot('header-phone-200');
        assert.equal(at100.text, NAME); assert.ok(at100.whole, 'the name is whole at 100%: ' + JSON.stringify(at100));
        assert.ok(at200.kidsBottom <= at200.headerH + 1, `at 200% the header grows to hold its rows (rows end at ${at200.kidsBottom}, header ${at200.headerH})`);
        assert.equal(at200.text, NAME);
        assert.ok(at200.whole, `at 200% the name is whole, not cut to "al…" (the label is ${at200.w}px wide, scrollWidth vs clientWidth: ${JSON.stringify(at200)})`);
        assert.ok(at200.over <= 0, 'no sideways scroll at 200%');
        assert.equal(at200.branchShown, false, 'at 200% text the branch is left out of the header: the name keeps its room');
        ok('at 390 px and 200% text the agent name is whole (header wraps), no sideways scroll');
        for (const k of ['team', 'branch']) if (at100[k] && at200[k]) assert.ok(at200[k] / at100[k] >= 1.9, `${k} text grows with the root font (${at100[k]} px to ${at200[k]} px)`);
        const more = await ev(`(() => { const e = document.querySelector('.load-more-btn'); return e ? parseFloat(getComputedStyle(e).fontSize) : null; })()`);
        if (more) assert.ok(more >= 20, `"Load older messages" grows with the root font (${more} px at 200%)`);
        ok('the team, the branch and the load-more texts grow with the root font');

        // The narrowest phones at normal text: the header stays one row (nothing wraps until the text is larger).
        await ev(`window.__m.add('phone-bot-with-a-long-name', 'B', 3, { sleeping: false, session_id: 'h200-long' }); true`); await settle(ev, 600); // sleep-ok: the list renders from several answers
        await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'h200-long', 'local'); true`); await settle(ev, 800); // sleep-ok: the header fills after the selection
        await ev(`T.render.updateSessionBranch('fix-4327-header-wrap', 'coral', {}); true`); await settle(ev, 300); // sleep-ok: the header redraws after the branch is set
        await ev(`document.documentElement.style.fontSize = ''; true`);
        // UX's measurement (#4327 review): the header wrapped to TWO rows from 350 to 365 px when the branch showed. A phone's header must be the same one
        // row at every width, with the branch only where it fits (370 px and up), and a long branch the way UX set it.
        await ev(`T.render.updateSessionBranch('feature/header-wrap-branch', 'coral', {}); true`); await settle(ev, 300); // sleep-ok: the header redraws after the branch is set
        const oneRow = async w => { await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true }); await settle(ev, 400); return read(); }; // sleep-ok: layout settles after the emulation change
        const rowH = (await oneRow(320)).headerH; // the branch is left out here: the header's one-row height
        for (const w of [320, 330, 340, 350, 355, 360, 365, 370, 375, 390, 414]) {
            const r = await oneRow(w);
            assert.ok(r.headerH <= rowH + 1 && r.kidsBottom <= r.headerH + 1, `${w} px at 100% text: one header row (rows end at ${r.kidsBottom}, header ${r.headerH}, one row is ${rowH}) with the name ${r.w}px wide`);
            assert.ok(r.w >= (w <= 340 ? 100 : 48), `${w} px at 100% text: the name has room (${r.w}px)`);
            assert.equal(await ev(`getComputedStyle(document.getElementById('terminal-open-window-link')).display`), 'none', `${w} px: Open Agent Tab is not in a phone's header (a pop-out tab is of little use there)`);
            assert.equal(r.branchShown, w >= 370, `${w} px at 100% text: the branch ${w >= 370 ? 'stays in' : 'is left out of'} the header`);
            assert.ok(r.over <= 0, `${w} px: no sideways scroll`);
        }
        ok('from 320 to 414 px with normal text the header is one row, the name has room, and the branch shows from 370 px up (the container is the content box of the header, 12 px under the viewport)');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
