// Keyboard focus survives a background redraw (#4882): focus is on a button, a refresh replaces the node, and focus used to fall to the body, so the next Enter landed on
// nothing (a keyboard user's, and the .team-open-btn test's). The sidebar's team button, every list drawn through morphHtml (the Tasks list, Activity, the rail), and a list
// redrawn by innerHTML (Servers) under rememberFocus (dom_morph.js) all keep focus on the same control; a control that is gone does not pull focus to a stranger.
// Drives Chrome over CDP against the isolated server.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Input } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { waitFor, nextFrame } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable();
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`!!window.__coralReady || !!document.getElementById('live-sessions-list')`, 'the page');

        console.log("The sidebar's team button keeps focus when a background render replaces it, and Enter still opens the team view");
        await ev(`(async () => {
            const { state } = await import(${S}+'state.js'); const render = await import(${S}+'render.js');
            window.__state = state; window.__render = render;
            window.__sess = [{ session_id: 'fs-1', name: 'fs-1', agent_type: 'claude', board_project: 'alpha', display_name: 'Alpha agent', working_directory: '/tmp/alpha' }];
            render.renderLiveSessions(window.__sess); return true; })()`);
        // One synchronous step (the page's own live-list refresh cannot come between the focus and the redraw and take the button away first).
        // Something about the header changes (it is the selected team), so the button's own markup changes and its node is replaced.
        const after = await ev(`(() => {
            window.__state.selectedTeam = null; window.__state.liveSessions = window.__sess; window.__render.renderLiveSessions(window.__sess); // the page's own refresh may have replaced the list since the last step
            const b = document.querySelector('.team-open-btn'); b.focus();
            window.__state.selectedTeam = 'alpha'; window.__render.renderLiveSessions(window.__sess);
            const a = document.activeElement;
            const first = { replaced: b !== document.querySelector('.team-open-btn'), connected: b.isConnected, onBtn: !!a && a.classList.contains('team-open-btn') && a.dataset.board === 'alpha', tag: a && a.tagName };
            // A second redraw with the agent list itself changing (a new agent in the team) keeps it too.
            window.__sess = window.__sess.concat([{ session_id: 'fs-2', name: 'fs-2', agent_type: 'claude', board_project: 'alpha', display_name: 'Second', working_directory: '/tmp/alpha' }]);
            window.__render.renderLiveSessions(window.__sess);
            return { first, second: !!document.activeElement && document.activeElement.classList.contains('team-open-btn') }; })()`);
        assert.ok(after.first.replaced, `the redraw replaced the button's node (the case under test): ${JSON.stringify(after)}`);
        assert.ok(after.first.onBtn, `focus is on the team button after the redraw: ${JSON.stringify(after)}`);
        assert.ok(after.second, 'focus is on the team button after the team gained an agent');
        console.log('  ok');

        console.log('Lists drawn through morphHtml and through innerHTML keep focus on the same control');
        const r = await ev(`(async () => {
            const { morphHtml, rememberFocus } = await import(${S}+'dom_morph.js');
            const host = document.createElement('div'); host.id = 'fs-host'; document.body.appendChild(host);
            const rows = (sel, extra = '') => ['a', 'b', 'c'].map(k => '<div class="row' + (k === sel ? ' is-open' : '') + '" data-id="' + k + '"><span class="t">' + k + extra + '</span><button type="button" class="act" data-act="open">Open</button><button type="button" class="act" data-act="del">Delete</button></div>').join('');
            const out = {};
            morphHtml(host, rows('a')); host.querySelector('[data-id="b"] [data-act="del"]').focus();
            morphHtml(host, rows('b', ' (changed)'));   // row b's own markup changed: its node is replaced
            const f = document.activeElement; out.morph = { sameControl: f.dataset.act === 'del' && f.closest('.row').dataset.id === 'b', connected: f.isConnected };
            // a refresh where nothing changed leaves the focused node itself alone
            const keep = document.activeElement; morphHtml(host, rows('b', ' (changed)')); out.untouched = document.activeElement === keep;
            // twins: two controls with the same key, the second one focused
            host.innerHTML = '<ul>' + ['x', 'y'].map(n => '<li><button type="button" data-action="go">' + n + '</button></li>').join('') + '</ul>';
            host.querySelectorAll('button')[1].focus();
            const back = rememberFocus(host); host.innerHTML = '<ul>' + ['x2', 'y2'].map(n => '<li><button type="button" data-action="go">' + n + '</button></li>').join('') + '</ul>'; back();
            out.twin = document.activeElement.textContent;
            // the control is gone: focus is not handed to another one
            host.innerHTML = '<button type="button" id="gone-1">only</button>'; host.querySelector('button').focus();
            const back2 = rememberFocus(host); host.innerHTML = '<button type="button" id="other">other</button>'; back2();
            out.gone = document.activeElement === document.body;
            // focus outside the host is never moved
            const outside = document.createElement('button'); outside.id = 'fs-out'; document.body.appendChild(outside); outside.focus();
            morphHtml(host, rows('c')); out.outside = document.activeElement === outside;
            host.remove(); outside.remove();
            return out; })()`);
        assert.ok(r.morph.sameControl && r.morph.connected, `morphHtml: focus on the row's own Delete button: ${JSON.stringify(r)}`);
        assert.ok(r.untouched, 'a refresh with no change leaves the focused node alone');
        assert.equal(r.twin, 'y2', 'the second of two like controls gets focus back');
        assert.ok(r.gone, 'a control that is gone does not hand focus to another one');
        assert.ok(r.outside, 'focus outside the redrawn list is not moved');
        console.log('  ok');

        console.log('The real Servers list (hub) and the real Cloud servers list keep focus on a control outside a row, and the code being typed');
        const real = await ev(`(async () => {
            const realFetch = window.fetch; let remote = [{ id: 'm1', label: 'machine-1', url: 'http://10.0.0.11:8420', status: 'online', agent_count: 0 }];
            let cloud = [{ id: 'c1', label: 'laptop', online: true, allow_launch: false, last_seen: new Date().toISOString() }];
            window.__setServers = (r, c) => { remote = r; cloud = c; };
            window.fetch = (url, opts) => { const u = String(url); if (u === '/api/servers' && (!opts || !opts.method || opts.method === 'GET')) return Promise.resolve(new Response(JSON.stringify(window.__cloudShape ? { servers: cloud } : remote), { status: 200, headers: { 'Content-Type': 'application/json' } })); return realFetch(url, opts); };
            const out = {};
            const hub = await import(${S}+'servers.js'), cl = await import(${S}+'cloud_servers.js');
            const host = document.createElement('div'); host.id = 'fs-servers'; document.body.appendChild(host);
            await hub.renderServersInto(host);
            const add = host.querySelector('[data-action="add"]'); out.hasAdd = !!add; if (add) add.focus();
            window.__setServers(remote.concat([{ id: 'm2', label: 'machine-2', url: 'http://10.0.0.12:8420', status: 'offline', agent_count: 0 }]), cloud);
            await hub.renderServersInto(host);   // the periodic refresh's path: the whole list is redrawn
            out.hubKept = document.activeElement?.dataset?.action === 'add' && host.contains(document.activeElement);
            hub.hideServersModal(); host.remove();
            window.__cloudShape = true;
            const host2 = document.createElement('div'); host2.id = 'fs-cloud'; document.body.appendChild(host2);
            await cl.renderCloudServersInto(host2);
            host2.querySelector('#cs-enroll-toggle')?.click(); // "Add a machine" is folded while the account has machines (#5992): open it first
            const code = host2.querySelector('input.cloud-code'); out.hasCode = !!code;
            if (code) { code.focus(); code.value = 'AB12'; code.setSelectionRange(2, 3); }
            window.__setServers(remote, cloud.concat([{ id: 'c2', label: 'desktop', online: false, allow_launch: false, last_seen: new Date().toISOString() }]));
            await cl.renderCloudServersInto(host2);
            const again = document.activeElement; out.cloudKept = !!again && again.classList.contains('cloud-code') && host2.contains(again); out.typed = again && again.value; out.caret = again && [again.selectionStart, again.selectionEnd];
            cl.hideCloudServers(); host2.remove(); window.fetch = realFetch;
            return out; })()`);
        assert.ok(real.hasAdd && real.hubKept, `Servers: focus stays on Add server after the list is redrawn: ${JSON.stringify(real)}`);
        assert.ok(real.hasCode && real.cloudKept, `Cloud servers: focus stays on the enrollment code box: ${JSON.stringify(real)}`);
        assert.equal(real.typed, 'AB12', 'and the code typed in it is still there');
        assert.deepEqual(real.caret, [2, 3], 'with the caret where it was');
        console.log('  ok');

        console.log('A typed value goes only to the one control it came from, and never between kinds of field');
        const probe = await ev(`(async () => {
            const { rememberFocus } = await import(${S}+'dom_morph.js');
            const host = document.createElement('div'); document.body.appendChild(host);
            const out = {};
            const rows = names => '<ul>' + names.map(n => '<li><span>' + n + '</span><input type="text" class="note"></li>').join('') + '</ul>';
            // twins: a row is inserted above the focused one; the text must not move into another row's box
            host.innerHTML = rows(['A', 'B']); const second = host.querySelectorAll('input')[1]; second.focus(); second.value = 'typed in B';
            const back = rememberFocus(host); host.innerHTML = rows(['NEW', 'A', 'B']); back();
            out.twins = [...host.querySelectorAll('input')].map(i => i.value);
            // rows that carry a stable id (data-id, as the agent, server and task rows do): a row inserted above leaves focus in B's own box, not the box now at B's old place
            const keyed = names => '<ul>' + names.map(n => '<li data-id="' + n + '"><span>' + n + '</span><input type="text" class="note"></li>').join('') + '</ul>';
            host.innerHTML = keyed(['A', 'B']); const kb = host.querySelector('li[data-id="B"] input'); kb.focus();
            const back1 = rememberFocus(host); host.innerHTML = keyed(['NEW', 'A', 'B']); back1();
            out.keyedFocus = document.activeElement.closest('li')?.dataset.id;
            // a password redrawn as a text box under the same id and data-*
            host.innerHTML = '<input type="password" id="k" data-f="key">'; const pw = host.querySelector('input'); pw.focus(); pw.value = 's3cret';
            const back2 = rememberFocus(host); host.innerHTML = '<input type="text" id="k" data-f="key">'; back2();
            out.password = host.querySelector('input').value;
            // the one box with that key, redrawn as the same kind: it keeps what was typed
            host.innerHTML = '<input type="text" id="code">'; const one = host.querySelector('input'); one.focus(); one.value = 'AB12';
            const back3 = rememberFocus(host); host.innerHTML = '<input type="text" id="code">'; back3();
            out.single = host.querySelector('input').value;
            host.remove(); return out; })()`);
        assert.deepEqual(probe.twins, ['', '', ''], `a row inserted above must leave every box empty: ${JSON.stringify(probe)}`);
        assert.equal(probe.keyedFocus, 'B', `a row inserted above keeps focus in its own row's box: ${JSON.stringify(probe)}`);
        assert.equal(probe.password, '', 'a password is never copied into a text box');
        assert.equal(probe.single, 'AB12', 'a single box of the same kind keeps its text');
        console.log('  ok');

        console.log('A real Enter on the restored team button opens the team view');
        // The page's own live-list refresh can replace the list between two steps; each attempt sets the list up and focuses the button in one step, then sends the key at once.
        let opened = false;
        for (let attempt = 0; attempt < 5 && !opened; attempt++) {
            const ready = await ev(`(() => { const st = window.__state; st.selectedTeam = null; st.liveSessions = window.__sess; window.__render.renderLiveSessions(window.__sess);
                document.querySelector('.team-open-btn').focus(); st.selectedTeam = 'alpha'; window.__render.renderLiveSessions(window.__sess); st.selectedTeam = null; window.__render.renderLiveSessions(window.__sess);
                return !!document.activeElement && document.activeElement.classList.contains('team-open-btn'); })()`);
            assert.ok(ready, 'focus is still on the team button after the redraws');
            // The key goes to whatever has focus: a keyDown with the character, as a native button activates on it.
            await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13, text: '\r' });
            await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 });
            try { await waitFor(`window.__state.selectedTeam === 'alpha'`, 'the team view opened by Enter', { timeout: 2000 }); opened = true; } catch { /* the list was refreshed between the focus and the key: try again */ }
        }
        assert.ok(opened, 'a real Enter on the restored team button opened the team view');
        console.log('PASS focus survives a background refresh');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
