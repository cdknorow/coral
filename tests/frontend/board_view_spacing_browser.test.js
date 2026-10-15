// The board view keeps a page gutter on both sides (#3607): the 'Message Board' heading doesn't
// touch the sidebar, the Delete button and the Subscribers column don't touch the window's right
// edge, and no subscriber label is cut off. Checked in both themes at 1400x900, 1024 wide and
// phone width (no horizontal scroll), on an empty board and with a long message and an attachment.
// BOARD_SPACING_SHOTS=<dir> also saves each layout.
'use strict';

const CDP = require('chrome-remote-interface');
const fs = require('node:fs');
const path = require('node:path');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const HEX = 'd'.repeat(64);

const VIEWS = [
    { name: '1400x900', width: 1400, height: 900, mobile: false, gutter: 24 },
    { name: '1024x768', width: 1024, height: 768, mobile: false, gutter: 24 },
    { name: 'phone', width: 390, height: 844, mobile: true, gutter: 16 },
];

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Fetch } = client;
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
        Fetch.requestPaused(({ requestId }) => Fetch.fulfillRequest({ requestId, responseCode: 200,
            responseHeaders: [{ name: 'Content-Type', value: 'image/png' }], body: png }).catch(() => {}));

        for (const [id, title] of [['member-1', 'Developer member-1'], ['a-very-long-subscriber-identifier-0123456789abcdef', 'Senior Platform Reliability Developer With A Long Title']]) {
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
                panelRight: pr ? Math.round(right - pr.right) : null,
                panelText: pr ? Math.round(right - Math.max(...labels.map(e => e.getBoundingClientRect().right))) : null,
                clipped, overflowing,
                pageScroll: document.documentElement.scrollWidth - document.documentElement.clientWidth,
                messagesScroll: msgs.scrollWidth - msgs.clientWidth,
            };
        })()`);

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
                    if (m.panelRight !== null) check(`${tag}: the Subscribers column's right gap is at least the page gutter`, m.panelRight >= v.gutter && m.panelText >= v.gutter, JSON.stringify([m.panelRight, m.panelText]));
                    check(`${tag}: no subscriber label is clipped`, m.clipped.length === 0, JSON.stringify(m.clipped));
                    check(`${tag}: nothing runs past the right edge, and no horizontal scroll`, m.overflowing.length === 0 && m.pageScroll <= 0 && m.messagesScroll <= 0, JSON.stringify(m));
                    if (shots) {
                        fs.mkdirSync(shots, { recursive: true });
                        const shot = await Page.captureScreenshot({ format: 'png' });
                        fs.writeFileSync(path.join(shots, `board-spacing-${phase}-${v.name}-${theme}.png`), Buffer.from(shot.data, 'base64'));
                    }
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
