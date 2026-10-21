// The board view keeps a page gutter on both sides (#3607): the 'Message Board' heading doesn't
// touch the sidebar, the Delete button and the Subscribers column don't touch the window's right
// edge, and no subscriber label is cut off. Checked in both themes at 1400x900, 1024 wide and
// phone width (no horizontal scroll), on an empty board and with a long message and an attachment.
// At 800 wide (the 768-1023 band, a narrower Subscribers column) too. At phone width the column is
// hidden and a Subscribers button opens the same list as a sheet (#3610): reachable, labelled, not
// clipped, Tab stays inside, and Escape, the backdrop and opening a subscriber each close it, with
// focus back on the button. BOARD_SPACING_SHOTS=<dir> also saves each layout.
'use strict';

const CDP = require('chrome-remote-interface');
const { safePaused } = require('./helpers/paused');
const fs = require('node:fs');
const path = require('node:path');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const HEX = 'd'.repeat(64);

const VIEWS = [
    { name: '1400x900', width: 1400, height: 900, mobile: false, gutter: 24 },
    { name: '1024x768', width: 1024, height: 768, mobile: false, gutter: 24 },
    { name: '800x1000', width: 800, height: 1000, mobile: false, gutter: 24 },
    { name: 'phone', width: 390, height: 844, mobile: true, gutter: 16 },
];

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Fetch, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what) => waitFor(expr, what, { timeout: 8000, soft: true });
    const settle = () => ev(`new Promise(r => setTimeout(() => r(true), 300))`); // sleep-ok: layout after a resize or theme change
    const api = (method, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' },
        body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    const board = 'spacing-qa-3607';
    const shots = process.env.BOARD_SPACING_SHOTS || '';
    let png = '';
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        // The attachment: an image artifact wider than any window, served by the test.
        png = await ev(`(() => { const c = document.createElement('canvas'); c.width = 1800; c.height = 240;
            const g = c.getContext('2d'); g.fillStyle = '#3a7bd5'; g.fillRect(0, 0, 1800, 240); g.fillStyle = '#fff'; g.fillRect(40, 40, 1720, 160);
            return c.toDataURL('image/png').split(',')[1]; })()`);
        await Fetch.enable({ patterns: [{ urlPattern: `*/api/artifacts/${HEX}*` }] });
        safePaused(client, ({ requestId }) => Fetch.fulfillRequest({ requestId, responseCode: 200,
            responseHeaders: [{ name: 'Content-Type', value: 'image/png' }], body: png }).catch(() => {}));

        // The Operator is one of the rows: the page subscribes it itself when a board opens, but the list only shows it once its next refresh runs, which in some runs is after the wait below gave up (8 s). Subscribed here, the list has its three rows from the first render.
        for (const [id, title] of [['Operator', 'Operator'], ['member-1', 'Developer member-1'], ['a-very-long-subscriber-identifier-0123456789abcdef', 'Senior Platform Reliability Developer With A Long Title']]) {
            await api('POST', `/api/board/${board}/subscribe`, { subscriber_id: id, job_title: title });
        }
        const open = async () => {
            const l = Page.loadEventFired();
            await Page.navigate({ url: `${BASE}/board/${board}` });
            await l;
            await coralReady();
            await until(`document.querySelectorAll('#mb-subscribers-list li .subscriber-history-link').length >= 3 && getComputedStyle(document.getElementById('mb-delete-btn')).display !== 'none'`, 'the board, its subscribers and Delete');
        };

        const measure = gutter => ev(`(() => {
            const view = document.getElementById('messageboard-view').getBoundingClientRect();
            const h = document.getElementById('messageboard-title').getBoundingClientRect();
            const del = document.getElementById('mb-delete-btn').getBoundingClientRect();
            const panel = document.getElementById('mb-subscribers-panel');
            const shown = el => el && getComputedStyle(el).display !== 'none' && el.getBoundingClientRect().width > 0;
            const right = Math.min(innerWidth, document.documentElement.clientWidth, view.right);
            const pageRight = Math.min(innerWidth, document.documentElement.clientWidth); // the Subscribers list is a tab of the right sidebar (#4539): its edge is the page's
            const pr = shown(panel) ? panel.getBoundingClientRect() : null;
            const labels = [...document.querySelectorAll('#mb-subscribers-list li *')].filter(e => e.children.length === 0 && e.textContent.trim());
            const clipped = labels.filter(e => e.scrollWidth > e.clientWidth + 0.5 || (pr && e.getBoundingClientRect().right > pr.right + 0.5))
                .map(e => e.textContent.trim() + ' ' + e.scrollWidth + '>' + e.clientWidth);
            const msgs = document.getElementById('mb-messages');
            const overflowing = [...document.querySelectorAll('#messageboard-view *')].filter(e => shown(e) && e.getBoundingClientRect().right > right + 0.5)
                .slice(0, 3).map(e => (e.id || e.className || e.tagName) + '@' + Math.round(e.getBoundingClientRect().right));
            return {
                headingLeft: Math.round(h.left - view.left),
                deleteRight: Math.round(right - del.right),
                panelRight: pr ? Math.round(pageRight - pr.right) : null,
                panelText: pr ? Math.round(pageRight - Math.max(...labels.map(e => e.getBoundingClientRect().right))) : null,
                clipped, overflowing,
                pageScroll: document.documentElement.scrollWidth - document.documentElement.clientWidth,
                messagesScroll: msgs.scrollWidth - msgs.clientWidth,
            };
        })()`);

        const save = async name => {
            fs.mkdirSync(shots, { recursive: true });
            const shot = await Page.captureScreenshot({ format: 'png' });
            fs.writeFileSync(path.join(shots, name), Buffer.from(shot.data, 'base64'));
        };
        const key = async (k, code, vk, shift) => {
            for (const type of ['keyDown', 'keyUp']) await Input.dispatchKeyEvent({ type, key: k, code, windowsVirtualKeyCode: vk, modifiers: shift ? 8 : 0 });
        };
        const SHOWN = `(document.getElementById('mb-subscribers-sheet').style.display !== 'none' && document.getElementById('mb-subscribers-sheet').getClientRects().length > 0)`;
        const openSheet = async () => {
            await ev(`(() => { const b = document.getElementById('mb-subscribers-btn'); b.focus(); b.click(); return true; })()`);
            await until(`${SHOWN} && document.getElementById('mb-subscribers-sheet').contains(document.activeElement)`, 'the sheet open, with focus inside');
        };
        const closed = () => ev(`(() => { const b = document.getElementById('mb-subscribers-btn');
            return { shown: ${SHOWN}, focusOnButton: document.activeElement === b, expanded: b.getAttribute('aria-expanded') }; })()`);
        // The phone's Subscribers sheet (#3610). leave: also open a subscriber, which leaves the board.
        const sheet = async (tag, v, leave) => {
            const btn = await ev(`(() => { const b = document.getElementById('mb-subscribers-btn'); if (!b) return null; const r = b.getBoundingClientRect();
                return { display: getComputedStyle(b).display, right: Math.round(innerWidth - r.right), left: Math.round(r.left), clipped: b.scrollWidth > b.clientWidth + 0.5,
                    name: b.textContent.trim(), haspopup: b.getAttribute('aria-haspopup'), controls: b.getAttribute('aria-controls'), expanded: b.getAttribute('aria-expanded') }; })()`);
            check(`${tag}: a Subscribers button shows in the header, inside the gutter, not clipped, named and marked as opening a dialog`,
                !!btn && btn.display !== 'none' && btn.right >= v.gutter && btn.left >= v.gutter && !btn.clipped && btn.name === 'Subscribers'
                && btn.haspopup === 'dialog' && btn.controls === 'mb-subscribers-sheet' && btn.expanded === 'false', JSON.stringify(btn));
            if (!btn) return; // nothing to open
            await openSheet();
            const m = await ev(`(() => { const s = document.getElementById('mb-subscribers-sheet'), c = s.querySelector('.modal-content').getBoundingClientRect();
                const label = document.getElementById(s.getAttribute('aria-labelledby'));
                const labels = [...s.querySelectorAll('#mb-subscribers-sheet-list li *')].filter(e => e.children.length === 0 && e.textContent.trim());
                return { role: s.getAttribute('role'), modal: s.getAttribute('aria-modal'), label: label && label.textContent.trim(),
                    expanded: document.getElementById('mb-subscribers-btn').getAttribute('aria-expanded'),
                    rows: s.querySelectorAll('#mb-subscribers-sheet-list li .subscriber-history-link').length,
                    clipped: labels.filter(e => e.scrollWidth > e.clientWidth + 0.5).map(e => e.textContent.trim()),
                    outside: labels.filter(e => { const r = e.getBoundingClientRect(); return r.left < c.left || r.right > c.right + 0.5 || r.right > innerWidth - ${v.gutter} + 0.5; }).map(e => e.textContent.trim()),
                    sheet: [Math.round(c.left), Math.round(c.right), Math.round(c.bottom)], innerWidth, innerHeight,
                    pageScroll: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
            check(`${tag}: the sheet is a labelled modal dialog listing every subscriber, inside the window, none clipped`,
                m.role === 'dialog' && m.modal === 'true' && m.label === 'Subscribers' && m.expanded === 'true' && m.rows >= 3
                && !m.clipped.length && !m.outside.length && m.sheet[0] >= 0 && m.sheet[1] <= m.innerWidth && m.sheet[2] <= m.innerHeight + 1 && m.pageScroll <= 0, JSON.stringify(m));
            if (shots) await save(`board-spacing-sheet-${tag.replace(/ /g, '-')}.png`);
            const inside = [];
            for (let i = 0; i < 8; i++) {
                await key('Tab', 'Tab', 9, i % 3 === 2);
                inside.push(await ev(`document.getElementById('mb-subscribers-sheet').contains(document.activeElement)`));
            }
            check(`${tag}: Tab and Shift+Tab stay inside the sheet`, inside.every(Boolean), JSON.stringify(inside));
            await key('Escape', 'Escape', 27);
            await until(`!${SHOWN}`, 'Escape closes the sheet');
            let c = await closed();
            check(`${tag}: Escape closes it and focus goes back to the Subscribers button`, !c.shown && c.focusOnButton && c.expanded === 'false', JSON.stringify(c));
            await openSheet();
            await Input.dispatchMouseEvent({ type: 'mousePressed', x: Math.round(v.width / 2), y: 20, button: 'left', clickCount: 1 });
            await Input.dispatchMouseEvent({ type: 'mouseReleased', x: Math.round(v.width / 2), y: 20, button: 'left', clickCount: 1 });
            await until(`!${SHOWN}`, 'the backdrop closes the sheet');
            c = await closed();
            check(`${tag}: a tap on the backdrop closes it`, !c.shown && c.expanded === 'false', JSON.stringify(c));
            if (leave) {
                await openSheet();
                await ev(`document.querySelector('#mb-subscribers-sheet-list .subscriber-history-link').click(); true`);
                await until(`!${SHOWN}`, 'opening a subscriber closes the sheet');
                check(`${tag}: opening a subscriber's history closes the sheet`, !(await ev(SHOWN)), '');
            }
        };

        const runAll = async phase => {
            for (const v of VIEWS) {
                await Emulation.setDeviceMetricsOverride({ width: v.width, height: v.height, deviceScaleFactor: 1, mobile: v.mobile });
                await open();
                for (const theme of ['dark', 'light']) {
                    await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}'); return true; })()`);
                    await settle();
                    if (phase === 'messages') await until(`!!document.querySelector('#mb-messages img.board-artifact-image') && document.querySelector('#mb-messages img.board-artifact-image').complete`, 'the attachment');
                    const m = await measure(v.gutter);
                    const tag = `${phase} ${v.name} ${theme}`;
                    check(`${tag}: the heading's left gap is at least the page gutter (${v.gutter}px)`, m.headingLeft >= v.gutter, JSON.stringify(m.headingLeft));
                    check(`${tag}: Delete's right gap is at least the page gutter`, m.deleteRight >= v.gutter, JSON.stringify(m.deleteRight));
                    if (m.panelRight !== null) check(`${tag}: the Subscribers list's right gap (the sidebar is flush with the page edge) is at least the page gutter`, m.panelText >= v.gutter, JSON.stringify([m.panelRight, m.panelText]));
                    check(`${tag}: no subscriber label is clipped`, m.clipped.length === 0, JSON.stringify(m.clipped));
                    check(`${tag}: nothing runs past the right edge, and no horizontal scroll`, m.overflowing.length === 0 && m.pageScroll <= 0 && m.messagesScroll <= 0, JSON.stringify(m));
                    if (v.name !== 'phone') {
                        const b = await ev(`(b => b ? getComputedStyle(b).display : 'none')(document.getElementById('mb-subscribers-btn'))`);
                        check(`${tag}: the column shows, so the Subscribers button doesn't`, b === 'none', b);
                    }
                    if (shots) await save(`board-spacing-${phase}-${v.name}-${theme}.png`);
                    if (v.name === 'phone') await sheet(tag, v, phase === 'messages' && theme === 'light');
                }
            }
        };

        // The report's case first: an empty board.
        await runAll('empty');
        const long = 'A long message that has to wrap inside the board column instead of pushing the Subscribers column off the page. '.repeat(6)
            + ' https://example.test/' + 'x'.repeat(220) + ' and an unbroken word ' + 'y'.repeat(160) + '.';
        await api('POST', `/api/board/${board}/messages`, { subscriber_id: 'member-1', content: long });
        await api('POST', `/api/board/${board}/messages`, { subscriber_id: 'member-1', content: `The screenshot: ![wide shot](coral://artifacts/${HEX})` });
        await runAll('messages');
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await api('DELETE', `/api/board/${board}`).catch(() => {});
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
