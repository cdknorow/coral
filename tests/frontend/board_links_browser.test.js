// Links on the message board are readable in both themes (#3548): a post's links (a web link, a
// dashboard page link, an artifact link) and a subscriber's name take the theme's link colour
// (--accent) instead of the browser's #0000ee, with a hover underline and a visible keyboard
// focus ring; a subscriber's name is a button (no javascript: href) that still opens the agent.
// BOARD_LINK_SHOTS=<dir> also saves the board in each theme.
'use strict';

const CDP = require('chrome-remote-interface');
const fs = require('node:fs');
const path = require('node:path');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const HEX = 'c'.repeat(64);

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what) => waitFor(expr, what, { timeout: 8000, soft: true });
    const api = (method, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' },
        body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    const board = 'links-qa-3548';
    const shots = process.env.BOARD_LINK_SHOTS || '';
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        await api('POST', `/api/board/${board}/subscribe`, { subscriber_id: 'Links QA', job_title: 'Links QA' });
        await api('POST', `/api/board/${board}/messages`, { subscriber_id: 'Links QA',
            content: `See [the docs](https://example.test/docs), [the agents](/agents) and [the report](coral://artifacts/${HEX}).` });
        const l2 = Page.loadEventFired();
        await Page.navigate({ url: `${BASE}/board/${board}` });
        await l2;
        await coralReady();
        const POST = '#messageboard-view .mb-message-body';
        await until(`document.querySelectorAll('${POST} a').length >= 3 && !!document.querySelector('#mb-subscribers-list .subscriber-history-link, .subscriber-history-link')`, 'the post and the subscriber');

        const themes = async theme => {
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}'); return true; })()`);
            await ev(`new Promise(r => setTimeout(() => r(true), 250))`); // sleep-ok: the 0.15 s colour transitions
            return ev(`(() => {
                const accent = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim();
                const h = accent.replace('#', ''); const want = 'rgb(' + [0, 2, 4].map(i => parseInt(h.slice(i, i + 2), 16)).join(', ') + ')';
                const links = [...document.querySelectorAll('${POST} a')].map(a => ({ text: a.textContent, color: getComputedStyle(a).color }));
                const sub = document.querySelector('.subscriber-history-link');
                return { want, links, sub: sub && { tag: sub.tagName, href: sub.getAttribute('href'), color: getComputedStyle(sub).color } };
            })()`);
        };
        for (const theme of ['dark', 'light']) {
            const t = await themes(theme);
            check(`${theme}: every link in the post has the theme's link colour`, t.links.length >= 3 && t.links.every(l => l.color === t.want), JSON.stringify(t));
            check(`${theme}: the subscriber's name too`, !!t.sub && t.sub.color === t.want, JSON.stringify(t.sub));
            if (shots) {
                fs.mkdirSync(shots, { recursive: true });
                await Emulation.setDeviceMetricsOverride({ width: 1280, height: 720, deviceScaleFactor: 2, mobile: false });
                await ev(`new Promise(r => setTimeout(() => r(true), 250))`); // sleep-ok: layout after the resize
                const clip = await ev(`(() => { const a = document.querySelector('${POST}').closest('.mb-message') || document.querySelector('${POST}');
                    const s = document.querySelector('.subscriber-history-link').closest('li') || document.querySelector('.subscriber-history-link');
                    const r1 = a.getBoundingClientRect(), r2 = s.getBoundingClientRect();
                    const x = Math.max(0, Math.min(r1.left, r2.left) - 8), y = Math.max(0, Math.min(r1.top, r2.top) - 8);
                    return { x, y, width: Math.min(innerWidth - x, Math.max(r1.right, r2.right) - x + 8), height: Math.max(r1.bottom, r2.bottom) - y + 8, scale: 1 }; })()`);
                const shot = await Page.captureScreenshot({ format: 'png', clip });
                fs.writeFileSync(path.join(shots, `board-links-${theme}.png`), Buffer.from(shot.data, 'base64'));
                await Emulation.clearDeviceMetricsOverride();
            }
        }

        const sub = await ev(`(() => { const b = document.querySelector('.subscriber-history-link'); return { tag: b.tagName, type: b.getAttribute('type'), href: b.getAttribute('href') }; })()`);
        check("the subscriber's name is a button, with no javascript: href", sub.tag === 'BUTTON' && sub.type === 'button' && sub.href === null, JSON.stringify(sub));

        const hover = await ev(`(() => { const a = document.querySelector('${POST} a'); const st = document.createElement('style');
            return getComputedStyle(a).textDecorationLine; })()`);
        check('post links are not underlined until hovered', hover === 'none', hover);

        const focus = await ev(`(() => { const out = {};
            for (const [k, sel] of [['link', '${POST} a'], ['subscriber', '.subscriber-history-link']]) {
                const el = document.querySelector(sel); el.focus({ focusVisible: true });
                const cs = getComputedStyle(el); out[k] = { focused: document.activeElement === el, outline: cs.outlineStyle, width: cs.outlineWidth };
            } return out; })()`);
        check('keyboard focus is visible on a post link and on a subscriber name',
            focus.link.focused && focus.link.outline !== 'none' && focus.subscriber.focused && focus.subscriber.outline !== 'none', JSON.stringify(focus));
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        try { await api('DELETE', `/api/board/${board}`); } catch { /* best effort */ }
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
