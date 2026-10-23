// The one-time "update is ready" notice (#6292, the UI half of #6290): when a verified update that this install can apply is staged, the dashboard says so once per version
// ("Nothing is installed until you say yes"); Review opens the confirmation, Later closes the notice and changes nothing (no request, the top-bar control stays). It is never drawn
// where the control is not offered (Homebrew or unsupported, no run record, Coral Hub off, busy, nothing staged) or on a dashboard served through Coral Hub (the updater UI is off there).
// Desktop and 390 px, both themes, 4.5:1 contrast, 44 px buttons on a touch screen. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const { setTheme, auditRegion } = require('./helpers/contrast');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

const staged = (version = '1.4.160', over = {}, relaunch = {}) => ({ enabled: true, installed: '1.4.159', state: 'staged', staged: { version, staged_at: '2026-10-23T03:00:00Z', days_ready: 0, verified: true },
    relaunch: { available: true, reason: '', hub_allowed: false, restarts: [], tmux_agents_restart: false, ack: 'ack-1', ...relaunch }, last_result: null, checked_at: new Date().toISOString(), auto: true, ...over });

// the server's status carries the repository inside `staged`, only for a verified update (#6290)
const stagedFrom = (source, relaunch = {}) => { const st = staged('1.4.160', {}, relaunch); st.staged.source = source; return st; };

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const settle = (ms, quietMs = 120) => require('./helpers/wait').settle(ev, ms, quietMs);
    const STUB = `(() => {
        const real = window.fetch;
        window.__upd = window.__upd || { status: null, calls: [] };
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => {
            const url = String(u), method = (o && o.method) || 'GET';
            if (url.includes('/api/cloud/status')) return json({ connected: false, hub_relaunch_updates: false });
            if (url.includes('/api/system/update')) {
                window.__upd.calls.push({ method, url: url.replace(/^.*\\/api\\/system\\/update/, '') || '/status' });
                if (url.endsWith('/api/system/update')) { const answer = () => (window.__upd.status ? json(window.__upd.status) : json({ error: 'none' }, 404)); return window.__upd.delay ? new Promise(r => setTimeout(() => r(answer()), window.__upd.delay)) : answer(); }
                return json({ ok: true });
            }
            return real(u, o);
        };
        return true; })()`;
    const boot = async (view, { flag = true } = {}) => {
        await install();
        await ev(`window.__coralReady.then(() => true)`);
        if (flag) await ev(`import(${S} + 'update_flag.js').then(m => { m.setUpdatesEnabled(true); return true; })`);
        await ev(STUB);
        await setTheme(ev, view.theme);
    };
    const load = async (view, { flag = true } = {}) => {
        await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
        await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }, { name: 'prefers-reduced-motion', value: 'reduce' }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await boot(view, { flag });
    };
    const setStatus = async (st) => {
        await ev(`window.__upd.status = ${JSON.stringify(st)}; import(${S} + 'update_ui.js').then(m => m.startUpdateUi && m.startUpdateUi()).then(() => import(${S} + 'update_ui.js')).then(m => m.refreshUpdateStatus()).then(() => true)`);
        await settle(200);
    };
    const notice = () => ev(`(() => { const b = document.getElementById('update-ready-notice'); if (!b) return null; const r = b.getBoundingClientRect(); const bt = [...b.querySelectorAll('button')].map(x => { const q = x.getBoundingClientRect(); return { text: x.textContent.trim(), w: Math.round(q.width), h: Math.round(q.height) }; });
        return { shown: b.querySelector('.update-ready-lead').textContent !== '' && r.width > 0, text: b.textContent.replace(/\\s+/g, ' ').trim(), role: b.querySelector('[role=status]') && b.querySelector('[role=status]').getAttribute('role'), buttons: bt, inside: r.left >= -1 && r.right <= innerWidth + 1, hscroll: document.documentElement.scrollWidth > innerWidth + 1 }; })()`);
    const calls = () => ev(`window.__upd.calls.map(c => c.method + ' ' + c.url)`);
    const pillShown = () => ev(`(() => { const p = document.getElementById('update-pill'); return !!p && !p.hidden && p.getBoundingClientRect().width > 0; })()`);

    const guard = await guardSettings();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const VIEWS = [{ name: 'desktop dark', w: 1280, h: 900, phone: false, theme: 'dark' }, { name: 'desktop light', w: 1280, h: 900, phone: false, theme: 'light' },
            { name: '390px dark', w: 390, h: 844, phone: true, theme: 'dark' }, { name: '390px light', w: 390, h: 844, phone: true, theme: 'light' }];
        for (const view of VIEWS) {
            const tag = view.name;
            console.log(`\n${tag}`);
            await load(view);
            await setStatus(staged());
            let n = await notice();
            check(`${tag}: a verified update that can be applied is announced`, n && n.shown, n && n.text);
            check(`${tag}: it names the version, says it is downloaded and checked, and that nothing installs without a yes`, n && n.text.includes('Coral 1.4.160 is ready.') && n.text.includes('downloaded and checked') && n.text.includes('Nothing is installed until you say yes.'));
            check(`${tag}: it is a polite status, not an alert`, n && n.role === 'status');
            check(`${tag}: Review and Later, inside the window, no sideways scroll`, n && n.buttons.map(b => b.text).join() === 'Review,Later' && n.inside && !n.hscroll);
            if (view.phone) check(`${tag}: both buttons are 44 px on a touch screen (${JSON.stringify(n.buttons)})`, n.buttons.every(b => b.h >= 44 && b.w >= 44));
            const au = await auditRegion(ev, ['#update-ready-notice']);
            check(`${tag}: 4.5:1 text contrast (${JSON.stringify((au.fails || []).slice(0, 2))})`, (au.fails || []).length === 0);

            // the same version is not said again
            await setStatus(staged());
            check(`${tag}: a second read of the same status does not announce it again`, (await notice()).shown === true);

            // Later closes it and changes nothing
            const before = await calls();
            await ev(`document.querySelector('#update-ready-notice .update-ready-later').click(); true`); await settle(150);
            n = await notice();
            check(`${tag}: Later closes the notice`, n && !n.shown);
            check(`${tag}: and sends nothing but status reads, and the control stays`, (await calls()).slice(before.length).every(c => c === 'GET /status') && await pillShown());
            await setStatus(staged());
            check(`${tag}: it does not come back for that version`, !(await notice()).shown);
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await boot(view);
            await setStatus(staged());
            check(`${tag}: nor after a reload`, !(await notice()).shown && await pillShown());

            // a newer version is a new notice; Review opens the confirmation and sends nothing by itself
            await setStatus(staged('1.4.161'));
            n = await notice();
            check(`${tag}: a newer version is announced`, n && n.shown && n.text.includes('Coral 1.4.161 is ready.'));
            const b2 = await calls();
            await ev(`document.querySelector('#update-ready-notice .update-ready-review').click(); true`); await settle(250);
            check(`${tag}: Review opens the confirmation and closes the notice`, !(await notice()).shown && (await ev(`getComputedStyle(document.getElementById('update-confirm-modal')).display`)) === 'flex');
            check(`${tag}: and nothing was sent but status reads`, (await calls()).slice(b2.length).every(c => c === 'GET /status'));
        }

        console.log('\na notice waiting for a hidden tab shows the moment the tab is seen');
        await load({ name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' });
        await ev(`Object.defineProperty(document, 'hidden', { configurable: true, get: () => true }); true`); // a tab nobody is looking at
        await setStatus(staged('1.4.170'));
        check('while the tab is hidden nothing is drawn', !(await notice()).shown);
        await ev(`window.__upd.delay = 3000; Object.defineProperty(document, 'hidden', { configurable: true, get: () => false }); document.dispatchEvent(new Event('visibilitychange')); true`); // the status read that follows takes 3 s
        await settle(200);
        const seen = await notice();
        check('seen again: the notice is there at once, not after the next read (the read is 3 s away)', seen && seen.shown && seen.text.includes('Coral 1.4.170 is ready.'), seen && seen.text);
        await ev(`window.__upd.delay = 0; true`);

        console.log('\nthe confirmation says what is installed, from where, what restarts and that nothing installs without a yes');
        const RELEASES = 'https://github.com/subgentic/coral';
        const dialogWords = async (st) => {
            await load({ name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' });
            await setStatus(st);
            await ev(`document.getElementById('update-pill').click(); true`); await settle(300);
            return ev(`(() => { const m = document.getElementById('update-confirm-modal'); const p = m.querySelector('.modal-body p'); const a = [...m.querySelectorAll('a')];
                return { title: m.querySelector('h3').textContent, first: p.textContent, links: a.map(x => ({ href: x.getAttribute('href'), target: x.target, rel: x.rel })), warn: (m.querySelector('.update-warn-body p') || {}).textContent || '', later: m.querySelector('.update-later-note').textContent, buttons: [...m.querySelectorAll('.modal-footer button')].map(b => b.textContent), foreign: m.querySelectorAll('img, script, iframe').length, text: m.textContent.replace(/\\s+/g, ' ') }; })()`);
        };
        const agents3 = [{ session_id: 'a', name: 'api', state: 'working', resumes: true }, { session_id: 'b', name: 'docs', state: 'idle', resumes: true }, { session_id: 'c', name: 'new', state: 'idle', resumes: false }];
        let dw = await dialogWords(stagedFrom('subgentic/coral', { restarts: agents3 }));
        check('title names the version and what happens', dw.title === 'Install Coral 1.4.160 and relaunch?', dw.title);
        check('it says where the update comes from and that its signature was checked, and what you are on', dw.first === "Coral 1.4.160 comes from Coral's releases on GitHub (github.com/subgentic/coral). It was downloaded and its signature was checked. You are on 1.4.159.", dw.first);
        check('its one link is this page\'s own constant, opening safely', dw.links.length === 1 && dw.links[0].href === RELEASES && dw.links[0].target === '_blank' && /noopener/.test(dw.links[0].rel), JSON.stringify(dw.links));
        check('the link sits inside its sentence: the paragraph is one block, the link inline (no line break around it)', await ev(`(() => { const p = document.querySelector('#update-confirm-modal .modal-body p'); const a = p.querySelector('a'); return getComputedStyle(a).display === 'inline' && p.getClientRects().length === 1; })()`));
        check('agents that restart: how many start again on their own and how many start a new conversation', /Relaunching restarts 3 agents that run in Coral's own terminal \(pty\)\. Their work in progress stops\. 2 start again on their own afterwards; 1 starts a new conversation\./.test(dw.warn), dw.warn);
        check('the consent line: nothing installs until Install, Later changes nothing', dw.later === 'Nothing is installed until you choose Install. Later changes nothing: the update stays ready and the button stays in the top bar.', dw.later);
        check('the buttons are Later and Install and restart 3 agents', dw.buttons.join() === 'Later,Install and restart 3 agents', dw.buttons.join());
        check('no claim that it installs at the next start, and none that the page comes back by itself', !/next time Coral starts|installs when|installed automatically|comes back by itself/i.test(dw.text));
        // a repository the status names that is not this page's: shown as text, never a link; a hostile or absent field is ignored
        dw = await dialogWords(stagedFrom('other/repo'));
        check('another repository is not named at all: just GitHub, no address, no link', dw.links.length === 0 && dw.first === "Coral 1.4.160 comes from Coral's releases on GitHub. It was downloaded and its signature was checked. You are on 1.4.159." && !/other|repo/.test(dw.first), dw.first);
        dw = await dialogWords(stagedFrom('<img src=x onerror=window.__pwned=1>'));
        check('a hostile source is ignored: no link, no element, just GitHub', dw.links.length === 0 && dw.foreign === 0 && !/\(/.test(dw.first) && /releases on GitHub\./.test(dw.first) && (await ev('window.__pwned')) === undefined, dw.first);
        dw = await dialogWords(staged('1.4.160', {}, { restarts: [] }));
        check('no source and no agents: the line still reads and the button is Install and relaunch', /Coral's releases on GitHub\. It was downloaded/.test(dw.first) && dw.buttons.join() === 'Later,Install and relaunch' && dw.warn === '', dw.buttons.join());

        console.log('\nSettings: the sentence under the Coral Hub relaunch switch');
        await load({ name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' });
        await setStatus(staged());
        const ON = 'On: the owner of this Coral at Coral Hub can install the downloaded update on this computer without anyone here saying yes. Agents restart when it does.';
        const OFF = 'Off: updates install only when someone at this computer chooses to.';
        await ev(`(async () => { await window.showSettingsModal('network'); return true; })()`); await settle(500);
        const hub = () => ev(`(() => { const b = document.getElementById('settings-cloud-hub-relaunch'), t = document.getElementById('settings-cloud-hub-relaunch-state'); return b && t ? { on: b.checked, text: t.textContent, desc: b.getAttribute('aria-describedby') } : null; })()`);
        let h = await hub();
        check('the sentence follows the switch as it stands', h && h.text === (h.on ? ON : OFF), h && h.text);
        check('the switch is described by it', h && /settings-cloud-hub-relaunch-state/.test(h.desc), h && h.desc);
        await ev(`(() => { const b = document.getElementById('settings-cloud-hub-relaunch'); b.checked = !b.checked; b.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(300);
        const h2 = await hub();
        check('turning it the other way changes the sentence at once', h2.on !== h.on && h2.text === (h2.on ? ON : OFF), h2.text);
        await ev(`(() => { const b = document.getElementById('settings-cloud-hub-relaunch'); b.checked = !b.checked; b.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(300);
        check('and back', (await hub()).text === (h.on ? ON : OFF));
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);
        // the Updates pane says the same under its "Relaunch from Coral Hub" row (the stub's saved switch is off)
        await ev(`(async () => { await window.showSettingsModal('updates'); return true; })()`); await settle(500);
        const row = await ev(`(() => { const r = [...document.querySelectorAll('#settings-modal .settings-row')].find(x => /Relaunch from Coral Hub/.test(x.textContent)); return r ? r.textContent.replace(/\\s+/g, ' ').trim() : null; })()`);
        check('the Updates pane row carries the Off sentence when the switch is off', row && row.includes(OFF), row);
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);

        console.log('\nwhere it is never drawn');
        const view = { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' };
        const unverified = { staged: { version: '1.4.162', staged_at: '2026-10-23T03:00:00Z', days_ready: 0, verified: false } };
        const CASES = [
            ['Homebrew or an install that cannot update itself', staged('1.4.162', { install_kind: 'homebrew', upgrade_command: 'brew upgrade coral' }, { available: false, reason: 'unsupported_install' })],
            ['no run record (Coral cannot restart itself here)', staged('1.4.162', {}, { available: false, reason: 'no_run_record' })],
            ['Coral Hub relaunching off for this machine', staged('1.4.162', {}, { available: false, reason: 'hub_off' })],
            ['a relaunch already running', staged('1.4.162', {}, { available: false, reason: 'busy' })],
            ['an update not verified', staged('1.4.162', unverified)],
            ['nothing staged', { enabled: true, installed: '1.4.159', state: 'none', staged: null, relaunch: { available: false, reason: 'nothing_staged', restarts: [], ack: '' }, last_result: null }],
            ['the updater switched off', staged('1.4.162', { enabled: false })],
        ];
        for (const [what, st] of CASES) {
            await load(view);
            await setStatus(st);
            check(`never for ${what}`, !(await notice()).shown);
        }
        await load(view, { flag: false });
        await ev(`window.__upd.status = ${JSON.stringify(staged('1.4.162'))}; true`);
        await ev(`import(${S} + 'update_ui.js').then(m => m.startUpdateUi && m.startUpdateUi()).then(() => true)`); await settle(300);
        check('never on a dashboard that does not run the updater UI (served through Coral Hub: the flag never turns on), and nothing is fetched', !(await notice()).shown && (await calls()).length === 0, JSON.stringify(await calls()));
    } finally { await guard.restore(); await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} update ready notice checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
