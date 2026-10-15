// The Coral Hub indicator in the header (#3544), with a mocked /api/cloud/status: each state
// shows its look and says it (tooltip and accessible name: the state, the hub's host, the time;
// never a token, code or id); it's hidden when this Coral isn't enrolled, and when the status
// can't be read (it then stops polling); the connecting pulse stops under prefers-reduced-motion;
// a click opens Settings at its Coral Hub section; it fits at phone width.
// HUB_SHOTS=<dir> also saves a screenshot of each state, light and dark, for the operator.
'use strict';

const CDP = require('chrome-remote-interface');
const fs = require('node:fs');
const path = require('node:path');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

const SECRET_ID = 'srv_9f2e7c_secret';
const SECRET_CODE = 'WXYZ-1234';
const base = { configured: true, hub_url: 'https://app.coralai.ai', server_id: SECRET_ID, user_code: SECRET_CODE, label: 'Work Mac', max_mode: 'interact' };
const STATES = {
    connected: { ...base, state: 'connected', connected_at: new Date(Date.now() - 3600e3).toISOString() },
    connecting: { ...base, state: 'connecting' },
    retrying: { ...base, state: 'retrying', last_error: 'the hub is unreachable; retrying' },
    revoked: { ...base, state: 'revoked', last_error: 'the hub rejected the token' },
    idle: { configured: true, hub_url: 'https://app.coralai.ai', state: 'idle', max_mode: 'interact' },
    enrolling: { ...base, state: 'enrolling' },
    off: { configured: false, state: 'off', max_mode: 'interact' },
};

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what) => waitFor(expr, what, { timeout: 6000, soft: true });
    const shots = process.env.HUB_SHOTS || '';
    try {
        await Page.enable();
        // The page's own requests for the status answer from window.__hubStatus (or its HTTP status).
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const real = window.fetch.bind(window);
            window.__hubCalls = 0;
            window.__hubStatus = ${JSON.stringify(STATES.connected)};
            window.fetch = (u, o) => {
                if (new URL(String(u), location.href).pathname === '/api/cloud/status') {
                    window.__hubCalls++;
                    const s = window.__hubStatus;
                    if (typeof s === 'number') return Promise.resolve(new Response('{"error":"refused"}', { status: s, headers: { 'Content-Type': 'application/json' } }));
                    return Promise.resolve(new Response(JSON.stringify(s), { status: 200, headers: { 'Content-Type': 'application/json' } }));
                }
                return real(u, o);
            };
        })()` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        const mod = `import((window.__coralStatic || '/static/') + 'hub_indicator.js')`;
        // Show a state and let the button's 0.15 s colour transition finish before it is read.
        const show = async name => { await ev(`(async () => { window.__hubStatus = ${JSON.stringify(STATES[name])}; (await ${mod}).renderIndicator(window.__hubStatus); return true; })()`);
            await ev(`new Promise(r => setTimeout(() => r(true), 250))`); }; // sleep-ok: the CSS colour transition (0.15 s)
        const look = () => ev(`(() => { const b = document.getElementById('hub-indicator'); if (!b) return null;
            const cs = getComputedStyle(b); const r = b.getBoundingClientRect();
            return { hidden: b.hidden || cs.display === 'none', state: b.dataset.state || null, title: b.title, aria: b.getAttribute('aria-label'),
                color: cs.color, anim: cs.animationName, inBar: !!b.closest('.top-bar-actions'), x: r.right, w: r.width,
                success: getComputedStyle(document.documentElement).getPropertyValue('--success').trim(), warning: getComputedStyle(document.documentElement).getPropertyValue('--warning').trim(),
                dom: document.documentElement.outerHTML.includes(${JSON.stringify(SECRET_ID)}) || document.documentElement.outerHTML.includes(${JSON.stringify(SECRET_CODE)}) }; })()`);
        const rgb = hex => { const h = hex.replace('#', ''); return `rgb(${parseInt(h.slice(0, 2), 16)}, ${parseInt(h.slice(2, 4), 16)}, ${parseInt(h.slice(4, 6), 16)})`; };

        console.log('1. The page start reads the status and shows it');
        await until(`!!document.getElementById('hub-indicator') && document.getElementById('hub-indicator').dataset.state === 'connected'`, 'the indicator');
        let l = await look();
        check('connected: shown in the header, success colour (green)', l && !l.hidden && l.inBar && l.state === 'connected' && l.color === rgb(l.success), JSON.stringify(l));
        check('its name: the state, the hub host and since when', l && /^Coral Hub: connected to app\.coralai\.ai since /.test(l.aria) && l.title === l.aria, l && l.aria);
        check('no id or code anywhere in the page', l && !l.dom);

        console.log('2. Each state');
        await show('connecting'); l = await look();
        check('connecting: muted, pulsing', !l.hidden && l.state === 'connecting' && l.anim === 'hub-indicator-pulse' && l.aria === 'Coral Hub: connecting to app.coralai.ai', JSON.stringify(l));
        await show('retrying'); l = await look();
        check('retrying: the same look, says reconnecting', !l.hidden && l.state === 'connecting' && l.aria === 'Coral Hub: reconnecting to app.coralai.ai', JSON.stringify(l));
        await show('revoked'); l = await look();
        check('enrolled but disconnected: warning tint and why', !l.hidden && l.state === 'disconnected' && l.color === rgb(l.warning) && /disconnected from app\.coralai\.ai \(/.test(l.aria), JSON.stringify(l));
        for (const name of ['idle', 'enrolling', 'off']) {
            await show(name); l = await look();
            check(`not enrolled (${name}): hidden, with no state, title or name left`, l && l.hidden && !l.state && !l.title && l.aria === null, JSON.stringify(l));
        }

        console.log('3. Reduced motion stops the pulse');
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
        await show('connecting'); l = await look();
        check('no animation with prefers-reduced-motion', l.anim === 'none', l.anim);
        await Emulation.setEmulatedMedia({ features: [] });

        console.log('4. A click opens Settings at the Coral Hub section');
        await show('connected');
        await ev(`document.getElementById('hub-indicator').click(); true`);
        await until(`(() => { const m = document.getElementById('settings-modal'); return m && getComputedStyle(m).display !== 'none'; })()`, 'Settings');
        await until(`document.activeElement && document.activeElement.id === 'settings-cloud-status'`, 'the Coral Hub section');
        const opened = await ev(`(() => { const s = document.getElementById('settings-cloud'); const r = s.getBoundingClientRect();
            return { modal: getComputedStyle(document.getElementById('settings-modal')).display !== 'none', focus: document.activeElement && document.activeElement.id, inView: r.top >= 0 && r.top < innerHeight }; })()`);
        check('Settings opens with its Coral Hub section in view and focused', opened.modal && opened.focus === 'settings-cloud-status' && opened.inView, JSON.stringify(opened));
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);

        console.log('5. Phone width');
        await Emulation.setDeviceMetricsOverride({ width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
        await show('connected');
        const phone = await ev(`(() => { const b = document.getElementById('hub-indicator'); const r = b.getBoundingClientRect();
            return { visible: !b.hidden && r.width > 0, inside: r.left >= 0 && r.right <= innerWidth, overflow: document.documentElement.scrollWidth - innerWidth }; })()`);
        check('visible and inside the viewport at 375 px, no overflow', phone.visible && phone.inside && phone.overflow <= 0, JSON.stringify(phone));
        await Emulation.clearDeviceMetricsOverride();

        if (shots) {
            fs.mkdirSync(shots, { recursive: true });
            await Emulation.setDeviceMetricsOverride({ width: 1280, height: 720, deviceScaleFactor: 2, mobile: false });
            for (const theme of ['light', 'dark']) {
                // As Settings switches it: inline custom-theme variables would override data-theme.
                await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}'); return true; })()`);
                for (const name of ['connected', 'connecting', 'revoked']) {
                    await show(name);
                    await ev(`new Promise(r => requestAnimationFrame(() => r(true)))`);
                    const clip = await ev(`(() => { const r = document.querySelector('.top-bar-actions').getBoundingClientRect(); return { x: Math.max(0, r.left - 140), y: 0, width: Math.min(innerWidth, r.width + 160), height: Math.max(48, r.bottom + 8), scale: 1 }; })()`);
                    const shot = await Page.captureScreenshot({ format: 'png', clip });
                    fs.writeFileSync(path.join(shots, `hub-indicator-${name}-${theme}.png`), Buffer.from(shot.data, 'base64'));
                }
            }
            await Emulation.clearDeviceMetricsOverride();
            console.log(`screenshots in ${shots}`);
        }

        console.log('6. A status it may not read (not this computer): hidden, and polling stops');
        await show('connected');
        // One poll step, as the 15 s interval runs it, answered 403 (a dashboard that isn't this computer's).
        const before = await ev(`(async () => { window.__hubStatus = 403; const m = await ${mod}; await m.refresh(); return window.__hubCalls; })()`);
        l = await look();
        check('a 403 hides it, with no state, title or name left', l && l.hidden && !l.state && !l.title && l.aria === null, JSON.stringify(l));
        const after = await ev(`(async () => { window.__hubStatus = ${JSON.stringify(STATES.connected)}; const m = await ${mod}; await m.refresh(); return window.__hubCalls; })()`);
        check('and it stops asking (no further status request)', after === before && (await look()).hidden, `${before} -> ${after}`);
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
