// The theme swap helper really renders the other theme (#5757). The dashboard keeps its custom colour variables inline on <html>, so setting only data-theme="light"
// leaves the dark colours in force: this suite pins that, and pins that helpers/theme.js (themeJs / swapTheme: the app's own two steps) turns every colour: the page
// background, the text colour and a variable the stylesheet defines in both blocks differ between the two themes, light is light and dark is dark.
'use strict';
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { themeJs, swapTheme } = require('./helpers/theme');

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { coralReady } = require('./helpers/wait').bind(ev);
    const results = [];
    const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    // What the page draws: the theme attribute, how many custom variables sit inline, the page background and text colour (read as pixels, so any colour syntax works) and a variable.
    const look = () => ev(`(() => { const r = document.documentElement, cs = getComputedStyle(document.body);
        const px = c => { const x = document.createElement('canvas').getContext('2d'); x.fillStyle = '#000'; x.fillStyle = c; x.fillRect(0, 0, 1, 1); return [...x.getImageData(0, 0, 1, 1).data].slice(0, 3); };
        const L = ([a, b, c]) => [a, b, c].map(v => v / 255).map(v => v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
        return { attr: r.getAttribute('data-theme'), inline: [...r.style].filter(p => p.startsWith('--')).length, bg: L(px(cs.backgroundColor)), fg: L(px(cs.color)), bgPrimary: getComputedStyle(r).getPropertyValue('--bg-primary').trim() }; })()`);
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        const loaded = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await loaded;
        await coralReady();

        await swapTheme(ev, 'dark');
        const dark = await look();
        check('dark: the attribute is dark, no custom variable is inline, the page is dark with light text', dark.attr === 'dark' && dark.inline === 0 && dark.bg < 0.1 && dark.fg > dark.bg + 0.3, JSON.stringify(dark));

        await ev(`${themeJs('light')}; true`);
        const light = await look();
        check('light: the attribute is light, no custom variable is inline, the page is light with dark text', light.attr === 'light' && light.inline === 0 && light.bg > 0.5 && light.fg < light.bg - 0.3, JSON.stringify(light));
        check('a variable the stylesheet sets in both blocks (--bg-primary) differs between the two', dark.bgPrimary && light.bgPrimary && dark.bgPrimary !== light.bgPrimary, `${dark.bgPrimary} vs ${light.bgPrimary}`);

        // Back to dark through the same helper: the dark look returns, nothing stays light.
        await swapTheme(ev, 'dark');
        const again = await look();
        check('dark again: the same dark page', again.attr === 'dark' && again.bg < 0.1 && again.bgPrimary === dark.bgPrimary, JSON.stringify(again));

        // The trap, pinned: the attribute alone does not turn a page that has custom variables inline. Put some back the way the app has them on load (a custom theme's), then swap raw.
        await ev(`(() => { document.documentElement.style.setProperty('--bg-primary', '#111114'); document.documentElement.style.setProperty('--bg-secondary', '#18181c'); return true; })()`);
        await ev(`document.documentElement.setAttribute('data-theme', 'light'); true`);
        const raw = await look();
        check('the attribute alone leaves the inline colours in force (why suites use helpers/theme.js)', raw.attr === 'light' && raw.inline >= 2 && raw.bgPrimary === dark.bgPrimary, JSON.stringify(raw));
        await ev(`${themeJs('light')}; true`);
        const fixed = await look();
        check('the helper clears them: the page is light', fixed.inline === 0 && fixed.bgPrimary === light.bgPrimary, JSON.stringify(fixed));
    } catch (e) { check('the suite ran', false, String(e.message).slice(0, 300)); }
    await client.close();
    const bad = results.filter(r => !r).length;
    console.log(bad ? `${bad} FAILED` : `${results.length}/${results.length} passed`);
    process.exit(bad ? 1 : 0);
})();
