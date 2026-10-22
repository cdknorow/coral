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
const RELEASES = 'https://coralhub.dev/coral.html#download'; // the download page, not GitHub's releases (#5927)
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
    'srv-avail': { level: 'available', label: 'Update', text: 'Update available: 1.5.0', shown: 'Update to 1.5.0' }, // the panel says "Update to 1.5.0" beside the one Update button (#5487); the badge's title, the tooltip and the header keep the full notice
    'srv-avail-bare': { level: 'available', label: 'Update', text: 'Update available' },
    'srv-req': { level: 'required', label: 'Update required', text: 'Update required: this Coral (1.3.9) is too old for some Coral Hub features. Update to 1.5.0.' },
    'srv-req-bare': { level: 'required', label: 'Update required', text: 'Update required: this Coral is too old for some Coral Hub features. Update to the latest release.' },
    // Fields that are not a version are not shown at all (#4133): the generic text, nothing the Hub sent.
    'srv-hostile': { level: 'required', label: 'Update required', text: 'Update required: this Coral is too old for some Coral Hub features. Update to the latest release.' },
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
    // Where a notice shows in the agent list (#3085, #4932 removed the search-row pill): with Group by
    // server on, each server header's marker and its ⋮ details panel (text + the constant link).
    const notices = (scope = '#live-sessions-list, .sidebar-header') => ev(`(() => {
        const out = { groups: {} };
        for (const h of document.querySelectorAll('li.server-group-header[data-server-group]')) {
            const panel = h.querySelector('details[data-server-details] .server-switcher-panel');
            const upd = panel && panel.querySelector('.server-details-update');
            const badge = upd && upd.querySelector('a.server-update-badge');
            out.groups[h.dataset.serverGroup] = {
                marker: h.querySelector('.server-group-toggle .server-update-marker')?.className || null,
                markerText: h.querySelector('.server-group-toggle .server-update-marker')?.textContent || null,
                updText: upd ? upd.childNodes[0].textContent.trim() : null,
                badge: badge ? { cls: badge.className, text: badge.textContent, title: badge.getAttribute('title'), aria: badge.getAttribute('aria-label'), href: badge.getAttribute('href'), target: badge.getAttribute('target'), rel: badge.getAttribute('rel') } : null,
                links: panel ? [...panel.querySelectorAll('a[href]')].map(a => a.getAttribute('href')) : [],
                hover: h.querySelector('summary.server-group-more-btn')?.getAttribute('title') || '',
                foreign: h.querySelectorAll('img, script, iframe').length,
            };
        }
        return out;
    })()`);
    const grouped = on => ev(`(() => { localStorage.setItem('coral-group-by-server', ${on ? "'true'" : "'false'"}); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);

    const checkNotices = async where => {
        await grouped(true);
        let n = await notices();
        for (const sv of SERVERS) assert.ok(n.groups[sv.id], `${where}: ${sv.id} has a server header`);
        for (const [id, want] of Object.entries(EXPECT)) {
            const g = n.groups[id];
            assert.equal(g.marker, `server-update-marker server-update-${want.level}`, `${where}: ${id} header marker`);
            assert.equal(g.markerText, want.label, `${where}: ${id} header marker text`);
            assert.equal(g.updText, want.shown || want.text, `${where}: ${id} details show the notice ("Update to <version>" when there is one)`);
            assert.ok(g.badge, `${where}: ${id} details carry the release link`);
            assert.equal(g.badge.cls, `server-update-badge server-update-${want.level}`);
            assert.equal(g.badge.title, want.text); assert.equal(g.badge.aria, want.text);
            assert.equal(g.badge.href, RELEASES, `${where}: ${id} links to the releases page`);
            assert.equal(g.badge.target, '_blank'); assert.equal(g.badge.rel, 'noopener noreferrer');
            assert.deepEqual(g.links, [RELEASES], `${where}: ${id} panel has exactly one link, the constant one`);
            assert.ok(g.hover.split('\n').includes(want.text), `${where}: ${id} ⋮ tooltip has the notice line`);
        }
        for (const id of NONE) {
            assert.equal(n.groups[id].marker, null, `${where}: no marker for ${id}`);
            assert.equal(n.groups[id].updText, null, `${where}: no notice in ${id}'s details`);
        }
        assert.ok(!n.groups.local || n.groups.local.marker === null, `${where}: the local (hub) header never has a marker`);
        assert.ok(Object.values(n.groups).every(g => g.foreign === 0), `${where}: server-sent text creates no elements`);
        assert.equal(await ev('window.__pwned'), undefined, `${where}: hostile version strings did not run`);
        await grouped(false);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); true`);

        console.log('1. Agent list: server headers and their details (the search-row pill was removed, #4932)');
        await setup();
        await checkNotices('desktop');
        console.log('   ✓ header markers and details only for available/required, exact texts, constant link, tooltips, hostile text inert');

        console.log('2. Coral Hub server list (Servers tab)');
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
            for (const main of document.querySelectorAll('#servers-tab-body .cs-row')) {
                const li = document.getElementById(main.id + '-detail'); // the notice is in the row's details
                li.dataset.id = main.dataset.id;
                const note = li.querySelector('.cs-update');
                out[li.dataset.id] = note ? {
                    cls: note.className, text: note.querySelector('span').textContent.trim(),
                    links: [...note.querySelectorAll('a')].map(a => ({ href: a.getAttribute('href'), target: a.getAttribute('target'), rel: a.getAttribute('rel'), text: a.textContent })),
                    foreign: li.querySelectorAll('img, script, iframe').length,
                } : null;
                out[li.dataset.id + ':disabled'] = [...main.querySelectorAll('button, select'), ...li.querySelectorAll('button, select')].filter(b => b.disabled).length;
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
            assert.deepEqual(n.links, [{ href: RELEASES, target: '_blank', rel: 'noopener noreferrer', text: 'Download the latest Coral' }], `dialog: ${id} link`);
            assert.equal(n.foreign, 0, `dialog: ${id} hostile text inert`);
        }
        for (const id of NONE) assert.equal(dialog[id], null, `dialog: no note for ${id}`);
        for (const sv of SERVERS) assert.equal(dialog[sv.id + ':disabled'], 0, `dialog: nothing disabled on ${sv.id}`);
        assert.equal(await ev('window.__pwned'), undefined, 'dialog: hostile strings did not run');
        console.log('   ✓ server list notes and link for available/required only, nothing disabled');

        console.log('3. Phone width: a server header carries the marker (the search-row pill was removed, #4932)');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await setup();
        // The phone list groups the agents it has by server, so put an agent on an update-required server for its header to show there.
        await ev(`localStorage.setItem('coral-group-by-server', 'true'); window.__m.add('a-srv-req', 'A', 3, { sleeping: false, server: 'srv-req' }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        await ev(`(async () => { const mob = await import((window.__coralStatic||'/static/')+'mobile.js'); mob.syncMobileAgentList(); return true; })()`);
        await waitFor(`!!document.querySelector('#mobile-agent-list li.server-group-header[data-server-group="srv-req"] .server-group-toggle .server-update-marker')`, 'phone server header marker', 3000);
        const ph = await ev(`(() => { const m = document.querySelector('#mobile-agent-list li.server-group-header[data-server-group="srv-req"] .server-group-toggle .server-update-marker'); const r = m.getBoundingClientRect(); return { marker: m.textContent, inside: r.width > 0 && r.right <= innerWidth + 1 }; })()`);
        assert.ok(/Update/.test(ph.marker), `phone: a server header carries the update marker (${ph.marker})`);
        assert.ok(ph.inside, 'phone: the marker is inside the viewport');
        await Emulation.clearDeviceMetricsOverride();
        console.log('   ✓ phone server header with the marker, inside the viewport');

        console.log('\nAll server update notice checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
