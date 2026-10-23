// A built-in theme picked in Settings survives a reload (#283, QA's #278): Light and System were saved (theme, with custom_theme
// empty) and every page load put Dark back. Saved through the settings PUT, then the page is loaded again:
//  - Light loads Light;
//  - System follows the OS scheme (prefers-color-scheme, emulated), including a change while the page is open;
//  - Dark, and nothing chosen, load the Dark theme as before.
'use strict';

const CDP = require('chrome-remote-interface');
const { BASE } = require('./test_base');

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 15000) => waitFor(expr, what, { timeout: ms, soft: true });
    const guard = await require('./helpers/settings_guard').guardSettings(); // theme and custom_theme are put back in the finally (#4849)
    const scheme = value => Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value }] });
    const save = body => ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: ${JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    // A fresh load, as after a restart of the browser: the page reads the saved settings and applies the theme.
    const reload = async () => {
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await coralReady();
    };
    const seen = () => ev(`(async () => { const st = (await import((window.__coralStatic || '/static/') + 'state.js')).state;
        return { dataTheme: document.documentElement.getAttribute('data-theme'), pref: localStorage.getItem('coral-theme'),
                 theme: st.settings && st.settings.theme, custom: st.settings && st.settings.custom_theme }; })()`);
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await scheme('dark');
        await reload();

        check('Light is saved', await save({ theme: 'light', custom_theme: '' }) === 200);
        await reload();
        await until(`document.documentElement.getAttribute('data-theme') === 'light'`, 'the Light theme after a reload');
        let s = await seen();
        check('a reload keeps Light', s.dataTheme === 'light' && s.theme === 'light' && s.custom === '', JSON.stringify(s));

        await scheme('light');
        check('System is saved', await save({ theme: 'system', custom_theme: '' }) === 200);
        await reload();
        await until(`document.documentElement.getAttribute('data-theme') === 'light'`, 'System on a light OS');
        s = await seen();
        check('a reload keeps System, light on a light OS', s.dataTheme === 'light' && s.theme === 'system', JSON.stringify(s));
        await scheme('dark');
        await until(`document.documentElement.getAttribute('data-theme') === 'dark'`, 'System following the OS to dark');
        s = await seen();
        check('System follows the OS while the page is open', s.dataTheme === 'dark' && s.theme === 'system', JSON.stringify(s));
        await reload();
        await until(`document.documentElement.getAttribute('data-theme') === 'dark'`, 'System on a dark OS');
        s = await seen();
        check('a reload keeps System, dark on a dark OS', s.dataTheme === 'dark' && s.theme === 'system', JSON.stringify(s));

        await scheme('light'); // Dark must not follow the OS
        check('Dark is saved', await save({ theme: 'dark', custom_theme: '' }) === 200);
        await reload();
        await until(`document.documentElement.getAttribute('data-theme') === 'dark'`, 'Dark after a reload');
        s = await seen();
        // on a light OS, with this browser's earlier System still saved; nothing is written into the settings (#6114)
        check('Dark loads the Dark theme, as before', s.dataTheme === 'dark' && s.theme === 'dark' && s.custom === '', JSON.stringify(s));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        try { await Emulation.setEmulatedMedia({ features: [] }); } catch { /* best effort */ }
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
