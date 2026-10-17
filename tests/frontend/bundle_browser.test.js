// The bundled startup (#4228): the dashboard starts from one script and one stylesheet under /static/bb/<build>/; every module
// still has its own URL (a stub) that is the SAME instance as the one inside the bundle, so a module loaded later by importOnce, or
// imported by URL, shares its state; a source map names the original files; and the agent pop-out stays on the original files.
//
// Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const POPOUT = BASE + '/agent/123e4567-e89b-12d3-a456-426614174000';

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');

        console.log('1. The dashboard loads one script and one stylesheet from the bundled prefix');
        const shell = await ev(`(() => ({
            app: document.querySelector('script[type=module][src$="app.js"]').src,
            sheets: [...document.querySelectorAll('head link[rel=stylesheet]')].map(l => new URL(l.href).pathname),
            prefix: window.__coralStatic,
        }))()`);
        assert.match(new URL(shell.app).pathname, /^\/static\/bb\/[0-9a-f]{12}\/app\.js$/, shell.app);
        assert.equal(shell.prefix, new URL(shell.app).pathname.replace(/app\.js$/, ''), 'the page knows its prefix');
        assert.deepEqual(shell.sheets.map(p => p.slice(shell.prefix.length)), ['css/bundle.css'], 'one stylesheet, the bundle: ' + shell.sheets);
        const css = await ev(`(() => { const s = [...document.styleSheets].find(x => x.href && x.href.endsWith('css/bundle.css')); return { rules: s.cssRules.length, vars: getComputedStyle(document.documentElement).getPropertyValue('--bg-primary').trim() }; })()`);
        assert.ok(css.rules > 1000 && css.vars, `the stylesheet is applied (${JSON.stringify(css)})`);
        console.log(`   ✓ app.js and css/bundle.css (${css.rules} rules) under ${shell.prefix}`);

        console.log('2. A module imported by its URL is the bundle\'s instance, not a copy');
        const shared = await ev(`(async () => {
            const base = window.__coralStatic;
            const state = (await import(base + 'state.js')).state;
            state.__bundleProbe = 'seen';
            const render = await import(base + 'render.js');
            const again = (await import(base + 'state.js')).state;
            return { state: state === again, sameAsApp: state === T.state, sees: T.state.__bundleProbe,
                render: render.renderLiveSessions === T.render.renderLiveSessions };
        })()`);
        assert.deepEqual(shared, { state: true, sameAsApp: true, sees: 'seen', render: true }, JSON.stringify(shared));
        console.log('   ✓ state.js and render.js are the instances the app runs');

        console.log('3. A lazily loaded module (importOnce) loads and works against the stubs');
        const lazy = await ev(`(async () => {
            const { importOnce } = await import(window.__coralStatic + 'import_once.js');
            const sec = await importOnce('./security_settings.js');
            const ui = await importOnce('./password_ui.js');
            return { sec: typeof sec.loadSecurityRow, ui: typeof ui.maybePromptSetPassword };
        })()`);
        assert.deepEqual(lazy, { sec: 'function', ui: 'function' }, JSON.stringify(lazy));
        console.log('   ✓ security_settings.js and password_ui.js');

        console.log('4. The source map names the original files');
        const map = await ev(`(async () => {
            const text = await (await fetch(window.__coralStatic + 'app.js')).text();
            const link = /sourceMappingURL=(\\S+)/.exec(text.slice(-200));
            if (!link) return null;
            const m = await (await fetch(new URL(link[1], location.origin + window.__coralStatic))).json();
            return { link: link[1], count: m.sources.length, has: ['state.js', 'render.js', 'app.js'].every(n => m.sources.some(s => s.endsWith(n))) };
        })()`);
        assert.ok(map && map.has && map.count > 100, JSON.stringify(map));
        console.log(`   ✓ ${map.link}: ${map.count} sources`);

        console.log('5. The agent pop-out stays on the original files and their stylesheets');
        await Page.navigate({ url: POPOUT });
        await Page.loadEventFired();
        const pop = await ev(`(() => ({ prefix: window.__coralStatic, mode: document.body.dataset.entryMode,
            app: document.querySelector('script[type=module][src$="app.js"]').src, sheets: document.querySelectorAll('head link[rel=stylesheet]').length }))()`);
        assert.match(pop.prefix, /^\/static\/b\/[0-9a-f]{12}\/$/, pop.prefix);
        assert.equal(pop.mode, 'agent');
        assert.ok(!pop.app.includes('/static/bb/') && pop.sheets >= 20, JSON.stringify(pop));
        console.log(`   ✓ ${pop.prefix}, ${pop.sheets} stylesheets`);

        console.log('\nAll bundle checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
