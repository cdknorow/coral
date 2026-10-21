// A registered server's plain-http warning in the Servers tab (#4301, pairs with #4297 step 2): the server's list, add, edit and test
// answers carry "warning" for a server reached over plain http at an address that is not this computer. A row with a warning shows it as
// an icon and words (not only a colour, not only a tooltip), a row without shows nothing, the edit form repeats it, a refused http URL
// (400 from the add) shows its message next to the URL field, and it all fits a 390 px phone at 200% text. SHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

const WARNING = 'the key crosses the network unencrypted; use https or a tunnel';
const REFUSED = 'URL must be https: the API key would cross the network unencrypted (a server on a private network or this computer can be registered with allow_private)';
const SERVERS = [
    { id: 'local', label: 'studio-mac', local: true, status: 'online' },
    { id: 'lan', label: 'LAN box', url: 'http://192.168.1.50:8420', status: 'online', allow_private: true, warning: WARNING },
    { id: 'tls', label: 'epoch', url: 'https://epoch.example.net:8420', status: 'online' },
    { id: 'fwd', label: 'port forward', url: 'http://127.0.0.1:9000', status: 'online' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a redraw and its layout settle over frames; there is no completion event
    const ok = name => console.log(`   ✓ ${name}`);
    const shot = async n => { if (process.env.SHOT_DIR) fs.writeFileSync(`${process.env.SHOT_DIR}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const R = window.fetch; window.__posts = 0; window.__postAnswer = { status: 400, body: { error: ${JSON.stringify(REFUSED)} } };
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
                const json = (status, body) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }));
                if (p === '/api/health') return json(200, { status: 'ok', hub: true, version: '1.4.75' });
                if (p === '/api/servers' && m === 'GET') return json(200, ${JSON.stringify(SERVERS)});
                if (p === '/api/servers' && m === 'POST') { window.__posts++; return json(window.__postAnswer.status, window.__postAnswer.body); }
                return R(u, o); }; })();` });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { const s = await import((window.__coralStatic || '/static/') + 'state.js'); s.state.hub = true; s.state.servers = ${JSON.stringify(SERVERS)}; return true; })()`);
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`!!document.querySelector('#server-row-lan')`, 'the servers list');

        // 1. rows
        const rows = await ev(`Object.fromEntries(['lan', 'tls', 'fwd'].map(id => { const r = document.getElementById('server-row-' + id); const w = r.querySelector('.servers-row-warn');
            return [id, w ? { text: w.lastElementChild.textContent.replace(/\\s+/g, ' ').trim(), icon: !!w.querySelector('.material-icons[aria-hidden="true"]'), word: w.querySelector('b') && w.querySelector('b').textContent, visible: w.checkVisibility({ checkVisibilityCSS: true }) } : null]; }))`);
        assert.deepEqual(rows.lan, { text: 'Warning: The key crosses the network unencrypted; use https or a tunnel.', icon: true, word: 'Warning:', visible: true });
        assert.equal(rows.tls, null, 'an https row shows nothing'); assert.equal(rows.fwd, null, 'a row with no warning field shows nothing');
        ok('a row with a warning shows an icon and "Warning: ..." in words; rows without one show nothing');
        await shot('rows-desktop');

        // 2. the edit form repeats it; an https row's does not
        await ev(`document.querySelector('#server-row-lan [data-action=edit]').click(); true`);
        await waitFor(`!!document.getElementById('servers-form')`, 'the edit form');
        assert.equal(await ev(`!!document.querySelector('#servers-form .servers-row-warn')`), true, 'the edit form shows the warning');
        assert.equal(await ev(`document.querySelector('#servers-form input[name=url]').closest('label').textContent.includes('unencrypted')`), false, 'it is not part of the URL field\'s name');
        // Name and description by what the markup says: the label text names each field, the help text under it describes it, and neither repeats the other.
        const acc = await ev(`(() => { const t = id => (document.getElementById(id) || {}).textContent?.replace(/\\s+/g, ' ').trim(); const f = n => document.querySelector('#servers-form input[name=' + n + ']');
            const o = {}; for (const n of ['id', 'url', 'api_key', 'allow_private']) { const e = f(n); o[n] = { name: e.getAttribute('aria-labelledby').split(' ').map(t).join(' '), desc: (e.getAttribute('aria-describedby') || '').split(' ').filter(Boolean).map(t).join(' ') }; } return o; })()`);
        assert.deepEqual(acc.id, { name: 'Server id', desc: 'Lowercase letters, digits and dashes. Cannot be changed later.' });
        assert.deepEqual(acc.url, { name: 'URL', desc: 'Scheme, host and port only.' });
        assert.deepEqual(acc.api_key, { name: 'API key', desc: 'Write-only. The key is stored encrypted and is never shown again.' });
        assert.equal(acc.allow_private.name, 'Allow private network address'); assert.match(acc.allow_private.desc, /^Only tick this for a server on your LAN/);
        ok('each server form field is named by its label and described by its help');
        await ev(`document.querySelector('#servers-form [data-action=cancel-form]').click(); document.querySelector('#server-row-tls [data-action=edit]').click(); true`);
        await waitFor(`!!document.getElementById('servers-form')`, 'the second edit form');
        assert.equal(await ev(`!!document.querySelector('#servers-form .servers-row-warn')`), false, 'an https server\'s edit form has none');
        await ev(`document.querySelector('#servers-form [data-action=cancel-form]').click(); true`);
        ok('the edit form shows the warning for the http server and none for the https one');

        // 3. a refused add shows its message next to the URL field, and that field is flagged and focused
        await ev(`document.querySelector('[data-action=add]').click(); true`);
        await waitFor(`!!document.getElementById('servers-form')`, 'the add form');
        await ev(`(() => { const f = document.getElementById('servers-form'); f.elements.id.value = 'new1'; f.elements.url.value = 'http://example.org:8420'; f.elements.api_key.value = 'k'; f.requestSubmit(); return true; })()`);
        await waitFor(`!!document.getElementById('servers-url-error')`, 'the inline URL error');
        const err = await ev(`(() => { const e = document.getElementById('servers-url-error'); const u = document.querySelector('#servers-form input[name=url]');
            return { text: e.lastElementChild.textContent.replace(/\\s+/g, ' ').trim(), role: e.getAttribute('role'), invalid: u.getAttribute('aria-invalid'), by: u.getAttribute('aria-describedby'), focus: document.activeElement === u, top: !!document.querySelector('#servers-form .servers-form-error'), posts: window.__posts, kept: u.value }; })()`);
        assert.equal(err.text, REFUSED); assert.equal(err.role, 'alert'); assert.equal(err.invalid, 'true'); assert.equal(err.by, 'servers-url-d servers-url-error', 'the URL field is described by its help, then the error');
        assert.equal(err.focus, true, 'focus goes to the URL field'); assert.equal(err.top, false, 'not also in the form\'s top error'); assert.equal(err.posts, 1); assert.equal(err.kept, 'http://example.org:8420', 'what was typed is kept');
        await shot('refused-desktop');
        // another refusal (a duplicate id) still goes to the form's top error, not next to the URL
        await ev(`window.__postAnswer = { status: 409, body: { error: 'A server with that id already exists.' } }; (() => { const f = document.getElementById('servers-form'); f.elements.api_key.value = 'k'; f.requestSubmit(); return true; })()`);
        await waitFor(`!!document.querySelector('#servers-form .servers-form-error')`, 'the top error');
        assert.equal(await ev(`!!document.getElementById('servers-url-error')`), false, 'the URL error is cleared by a new submit');
        ok('a refused http URL shows the server\'s words next to the URL field (flagged, focused, kept); other refusals stay in the top error');
        await ev(`document.querySelector('#servers-form [data-action=cancel-form]').click(); true`);

        // 4. phone, 200% text
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await ev(`document.documentElement.style.fontSize = '32px'; true`); await sleep(400); // sleep-ok: layout settles after a font-size or scroll change; there is no completion event
        const fit = await ev(`(() => { const r = document.getElementById('server-row-lan'); const w = r.querySelector('.servers-row-warn'); const wr = w.getBoundingClientRect();
            return { docOver: document.documentElement.scrollWidth - innerWidth, right: Math.round(wr.right), vw: innerWidth, sw: w.scrollWidth, cw: w.clientWidth, rowOver: r.scrollWidth - r.clientWidth }; })()`);
        assert.ok(fit.docOver <= 0 && fit.right <= fit.vw && fit.sw <= fit.cw + 1 && fit.rowOver <= 1, JSON.stringify(fit));
        await ev(`document.getElementById('server-row-lan').scrollIntoView({ block: 'center' }); true`); await sleep(200); // sleep-ok: layout settles after a font-size or scroll change; there is no completion event
        await shot('rows-phone-200');
        await ev(`document.querySelector('[data-action=add]').click(); true`);
        await waitFor(`!!document.getElementById('servers-form')`, 'the add form on the phone');
        await ev(`window.__postAnswer = { status: 400, body: { error: ${JSON.stringify(REFUSED)} } }; (() => { const f = document.getElementById('servers-form'); f.elements.id.value = 'new1'; f.elements.url.value = 'http://example.org:8420'; f.elements.api_key.value = 'k'; f.requestSubmit(); return true; })()`);
        await waitFor(`!!document.getElementById('servers-url-error')`, 'the inline URL error on the phone');
        const fit2 = await ev(`(() => { const e = document.getElementById('servers-url-error'); const r = e.getBoundingClientRect(); return { docOver: document.documentElement.scrollWidth - innerWidth, right: Math.round(r.right), vw: innerWidth, sw: e.scrollWidth, cw: e.clientWidth }; })()`);
        assert.ok(fit2.docOver <= 0 && fit2.right <= fit2.vw && fit2.sw <= fit2.cw + 1, JSON.stringify(fit2));
        await ev(`document.getElementById('servers-url-error').scrollIntoView({ block: 'center' }); true`); await sleep(200); // sleep-ok: layout settles after a font-size or scroll change; there is no completion event
        await shot('refused-phone-200');
        const chk = await ev(`(() => { const i = document.querySelector('#servers-form input[name=allow_private]'); i.scrollIntoView({ block: 'center' }); const r = i.getBoundingClientRect(); const cs = getComputedStyle(i);
            return { w: Math.round(r.width), h: Math.round(r.height), left: Math.round(r.left), right: Math.round(r.right), display: cs.display, appearance: cs.appearance, vis: i.checkVisibility({ checkVisibilityCSS: true }) }; })()`);
        assert.ok(chk.vis && chk.w >= 12 && chk.h >= 12 && chk.left >= 0 && chk.right <= 390, 'the private-network checkbox is drawn inside the screen at 200%: ' + JSON.stringify(chk));
        await shot('checkbox-phone-200');
        ok('at 390 px and 200% text the row warning and the inline URL error stay inside the screen');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
