// Cloud Hub update notice (#3059): a servers entry's update field shows a badge in
// the server strip, a line in the server tooltip and a note in the Cloud Hub
// Servers dialog, only for 'available' and 'required'. Every link is the fixed
// releases URL (never built from a server field), server-sent text is inert, and
// nothing is disabled. Desktop and phone width.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const RELEASES = 'https://github.com/cdknorow/coral/releases/latest';
const HOSTILE_V = '<img src=x onerror="window.__pwned=1">';
const HOSTILE_L = '"><script>window.__pwned=2</script><a href="javascript:window.__pwned=3">';

const SERVERS = [
    { id: 'srv-current', label: 'Current', status: 'online', update: 'current', coral_version: '1.5.0', latest_coral: '1.5.0' },
    { id: 'srv-avail', label: 'Avail', status: 'online', update: 'available', coral_version: '1.4.3', latest_coral: '1.5.0' },
    { id: 'srv-avail-bare', label: 'AvailBare', status: 'online', update: 'available' },
    { id: 'srv-req', label: 'Req', status: 'online', update: 'required', coral_version: '1.3.9', latest_coral: '1.5.0' },
    { id: 'srv-req-bare', label: 'ReqBare', status: 'offline', update: 'required' },
    { id: 'srv-unknown', label: 'Unknown', status: 'online', update: 'unknown', coral_version: '0.0.0-dev+abc' },
    { id: 'srv-odd', label: 'Odd', status: 'online', update: 'UPGRADE NOW', latest_coral: '9.9.9' },
    { id: 'srv-hostile', label: 'Hostile', status: 'online', update: 'required', coral_version: HOSTILE_V, latest_coral: HOSTILE_L },
];
const EXPECT = {
    'srv-avail': { level: 'available', label: 'Update', text: 'Update available: 1.5.0' },
    'srv-avail-bare': { level: 'available', label: 'Update', text: 'Update available' },
    'srv-req': { level: 'required', label: 'Update required', text: 'Update required: this Coral (1.3.9) is too old for some Cloud Hub features. Update to 1.5.0.' },
    'srv-req-bare': { level: 'required', label: 'Update required', text: 'Update required: this Coral is too old for some Cloud Hub features. Update to the latest release.' },
    'srv-hostile': { level: 'required', label: 'Update required', text: `Update required: this Coral (${HOSTILE_V}) is too old for some Cloud Hub features. Update to ${HOSTILE_L}.` },
};
const NONE = ['srv-current', 'srv-unknown', 'srv-odd'];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();

    const setup = () => ev(`(() => {
        window.__pwned = undefined;
        T.state.hub = true;
        T.state.servers = ${JSON.stringify(SERVERS)};
        window.__m.add('local-agent', 'L', 3, { sleeping: false });
        T.render.renderLiveSessions(T.state.liveSessions);
        return true;
    })()`);
    // What the strip shows for each server row.
    const strip = (listSel = '#live-sessions-list') => ev(`(() => {
        const out = {};
        for (const li of document.querySelectorAll('${listSel} .server-strip-item')) {
            const badges = [...li.querySelectorAll('a.server-update-badge')];
            out[li.dataset.server] = {
                title: li.getAttribute('title'),
                badges: badges.map(a => ({ cls: a.className, text: a.textContent, title: a.getAttribute('title'), aria: a.getAttribute('aria-label'),
                    href: a.getAttribute('href'), target: a.getAttribute('target'), rel: a.getAttribute('rel') })),
                links: [...li.querySelectorAll('a[href]')].map(a => a.getAttribute('href')),
                foreign: li.querySelectorAll('img, script, iframe').length,
            };
        }
        return out;
    })()`);

    const checkStrip = async (where, listSel) => {
        const s = await strip(listSel);
        for (const sv of SERVERS) assert.ok(s[sv.id], `${where}: ${sv.id} has a strip row`);
        for (const [id, want] of Object.entries(EXPECT)) {
            const row = s[id];
            assert.equal(row.badges.length, 1, `${where}: ${id} has one update badge`);
            const b = row.badges[0];
            assert.equal(b.cls, `server-update-badge server-update-${want.level}`, `${where}: ${id} badge class`);
            assert.equal(b.text, want.label, `${where}: ${id} badge text`);
            assert.equal(b.title, want.text, `${where}: ${id} badge title is the exact notice`);
            assert.equal(b.aria, want.text, `${where}: ${id} badge aria-label`);
            assert.equal(b.href, RELEASES, `${where}: ${id} links to the releases page`);
            assert.equal(b.target, '_blank'); assert.equal(b.rel, 'noopener noreferrer');
            assert.ok(row.title.split('\n').includes(want.text), `${where}: ${id} tooltip has the notice line`);
            assert.deepEqual(row.links, [RELEASES], `${where}: ${id} has exactly one link, the constant one`);
        }
        for (const id of NONE) {
            assert.equal(s[id].badges.length, 0, `${where}: no badge for ${id}`);
            assert.ok(!/Update (available|required)/.test(s[id].title), `${where}: no notice in ${id}'s tooltip`);
        }
        assert.ok(Object.values(s).every(r => r.foreign === 0), `${where}: server-sent text creates no elements`);
        assert.equal(s.local && s.local.badges.length, 0, `${where}: the local (hub) row never has a badge`);
        assert.equal(await ev('window.__pwned'), undefined, `${where}: hostile version strings did not run`);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); true`);

        console.log('1. Server strip and tooltip');
        await setup();
        await checkStrip('desktop', '#live-sessions-list');
        // Server badges on agent rows use the same hover text.
        await ev(`(() => { window.__m.add('remote-agent', 'R', 3, { sleeping: false, server: 'srv-req' }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        const badgeTitle = await ev(`document.querySelector('#live-sessions-list .server-badge[data-server="srv-req"]')?.getAttribute('title') || ''`);
        assert.ok(badgeTitle.split('\n').includes(EXPECT['srv-req'].text), `a remote agent's server badge tooltip carries the notice (${JSON.stringify(badgeTitle)})`);
        console.log('   ✓ badges only for available/required, exact texts, constant link, tooltips, hostile text inert');

        console.log('2. Cloud Hub server list (Servers tab)');
        const dialog = await ev(`(async () => {
            const real = window.fetch;
            window.fetch = (u, o) => String(u).startsWith('/api/servers')
                ? Promise.resolve(new Response(JSON.stringify({ servers: ${JSON.stringify(SERVERS.map(({ id, label, status }) => ({ id, label, online: status === 'online', last_seen: '2026-10-13T00:00:00Z', access_mode: 'view' })))} }), { status: 200, headers: { 'content-type': 'application/json' } }))
                : real(u, o);
            const cs = await import((window.__coralStatic||'/static/')+'cloud_servers.js');
            // The Cloud Hub's server list, as the Servers tab renders it (#3070).
            await cs.renderCloudServersInto(document.getElementById('servers-tab-body'));
            await new Promise(r => setTimeout(r, 200));
            const out = {};
            for (const li of document.querySelectorAll('#servers-tab-body .cs-row')) {
                const note = li.querySelector('.cs-update');
                out[li.dataset.id] = note ? {
                    cls: note.className, text: note.querySelector('span').textContent.trim(),
                    links: [...note.querySelectorAll('a')].map(a => ({ href: a.getAttribute('href'), target: a.getAttribute('target'), rel: a.getAttribute('rel'), text: a.textContent })),
                    foreign: li.querySelectorAll('img, script, iframe').length,
                } : null;
                out[li.dataset.id + ':disabled'] = [...li.querySelectorAll('button, select')].filter(b => b.disabled).length;
            }
            cs.hideCloudServers();
            window.fetch = real;
            return out;
        })()`);
        for (const [id, want] of Object.entries(EXPECT)) {
            const n = dialog[id];
            assert.ok(n, `dialog: ${id} has an update note`);
            assert.equal(n.cls, `cs-note cs-update cs-update-${want.level}`);
            assert.equal(n.text, want.text, `dialog: ${id} text`);
            assert.deepEqual(n.links, [{ href: RELEASES, target: '_blank', rel: 'noopener noreferrer', text: 'Get the latest release' }], `dialog: ${id} link`);
            assert.equal(n.foreign, 0, `dialog: ${id} hostile text inert`);
        }
        for (const id of NONE) assert.equal(dialog[id], null, `dialog: no note for ${id}`);
        for (const sv of SERVERS) assert.equal(dialog[sv.id + ':disabled'], 0, `dialog: nothing disabled on ${sv.id}`);
        assert.equal(await ev('window.__pwned'), undefined, 'dialog: hostile strings did not run');
        console.log('   ✓ server list notes and link for available/required only, nothing disabled');

        console.log('3. Phone width: the agent list clone shows the same badges');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await setup();
        await ev(`(async () => { const mob = await import((window.__coralStatic||'/static/')+'mobile.js'); mob.syncMobileAgentList(); return true; })()`);
        await waitFor(`!!document.querySelector('#mobile-session-list .server-strip-item')`, 'mobile strip', 3000);
        await checkStrip('phone', '#mobile-session-list');
        const fits = await ev(`[...document.querySelectorAll('#mobile-session-list a.server-update-badge')].every(a => { const r = a.getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth + 1; })`);
        assert.ok(fits, 'phone: badges are visible and inside the viewport');
        await Emulation.clearDeviceMetricsOverride();
        console.log('   ✓ phone list shows the same badges, inside the viewport');

        console.log('\nAll server update notice checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
