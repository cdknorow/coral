// Jump to latest in an agent's Chat view (#4511, operator request): a round down-arrow button above the chat input, shown once you
// read more than about a screen above the bottom and gone at the bottom; messages that arrive meanwhile are counted on it and
// announced by a polite live region that stays displayed; Enter or a click scrolls to the latest message and pins the view, so
// the next messages follow; focus goes to the chat input on a desktop and to the messages on a touch screen. It has a name, is
// reached with Tab, is 44 px on touch, white on --accent-dim (4.5:1 or more), never covers the input, and fits a 390 px phone
// at 100% and 200% text. Not in the Terminal view; another agent starts with no count. Drives Chrome over CDP through
// agent_view_harness.js (the agent's transcript is served by an in-page mock).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, Runtime, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const C = `document.getElementById('live-history-messages')`;
    const BTN = `document.querySelector('#chat-jump-latest button')`;
    const state = () => ev(`(() => { const c = ${C}, b = ${BTN}, r = document.getElementById('chat-jump-latest');
        const br = b.getBoundingClientRect(), dot = r.querySelector('.chat-jump-latest-dot');
        return { shown: !b.hidden && br.width > 0, label: b.getAttribute('aria-label'), dot: !dot.hidden && dot.getBoundingClientRect().width > 0, live: r.querySelector('.chat-jump-latest-status').textContent,
            region: { role: r.getAttribute('role'), live: r.getAttribute('aria-live'), displayed: getComputedStyle(r).display !== 'none' },
            dist: Math.round(c.scrollHeight - c.scrollTop - c.clientHeight), ch: c.clientHeight, st: Math.round(c.scrollTop), w: Math.round(br.width), h: Math.round(br.height) }; })()`);
    const scrollTo = (expr) => ev(`(() => { const c = ${C}; c.scrollTop = ${expr}; c.dispatchEvent(new Event('scroll')); return new Promise(r => setTimeout(() => r(true), 50)); })()`); // sleep-ok: the scroll event's handlers run
    const add = (n) => ev(`(async () => { const a = window.__m.agents['jl-id']; for (let i = 0; i < ${n}; i++) { const k = a.msgs.length; a.msgs.push(k % 2 ? { type: 'assistant', text: 'new reply ' + k } : { type: 'user', content: 'new note ' + k }); }
        await (await import(${S} + 'live_chat.js')).refreshLiveHistory(); return a.msgs.length; })()`);
    const enter = async () => {
        await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' });
        await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    };
    const views = [
        { name: 'desktop', width: 1280, height: 900, mobile: false, coarse: false, font: '' },
        { name: 'phone 390', width: 390, height: 844, mobile: true, coarse: true, font: '' },
        { name: 'phone 390, 200% text', width: 390, height: 844, mobile: true, coarse: true, font: '200%' },
    ];
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const v of views) {
            await Emulation.setDeviceMetricsOverride({ width: v.width, height: v.height, deviceScaleFactor: 1, mobile: v.mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: v.coarse, maxTouchPoints: v.coarse ? 5 : 1 });
            // Motion on, so the jump is the smooth scroll and its watch (the reduced-motion jump, at once, is checked at the end).
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }, ...(v.coarse ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [{ name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }])] });
            await Page.navigate({ url: BASE });
            await Page.loadEventFired();
            await install();
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); document.documentElement.style.fontSize = ${JSON.stringify(v.font)}; window.__m.add('jl', 'jl', 150, { sleeping: false }); window.__m.add('jl-other', 'o', 60, { sleeping: false }); true`);
            await ev(`window.selectLiveSession('jl', 'claude', 'jl-id', 'local'); true`);
            await waitFor(`${C}.querySelectorAll('.chat-message, .chat-bubble, [data-msg-index]').length > 20 && ${C}.scrollHeight > ${C}.clientHeight * 3`, `${v.name}: the chat`, 8000);
            await waitFor(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, `${v.name}: opened at the bottom`);

            console.log(`${v.name}: at the bottom, and a little above it: no button`);
            let s = await state();
            assert.equal(s.shown, false, 'hidden at the bottom');
            assert.deepEqual(s.region, { role: 'status', live: 'polite', displayed: true }, 'the live region is there and displayed');
            await scrollTo(`c.scrollHeight - c.clientHeight - Math.round(c.clientHeight / 3)`);
            assert.equal((await state()).shown, false, 'within a screen of the bottom, still hidden');

            console.log(`${v.name}: more than a screen up: shown, named, reachable`);
            await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
            s = await state();
            assert.equal(s.shown, true, `shown more than a screen up (${JSON.stringify(s)})`);
            assert.equal(s.label, 'Jump to latest');
            assert.equal(s.dot, false, 'no dot with nothing new');
            const geo = await ev(`(() => { const b = ${BTN}.getBoundingClientRect(), i = document.getElementById('command-pane').getBoundingClientRect(), vw = document.documentElement.clientWidth;
                const lum = c => { const [r, g, b2] = c.match(/[\\d.]+/g).slice(0, 3).map(Number).map(x => { x /= 255; return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4; }); return 0.2126 * r + 0.7152 * g + 0.0722 * b2; };
                const circle = getComputedStyle(${BTN}, ${v.coarse ? "'::before'" : 'null'}); const a = lum(getComputedStyle(${BTN}).color), bg = lum(circle.backgroundColor);
                const cw = ${v.coarse ? `parseFloat(circle.width) || (${BTN}.getBoundingClientRect().width - 8)` : `${BTN}.getBoundingClientRect().width`};
                const m = document.getElementById('live-history-messages').getBoundingClientRect();
                return { aboveInput: b.bottom <= i.top + 0.5, inside: b.left >= 0 && b.right <= vw, centre: Math.abs((b.left + b.right) / 2 - (m.left + m.right) / 2), contrast: (Math.max(a, bg) + 0.05) / (Math.min(a, bg) + 0.05), circle: Math.round(cw),
                    opacity: Number(getComputedStyle(${BTN}).opacity), arrowAlpha: Number((getComputedStyle(${BTN}).color.split(',')[3] || '1').replace(')', '')), bgColor: circle.backgroundColor }; })()`);
            assert.ok(geo.aboveInput, `${v.name}: above the input, not over it`);
            assert.ok(geo.inside, `${v.name}: on the screen`);
            assert.ok(geo.centre <= 1, `${v.name}: centred over the chat, clear of the replies' Copy and Save buttons at the right (off by ${geo.centre} px)`);
            assert.ok(geo.contrast >= 4.5, `${v.name}: the white arrow on the circle, contrast ${geo.contrast.toFixed(2)}:1`);
            assert.equal(geo.bgColor, 'rgb(0, 0, 0)', `${v.name}: a black circle (dark theme)`);
            assert.ok(geo.circle >= 32 && geo.circle <= 36, `${v.name}: a 32-36 px circle (${geo.circle})`);
            assert.ok(geo.arrowAlpha < 1 && geo.opacity === 1, `${v.name}: the arrow a little faded at rest, the circle opaque (arrow alpha ${geo.arrowAlpha}, opacity ${geo.opacity})`);
            if (v.coarse) assert.ok(s.w >= 44 && s.h >= 44, `${v.name}: 44 px on touch (${s.w}x${s.h})`);
            if (!v.coarse) {
                // The light theme: a dark grey circle, still a white arrow.
                const light = await ev(`(() => { const r = document.documentElement, was = r.getAttribute('data-theme'); r.setAttribute('data-theme', 'light');
                    const b = ${BTN}, out = { bg: getComputedStyle(b).backgroundColor, fg: getComputedStyle(b).color }; if (was) r.setAttribute('data-theme', was); else r.removeAttribute('data-theme'); return out; })()`);
                assert.deepEqual(light, { bg: 'rgb(58, 58, 58)', fg: 'rgba(255, 255, 255, 0.78)' }, `${v.name}: light theme, a dark grey circle and a white arrow (faded at rest)`);
            }
            if (!v.coarse) {
                // Tab reaches it: from the control before it in the page, the next Tab stop is the button.
                const reach = await ev(`(() => { const b = ${BTN}; const all = [...document.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])')].filter(e => e.getClientRects().length && !e.closest('[hidden]'));
                    return all.indexOf(b) >= 0 && b.tabIndex >= 0; })()`);
                assert.ok(reach, `${v.name}: in the Tab order`);
            }

            console.log(`${v.name}: new messages while reading higher up are counted and announced; the view stays where it is`);
            const st0 = (await state()).st;
            await add(3);
            await waitFor(`${BTN}.getAttribute('aria-label') === 'Jump to latest, 3 new messages'`, `${v.name}: the count 3`);
            s = await state();
            assert.equal(s.live, '3 new messages');
            assert.equal(s.label, 'Jump to latest, 3 new messages');
            assert.equal(s.dot, true, 'a dot, and no number on the button');
            assert.ok(!/\d/.test(await ev(`${BTN}.textContent`)), 'no visible count text');
            assert.equal(s.st, st0, 'the view did not move');
            await add(1);
            await waitFor(`${BTN}.getAttribute('aria-label') === 'Jump to latest, 4 new messages'`, `${v.name}: the count 4`);

            console.log(`${v.name}: Enter jumps to the latest and pins the view`);
            await ev(`${BTN}.focus(); true`);
            await enter();
            await waitFor(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, `${v.name}: at the bottom after the jump`, 4000);
            await waitFor(`${BTN}.hidden`, `${v.name}: the button goes`);
            s = await state();
            assert.equal(s.dot, false);
            assert.equal(s.live, '', 'the count is cleared');
            const focus = await ev(`document.activeElement && (document.activeElement.id || document.activeElement.className)`);
            assert.equal(focus, v.coarse ? 'live-history-messages' : 'command-input', `${v.name}: focus after the jump`);
            await add(2);
            await waitFor(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, `${v.name}: the next messages follow (pinned)`);
            assert.equal((await state()).shown, false, 'still no button: pinned at the bottom');

            console.log(`${v.name}: a jump that stops short of the bottom (messages came in on the way) is finished and pinned`);
            // Headless Chrome ends a smooth scroll at once, so the scroll is made to stop 300 px short, as new messages make it.
            await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
            await ev(`(() => { const c = ${C}; c.scrollTo = o => { c.scrollTop = o.top - c.clientHeight - 300; }; return true; })()`);
            await ev(`${BTN}.focus(); true`);
            await enter();
            await waitFor(`(async () => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 2 && (await import(${S} + 'state.js')).state.autoScroll; })()`, `${v.name}: finished at the bottom, pinned`, 3000);
            await add(1);
            await waitFor(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, `${v.name}: pinned: the next one follows`);
            assert.equal((await state()).dot, false, 'nothing counted while pinned');

            console.log(`${v.name}: scrolling back up during a jump lets go of it`);
            await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
            // The jump starts down (100 px, as a smooth scroll's first step) and, before the next frame, you scroll 300 px back up.
            await ev(`(() => { const c = ${C}; c.scrollTo = () => { c.scrollTop += 100; }; ${BTN}.click(); c.scrollTop -= 300;
                return new Promise(r => setTimeout(() => r(true), 1300)); })()`); // sleep-ok: past the watch's second
            assert.ok(await ev(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight > c.clientHeight; })()`), `${v.name}: not pulled down after scrolling up`);
            await ev(`delete ${C}.scrollTo; true`);

            console.log(`${v.name}: scrolling to the bottom by hand clears the count`);
            await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
            await add(1);
            await waitFor(`${BTN}.getAttribute('aria-label') === 'Jump to latest, 1 new message'`, `${v.name}: the count 1`);
            assert.equal((await state()).live, '1 new message', 'singular');
            await scrollTo(`c.scrollHeight`);
            await waitFor(`${BTN}.hidden`, `${v.name}: gone at the bottom`);
            assert.equal((await state()).dot, false);

            if (!v.coarse) {
                console.log(`${v.name}: a click jumps too; another agent starts with no count; not in the Terminal view`);
                await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
                await add(2);
                await waitFor(`${BTN}.getAttribute('aria-label') === 'Jump to latest, 2 new messages'`, `${v.name}: the count 2`);
                await ev(`${BTN}.click(); true`);
                await waitFor(`${BTN}.hidden && (() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, `${v.name}: a click jumps`, 4000);
                await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
                await add(2);
                await waitFor(`${BTN}.getAttribute('aria-label') === 'Jump to latest, 2 new messages'`, `${v.name}: the count 2 again`);
                await ev(`window.selectLiveSession('jl-other', 'claude', 'jl-other-id', 'local'); true`);
                await waitFor(`${C}.textContent.includes('o59')`, `${v.name}: the other agent's chat`, 8000);
                s = await state();
                assert.equal(s.dot, false, 'another agent starts with no count');
                assert.equal(s.shown, false, 'and at its bottom, no button');
                await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
                await ev(`(async () => { (await import(${S} + 'live_chat.js')).setLiveViewMode('terminal'); return true; })()`);
                assert.equal(await ev(`getComputedStyle(document.getElementById('chat-jump-latest')).display`), 'none', 'not shown in the Terminal view');
                await ev(`(async () => { (await import(${S} + 'live_chat.js')).setLiveViewMode('chat'); return true; })()`);

                console.log(`${v.name}: with reduced motion the jump is at once`);
                await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'reduce' }, { name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }] });
                await waitFor(`${C}.scrollHeight > ${C}.clientHeight * 3`, `${v.name}: the other agent's chat is long enough`);
                await scrollTo(`c.scrollHeight - c.clientHeight * 2.5`);
                await ev(`(() => { const c = ${C}; c.scrollTo = () => { window.__jlSmooth = true; }; window.__jlSmooth = false; return true; })()`);
                await ev(`${BTN}.click(); true`);
                assert.ok(await ev(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 2 && !window.__jlSmooth; })()`), 'at the bottom straight away, no smooth scroll');
                await ev(`delete ${C}.scrollTo; true`);
            }
        }
        console.log('chat_jump_latest: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
