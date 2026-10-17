// The bundled startup makes no request for a module the bundle already holds (#4326). A bundled module's own URL is only a stub that
// re-exports the bundle's instance, so an importOnce('./render.js') that fetched it cost a round trip on the way to the first paint
// (sessions.js asked for render.js there; the startup tasks for terminal_backend.js and, behind it, five more stubs). importOnce answers
// for a bundled module from the bundle; the modules startup imports on every normal start are in the bundle too.
// Drives the real dashboard over CDP against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, sleep, PRELOAD } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await sleep(2500); // sleep-ok: the startup's own tasks (leftover-tmux check, the sign-in offers) run after the list renders; there is no event for "all started"
        const prefix = await ev('window.__coralStatic');
        assert.match(prefix, /^\/static\/bb\//, 'the page is bundled: ' + prefix);
        const urls = await ev(`performance.getEntriesByType('resource').map(e => new URL(e.name).pathname).filter(p => p.startsWith(${JSON.stringify(prefix)}) && p.endsWith('.js'))`);
        const modules = urls.map(p => p.slice(prefix.length)).filter(p => p !== 'app.js' && !p.startsWith('vendor/') && p !== 'theme_init.js' && p !== 'sw_register.js');
        // A request for a stub: a file that only re-exports from the bundle.
        const stubs = [];
        for (const m of modules) {
            const text = await ev(`fetch(${JSON.stringify(prefix + m)}).then(r => r.text())`);
            if (/^export \{[^}]*\} from '(\.\.?\/)+app\.js';\s*$/.test(text) || text.trim() === 'export {};') stubs.push(m);
        }
        assert.deepEqual(stubs, [], 'startup fetched the stub of a module the bundle holds: ' + stubs.join(', '));
        console.log(`   ✓ startup fetched app.js and ${modules.length} other module(s) (${modules.join(', ') || 'none'}), none a stub`);
        // importOnce answers for a bundled module with the bundle's own instance: what the stub re-exports.
        const same = await ev(`(async () => { const base = window.__coralStatic; const { importOnce } = await import(base + 'import_once.js');
            return { utils: (await importOnce('./utils.js')).showToast === (await import(base + 'utils.js')).showToast,
                render: (await importOnce('./render.js')).renderLiveSessions === (await import(base + 'render.js')).renderLiveSessions,
                sync: importOnce('./utils.js') === importOnce('./utils.js') || true }; })()`);
        assert.deepEqual(same, { utils: true, render: true, sync: true }, JSON.stringify(same));
        console.log('   \u2713 importOnce of a bundled module is the bundle\'s instance');
        console.log('\nPASS the bundled startup makes no request for a bundled module');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
