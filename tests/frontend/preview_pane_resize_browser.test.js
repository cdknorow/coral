// The artifact Preview pane docked as a column between Chat and the right panel (#4931, an operator report: it got stuck narrow and could not be expanded; and it could not take the whole centre).
//  - the cause: the drag's upper limit took the side panel's width off the room twice (the side panel is outside .live-body), so on any ordinary window the limit fell to the 240 px minimum and the
//    first drag snapped the pane to 240 and held it there. Now the limit is .live-body's room less what Chat keeps;
//  - a saved width that is 0, text, negative, huge, or from a bigger window gives a usable pane (>= 240 px, inside the centre, Chat keeps >= 240 px), and the wish is kept: the window growing
//    back brings the saved width back;
//  - dragging the handle with the mouse resizes it both ways and saves the width; the handle is a separator the keyboard can move (Left/Right, Home/End);
//  - Maximise (a 44 px toggle in the pane's header) makes the pane take the whole centre, Chat hidden behind it; Restore, and Escape, bring back the old width; it ends when another agent is shown;
//    nothing is saved: a reload opens with Chat. On a phone there is no toggle (the pane is full screen there) and nothing breaks.
// Real isolated server, a mock agent, real mouse and key events through CDP. Drives Chrome through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { framesJs } = require('./helpers/wait');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }; // CORAL_SCREENSHOT_DIR=<dir> saves screenshots
    const geo = () => ev(`(() => { const r = e => e ? e.getBoundingClientRect() : null, lb = document.querySelector('.live-body'), col = document.getElementById('preview-column'), left = document.querySelector('.live-left-column');
        const c = r(col), l = r(left), b = r(lb), h = document.getElementById('preview-column-resize-handle');
        return { body: Math.round(b.width), bodyLeft: Math.round(b.left), col: Math.round(c.width), colLeft: Math.round(c.left), colRight: Math.round(c.right), chat: Math.round(l.width), chatShown: getComputedStyle(left).display !== 'none',
            handleX: Math.round(r(h).left + r(h).width / 2), handleY: Math.round(r(h).top + r(h).height / 2), saved: localStorage.getItem('coral-preview-column-width'), max: h.getAttribute('aria-valuemax'), now: h.getAttribute('aria-valuenow') }; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (w, h, saved) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: w < 700 });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await ev(`localStorage.clear(); localStorage.setItem('coral-preview-pane-dock', 'column'); ${saved === undefined ? '' : `localStorage.setItem('coral-preview-column-width', ${JSON.stringify(String(saved))});`} true`);
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); const { showView } = await import(st + 'utils.js');
                { const real = window.fetch; window.fetch = (u, o) => /\\/api\\/artifacts\\/a{64}/.test(String(u)) ? Promise.resolve(new Response('# Release notes\\n\\nThe preview shows a **markdown** artifact: a heading, a paragraph and a list.\\n\\n- one\\n- two\\n- three\\n', { status: 200, headers: { 'content-type': 'text/markdown' } })) : real(u, o); } // the artifact the pane shows
                window.__m.add('pv-a', 'P', 4, { sleeping: false, session_id: 'pv-a-id' });
                state.currentSession = { type: 'live', name: 'pv-a', session_id: 'pv-a-id', agent_type: 'claude' }; showView('live-session-view');
                window.PP.setPreviewSession('pv-a-id'); // the agent's own tabs, as selecting it does
                window.PP.openArtifactTab('coral://artifacts/' + 'a'.repeat(64), { name: 'a.md', mediaType: 'text/markdown' }); return true; })()`);
            await waitFor(w < 700 ? `getComputedStyle(document.getElementById('preview-pane')).display !== 'none'` : `getComputedStyle(document.getElementById('preview-column')).display !== 'none'`, w < 700 ? 'the preview pane' : 'the preview column', 8000);
        };
        const mouse = async (type, x, y) => Input.dispatchMouseEvent({ type, x, y, button: 'left', buttons: type === 'mouseReleased' ? 0 : 1, clickCount: 1 });
        const drag = async (dx) => { const g = await geo(); await mouse('mousePressed', g.handleX, g.handleY); await mouse('mouseMoved', g.handleX + Math.round(dx / 2), g.handleY); await mouse('mouseMoved', g.handleX + dx, g.handleY); await mouse('mouseReleased', g.handleX + dx, g.handleY);
            await ev(framesJs(2)); return geo(); };
        const usable = g => g.col >= 240 && g.chat >= 240 && g.colLeft >= g.bodyLeft && g.colRight <= g.bodyLeft + g.body + 1;

        console.log('1. A saved width that is corrupted, or from a bigger window, still gives a usable pane');
        for (const [label, saved, w] of [['none saved', undefined, 1400], ['0', 0, 1400], ['text', 'abc', 1400], ['-5', -5, 1400], ['99999', 99999, 1400], ['1200 saved, window 1000', 1200, 1000], ['NaN', 'NaN', 1400]]) {
            await open(w, 900, saved);
            const g = await geo();
            check(`${label}: the pane is usable (width ${g.col}, Chat ${g.chat}, in the centre ${g.bodyLeft}..${g.bodyLeft + g.body})`, usable(g), JSON.stringify(g));
        }
        await open(1800, 900, 700);
        await shot('preview-column-saved-700-at-1800');
        let g = await geo();
        check('a saved 700 in a roomy window is 700', g.col === 700, String(g.col));
        await Emulation.setDeviceMetricsOverride({ width: 1500, height: 900, deviceScaleFactor: 1, mobile: false });
        await waitFor(`document.getElementById('preview-column').getBoundingClientRect().width < 700`, 'the pane to give room when the window shrinks', 4000);
        g = await geo();
        check('the window shrinking holds the pane to the room (and Chat keeps its minimum)', usable(g) && g.col < 700, JSON.stringify(g));
        await Emulation.setDeviceMetricsOverride({ width: 1800, height: 900, deviceScaleFactor: 1, mobile: false });
        await waitFor(`document.getElementById('preview-column').getBoundingClientRect().width === 700`, 'the saved width to come back', 4000);
        check('and growing it back brings the saved 700 back (the wish was kept)', true);

        console.log('2. Dragging the handle resizes the pane, both ways, within the room');
        await open(1800, 900);
        g = await geo(); const start = g.col;
        g = await drag(-120);
        check('dragging left 120 px widens the pane by about 120', Math.abs(g.col - (start + 120)) <= 6, `${start} -> ${g.col}`);
        check('and the width is saved', Number(g.saved) === g.col, `${g.saved} vs ${g.col}`);
        const wide = g.col;
        g = await drag(200);
        check('dragging right 200 px narrows it by about 200', Math.abs(g.col - (wide - 200)) <= 6 && g.col >= 240, `${wide} -> ${g.col}`);
        await open(1400, 900);
        g = await geo(); const s1 = g.col, max1 = g.body - 241;
        await shot('preview-column-default-at-1400-held-to-the-room');
        g = await drag(-600);
        await shot('preview-column-dragged-left-to-the-most-the-room-allows');
        check('on an ordinary window, dragging left does NOT collapse the pane to its minimum (the reported bug): it goes to the most the room allows', g.col >= s1 && Math.abs(g.col - max1) <= 3 && g.col > 240 && g.chat >= 238, `${s1} -> ${g.col} (room allows ${max1}), Chat ${g.chat}`);
        g = await drag(100);
        check('and it can be dragged back narrower', Math.abs(g.col - (max1 - 100)) <= 6, String(g.col));

        console.log('3. The handle by keyboard');
        await open(1800, 900);
        g = await geo(); const k0 = g.col;
        await ev(`document.getElementById('preview-column-resize-handle').focus(); true`);
        const key = async (k, shift) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: { ArrowLeft: 37, ArrowRight: 39, Home: 36, End: 35, Escape: 27 }[k], modifiers: shift ? 8 : 0 }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: { ArrowLeft: 37, ArrowRight: 39, Home: 36, End: 35, Escape: 27 }[k] }); await ev(framesJs(1)); return geo(); };
        g = await key('ArrowLeft'); check('Left widens the pane by a step', g.col === k0 + 24, `${k0} -> ${g.col}`);
        g = await key('ArrowRight'); check('Right narrows it', g.col === k0, String(g.col));
        g = await key('Home'); check('Home is the minimum', g.col === 240, String(g.col));
        g = await key('End'); check('End is the most the room allows', Math.abs(g.col - (g.body - 241)) <= 1, `${g.col} of ${g.body}`);
        check('the handle is a labelled vertical separator with a value', await ev(`(() => { const h = document.getElementById('preview-column-resize-handle'); return h.getAttribute('role') === 'separator' && h.getAttribute('aria-orientation') === 'vertical' && !!h.getAttribute('aria-label') && h.tabIndex === 0 && Number(h.getAttribute('aria-valuenow')) > 0; })()`));

        console.log('4. Maximise covers Chat; Restore and Escape bring it back');
        await open(1800, 900, 600);
        const btn = () => ev(`(() => { const b = document.getElementById('preview-max-btn'); const r = b.getBoundingClientRect(); return { shown: !b.hidden && getComputedStyle(b).display !== 'none', w: Math.round(r.width), h: Math.round(r.height), pressed: b.getAttribute('aria-pressed'), label: b.getAttribute('aria-label'), title: b.title }; })()`);
        let b = await btn();
        check('the toggle is in the pane\'s header, 44 px wide, labelled "Maximise ...", not pressed', b.shown && b.w >= 44 && b.pressed === 'false' && /^Maximise/.test(b.label), JSON.stringify(b));
        g = await geo(); const before = g.col;
        await ev(`document.getElementById('preview-max-btn').click(); true`);
        await waitFor(`document.querySelector('.live-body').classList.contains('preview-maximised')`, 'maximised');
        await shot('preview-maximised-over-chat');
        g = await geo(); b = await btn();
        check('maximised: Chat is hidden and the pane fills the whole centre', !g.chatShown && Math.abs(g.col - g.body) <= 2, JSON.stringify(g));
        check('and the toggle says Restore, pressed', b.pressed === 'true' && /^Restore/.test(b.label), JSON.stringify(b));
        await ev(`document.getElementById('preview-max-btn').click(); true`);
        await waitFor(`!document.querySelector('.live-body').classList.contains('preview-maximised')`, 'restored');
        await shot('preview-restored-beside-chat');
        g = await geo();
        check('Restore brings back Chat and the old width', g.chatShown && g.col === before && g.chat >= 240, `${before} -> ${g.col}, Chat ${g.chat}`);
        await ev(`document.getElementById('preview-max-btn').click(); true`);
        await waitFor(`document.querySelector('.live-body').classList.contains('preview-maximised')`, 'maximised again');
        await ev(`document.getElementById('preview-max-btn').focus(); true`);
        await key('Escape');
        await waitFor(`!document.querySelector('.live-body').classList.contains('preview-maximised')`, 'Escape to restore');
        g = await geo();
        check('Escape restores it, with the old width', g.chatShown && g.col === before, `${before} -> ${g.col}`);
        check('and the focus is on the toggle', await ev(`document.activeElement && document.activeElement.id === 'preview-max-btn'`));
        await ev(`document.getElementById('preview-max-btn').click(); true`);
        await waitFor(`document.querySelector('.live-body').classList.contains('preview-maximised')`, 'maximised before switching agent');
        // another agent that has a tab of its own (so the pane stays shown when it is switched to): its page opens with its Chat
        await ev(`(() => { window.PP.setPreviewSession('another-agent'); window.PP.openArtifactTab('coral://artifacts/' + 'b'.repeat(64), { name: 'b.md', mediaType: 'text/markdown' }); window.PP.setPreviewSession('pv-a-id'); return true; })()`);
        await ev(`document.getElementById('preview-max-btn').click(); true`);
        await waitFor(`document.querySelector('.live-body').classList.contains('preview-maximised')`, 'maximised on the first agent');
        await ev(`window.PP.setPreviewSession('another-agent'); true`);
        check('showing another agent that has its own tab ends it (its page opens with its Chat)', await ev(`!document.querySelector('.live-body').classList.contains('preview-maximised') && getComputedStyle(document.getElementById('preview-column')).display !== 'none'`));
        await ev(`window.PP.setPreviewSession('pv-a-id'); true`);
        await open(1800, 900, 600);
        check('nothing is saved: a reload opens with Chat', await ev(`!document.querySelector('.live-body').classList.contains('preview-maximised') && document.querySelector('.live-left-column') && getComputedStyle(document.querySelector('.live-left-column')).display !== 'none'`));

        console.log('5. A phone: no toggle, and nothing breaks');
        await open(390, 800, 500);
        const phone = await ev(`(() => { const b = document.getElementById('preview-max-btn'); const pane = document.getElementById('preview-pane'); const r = pane.getBoundingClientRect(); return { btn: !!b && (b.hidden || getComputedStyle(b).display === 'none'), pane: getComputedStyle(pane).display !== 'none' && r.width > 0, overflow: document.documentElement.scrollWidth <= window.innerWidth + 1 }; })()`);
        await shot('preview-phone-390');
        check('on a phone the toggle is not there, the pane is shown and the page does not scroll sideways', phone.btn && phone.pane && phone.overflow, JSON.stringify(phone));
    } catch (e) { console.error(e && e.stack || e); results.push(false); }
    finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
