// The sidebar's goal line ("No goal yet" here) is at least 4.5:1 on every background its row can
// have, in both themes (#4209): plain, hovered, keyboard-focused, selected, sleeping, done, needing
// attention and stuck, and each of those hovered. v1.4.71's a11y_dashboard_browser found 4.33:1 in
// the light theme only when the pointer happened to rest on a row (hover); here each state is forced
// (CSS.forcePseudoState for :hover and :focus-visible), so the check does not depend on the mouse.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const STATES = [[], ['active'], ['sleeping'], ['needs-attention'], ['needs-attention', 'is-stuck'], ['active', 'sleeping']];
const PSEUDO = [[], ['hover'], ['focus-visible'], ['hover', 'focus-visible']];

// The label's colour over its composited background (every translucent layer up to an opaque one).
const RATIO = `(() => {
    // rgb()/rgba(), or color(srgb r g b / a) (0-1 channels: how Chrome reports a color-mix() colour).
    const parse = s => { let m = s.match(/rgba?\\(([^)]+)\\)/); if (m) { const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; }
        m = s.match(/color\\(srgb ([^)]+)\\)/); if (!m) return null; const p = m[1].split(/[ \\/]+/).filter(Boolean).map(Number); return { r: p[0] * 255, g: p[1] * 255, b: p[2] * 255, a: p.length > 3 ? p[3] : 1 }; };
    const blend = (f, b) => ({ r: f.r * f.a + b.r * (1 - f.a), g: f.g * f.a + b.g * (1 - f.a), b: f.b * f.a + b.b * (1 - f.a), a: 1 });
    const lum = c => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
    const bgOf = e => { const layers = []; for (let n = e; n && n.nodeType === 1; n = n.parentElement) { const c = parse(getComputedStyle(n).backgroundColor); if (c && c.a > 0) { layers.push(c); if (c.a === 1) break; } }
        let base = parse(getComputedStyle(document.body).backgroundColor) || { r: 255, g: 255, b: 255, a: 1 }; for (let i = layers.length - 1; i >= 0; i--) base = blend(layers[i], base); return base; };
    const label = document.querySelector('.session-group-item[data-qa-row] .session-goal-empty-label');
    if (!label) return null;
    const color = parse(getComputedStyle(label).color);
    if (!color) return 'unparsed ' + getComputedStyle(label).color;
    const bg = bgOf(label), fg = blend(color, bg);
    return Math.round((Math.max(lum(fg), lum(bg)) + 0.05) / (Math.min(lum(fg), lum(bg)) + 0.05) * 100) / 100;
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { DOM, CSS } = client;
    try {
        await Page.enable(); await DOM.enable(); await CSS.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        const bad = [];
        let checked = 0;
        for (const theme of ['dark', 'light']) {
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { window.__m.add('docs-bot', 'A', 2, { sleeping: false }); window.__m.add('api-worker', 'B', 2, { sleeping: false });
                T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}'); })()`);
            await ev(`(() => { const st = document.createElement('style'); st.textContent = '* { transition: none !important }'; document.head.appendChild(st); return true; })()`);
            await waitFor(`!!document.querySelector('.session-group-item .session-goal-empty-label')`, 'a row with "No goal yet"', 10000,
                `({ rows: document.querySelectorAll('.session-group-item').length, goals: [...document.querySelectorAll('.session-goal')].map(g => g.textContent.trim().slice(0, 30)), live: (window.__qaState && window.__qaState.liveSessions || []).length })`);
            await ev(`(() => { const li = document.querySelector('.session-group-item .session-goal-empty-label').closest('.session-group-item'); li.setAttribute('data-qa-row', ''); return true; })()`);
            const { root } = await DOM.getDocument({ depth: -1 });
            const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: '.session-group-item[data-qa-row]' });
            const base = await ev(`document.querySelector('[data-qa-row]').className`);
            for (const classes of STATES) {
                await ev(`(() => { const li = document.querySelector('[data-qa-row]'); li.className = ${JSON.stringify(base)}; for (const c of ['active', 'sleeping', 'needs-attention', 'is-stuck']) li.classList.remove(c); for (const c of ${JSON.stringify(classes)}) li.classList.add(c); return true; })()`);
                for (const pseudo of PSEUDO) {
                    await CSS.forcePseudoState({ nodeId, forcedPseudoClasses: pseudo });
                    const ratio = await ev(RATIO);
                    assert.notEqual(ratio, null, 'the label is there');
                    checked++;
                    if (typeof ratio !== 'number' || ratio < 4.5) bad.push(`${theme} ${classes.join('+') || 'plain'}${pseudo.length ? ' :' + pseudo.join(':') : ''} ${ratio}`);
                }
            }
            await CSS.forcePseudoState({ nodeId, forcedPseudoClasses: [] });
        }
        assert.deepEqual(bad, [], `"No goal yet" under 4.5:1: ${bad.join('; ')}`);
        console.log(`PASS "No goal yet" is at least 4.5:1 on all ${checked} row backgrounds, both themes`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
