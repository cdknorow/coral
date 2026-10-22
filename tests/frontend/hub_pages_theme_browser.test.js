// The Cloud Hub's own pages (sign-in, connect a machine) load theme_init.js and say so with data-hub-page on <html> (#6094): with no saved choice they follow the system theme, like the dashboard through the hub does; a saved
// dark, light or system choice is still honoured. This computer's own pages (the dashboard, whose boot flag says "local", and its sign-in and create-a-password pages, which have neither) still start dark. theme_init.js is run as the
// page does, in a document with or without #coral-boot and data-hub-page.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

// The hub's two pages carry the mark; the real pages are the ones that matter.
for (const page of ['signin', 'enroll']) assert.match(fs.readFileSync(path.join(__dirname, `../../coral-go/pkg/frontend/templates/cloud/${page}.html`), 'utf8'), /<html[^>]* data-hub-page[ >]/, `cloud/${page}.html says it is a hub page`);

(async () => {
    const { client, Page, Emulation, ev } = await connect();
    try {
        await Page.enable();
        const run = async ({ system, boot, saved, hub }) => {
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: system }] });
            await Page.navigate({ url: BASE + '/static/theme_init.js' }); await Page.loadEventFired();
            return ev(`(async () => {
                const src = await (await fetch('/static/theme_init.js')).text();
                document.documentElement.removeAttribute('data-theme');
                document.querySelectorAll('#coral-boot').forEach(e => e.remove()); document.documentElement.removeAttribute('data-hub-page'); ${hub ? "document.documentElement.setAttribute('data-hub-page', '');" : ''}
                try { localStorage.removeItem('coral-theme'); ${saved ? `localStorage.setItem('coral-theme', ${JSON.stringify(saved)});` : ''} } catch (e) {}
                ${boot ? `const b = document.createElement('script'); b.type = 'application/json'; b.id = 'coral-boot'; b.textContent = ${JSON.stringify(boot)}; document.head.append(b);` : ''}
                new Function(src)();
                const t = document.documentElement.getAttribute('data-theme');
                try { localStorage.removeItem('coral-theme'); } catch (e) {}
                return t;
            })()`);
        };
        for (const system of ['light', 'dark']) {
            assert.equal(await run({ system, hub: true }), system, `a hub page with nothing saved follows the system (${system})`);
            assert.equal(await run({ system, hub: true, saved: 'dark' }), 'dark', 'a saved dark is kept');
            assert.equal(await run({ system, hub: true, saved: 'light' }), 'light', 'a saved light is kept');
            assert.equal(await run({ system, hub: true, saved: 'system' }), system, 'a saved system follows the system');
            assert.equal(await run({ system, hub: true, saved: 'GhostV3' }), system, 'a saved value that is not a theme is not one');
            assert.equal(await run({ system }), 'dark', 'this computer\'s own sign-in and password pages (no boot flag, not a hub page) start dark, as before');
            assert.equal(await run({ system, boot: '{"mode":"local"}' }), 'dark', 'the dashboard on this computer starts dark, as before');
            assert.equal(await run({ system, boot: '{"mode":"cloud"}' }), system, 'the dashboard through the hub follows the system');
        }
        console.log('PASS the hub pages follow the system theme when nothing is saved');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
