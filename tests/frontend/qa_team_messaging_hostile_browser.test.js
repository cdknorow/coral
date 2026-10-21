// QA #5584: the Messaging panel (team_cross_team_settings.js) against hostile and odd answers from the machine, drawn straight into a host element with mountCrossTeamSettings: the server's words and the
// linked teams are shown as text only, an unknown state or a status without words falls back to the switch, a hub caller's "can_change" must be exactly true to unlock the switches, the Hub's 404 and 403
// on save never show a raw word, and the panel keeps its two switches and its link constant whatever the state says. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const HUB = 'https://app.coralhub.dev/team-links';
    // draws the panel in a fresh host with this state, in the Hub's view or not, and reports what it shows
    const draw = (state, { hub = false, settings = { cross_team_messaging: true, cross_team_artifacts: false } } = {}) => ev(`(async () => {
        document.getElementById('qa-host')?.remove(); window.__pwn = 0;
        const host = document.createElement('div'); host.id = 'qa-host'; document.body.append(host);
        const m = await import(${S} + 'team_cross_team_settings.js'); m._setHubForTests(${hub ? 'true' : 'null'});
        await m.mountCrossTeamSettings(host, 'qa-team', { state: ${JSON.stringify(state)}, settings: ${JSON.stringify(settings)} });
        const q = s => host.querySelector(s), all = s => [...host.querySelectorAll(s)];
        const prim = q('.xt-primary'), sw = all('input[type=checkbox]');
        return { line: (q('.xt-line') || {}).textContent || '', dot: (q('.update-state-dot') || { dataset: {} }).dataset.k || '', prim: prim ? { tag: prim.tagName, act: prim.dataset.act || '', href: prim.getAttribute('href') } : null,
            switches: sw.map(c => c.name + ':' + c.checked + ':' + c.disabled), teams: all('.xt-teams li').map(li => li.textContent), imgs: host.querySelectorAll('img, script, iframe').length, bolds: host.querySelectorAll('.xt-teams b b').length,
            links: all('a').map(a => a.getAttribute('href')), pwn: window.__pwn, locked: (q('.xt-locked') || {}).textContent || '', saveHidden: !!(q('.xt-save') || {}).hidden, html: host.innerHTML.length };
    })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);

        // the server's words are text: markup in them draws nothing
        let p = await draw({ status: { state: 'ready', text: '<img src=x onerror="window.__pwn=1"><b>two</b> teams' }, links: [] });
        check(`status text with markup is text (${p.line.slice(0, 40)})`, p.line.startsWith('<img') && p.imgs === 0 && p.pwn === 0);
        p = await draw({ status: { state: 'ready', text: 'x'.repeat(5000) }, links: [] });
        check('status text is cut to 300 characters', p.line.length === 300, String(p.line.length));
        // an unknown state, a status without words, a status that is not an object: the line follows the switch, no throw
        for (const bad of [{ state: 'owned', text: 'hi' }, { state: 'ready', text: '   ' }, { state: 'ready' }, { state: 7, text: 'x' }, 'ready', 5, null, [], { state: 'ready', text: { a: 1 } }]) {
            p = await draw({ status: bad, links: [] });
            check(`status ${JSON.stringify(bad)} falls back to the switch (${p.line})`, p.line === 'Cross-team messaging is on for this team.' && p.dot === 'warm', p.dot);
        }
        // the state itself odd
        for (const st of [null, 'str', 5, [], { links: 'x' }, { links: [null, 5, 'a', [], {}] }]) {
            p = await draw(st);
            check(`state ${JSON.stringify(st)} draws the panel with both switches`, p.switches.length === 2 && p.prim !== null, JSON.stringify(p.switches));
        }
        // linked teams: text only, a bad id dropped, bounded; an object that is not a team still draws as "a team"
        p = await draw({ status: { state: 'ready', text: 'ok' }, links: [{ team_label: '<b>x</b>', server_label: '"><script>window.__pwn=2</script>', server_id: 'a b', refreshed: '<i>' }, { team_label: { a: 1 }, server_id: 5 }, { team_label: 'A'.repeat(500), server_id: 'x'.repeat(65) }] });
        check(`linked teams are text (${JSON.stringify(p.teams)})`, p.imgs === 0 && p.pwn === 0 && p.bolds === 0 && p.teams.length === 3 && p.teams.every(t => !/server a b|server x{65}/.test(t)));
        check('a long team label is cut to 80', p.teams[2].length < 120, String(p.teams[2].length));
        p = await draw({ status: { state: 'ready', text: 'ok' }, links: Array.from({ length: 500 }, (_, i) => ({ team_label: 't' + i })) });
        check('500 links: at most 50 drawn', p.teams.length === 50, String(p.teams.length));
        // the only outside link is the constant, whatever the server sent
        p = await draw({ status: { state: 'ready', text: 'ok', href: 'https://evil.example/' }, links: [{ team_label: 't', href: 'https://evil.example/', url: 'https://evil.example/' }], hub_url: 'https://evil.example/' });
        check(`every link is the Coral Hub constant (${JSON.stringify(p.links)})`, p.links.length > 0 && p.links.every(h => h === HUB));
        // through the hub: locked unless can_change is exactly true
        for (const cc of [undefined, false, 'true', 1, 'yes', null, [true], {}]) {
            p = await draw({ status: { state: 'ready', text: 'ok' }, links: [], can_change: cc }, { hub: true });
            check(`through the hub, can_change ${JSON.stringify(cc)} keeps the switches disabled with the reason and no Save`, p.switches.every(s => s.endsWith(':true')) && /can only be changed on the computer itself/.test(p.locked) && p.saveHidden === true, JSON.stringify(p.switches));
        }
        p = await draw({ status: { state: 'ready', text: 'ok' }, links: [], can_change: true }, { hub: true });
        check('through the hub, can_change true unlocks the switches and shows Save', p.switches.every(s => s.endsWith(':false')) && p.saveHidden === false && p.locked === '');
        p = await draw({ status: { state: 'off', text: 'off' }, links: [], can_change: false }, { hub: true });
        check('through the hub, off and locked: no "Turn on" button', p.prim === null || p.prim.act !== 'turn-on', JSON.stringify(p.prim));
        // settings that cannot be read
        p = await draw({}, { settings: null });
        check('settings that cannot be read: a plain sentence, no switches', p.switches.length === 0 && p.line === '');
        const none = await ev(`document.querySelector('#qa-host').textContent`);
        check(`and the sentence is ours (${none})`, /not available here|Messaging settings/.test(none));
    } catch (e) { check('suite ran without throwing', false, e && e.stack || String(e)); }
    try { await client.close(); } catch (e) { /* closing */ }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})();
