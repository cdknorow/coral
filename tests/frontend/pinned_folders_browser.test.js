// Pinned folders (#3630, the operator's design): a pinned folder is an ordinary entry in the Agents
// tab's left folder list, empty allowed, and stays when its last agent ends.
//  - Open folder on the home page and in the +New chooser opens a dialog; a folder that doesn't
//    exist is refused with the reason, an existing one is pinned and listed with no agents.
//  - The entry's menu launches into it (Add Terminal here), has Unpin folder, and no Kill All
//    while it is empty. Any other folder's menu has Pin folder: pinned, it stays when its last
//    agent is killed.
//  - Pins live on the server (a reload keeps them); a search hides an empty pin unless its name
//    matches; the phone list shows them too.
'use strict';

const CDP = require('chrome-remote-interface');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'pin3630-'));
    fs.mkdirSync(path.join(tmp, 'qa-pinned'), { recursive: true }); // (its return value is the first folder made, which Node on Windows reports as 'C:')
    const pinDir = fs.realpathSync(path.join(tmp, 'qa-pinned'));
    fs.mkdirSync(path.join(tmp, 'qa-busy'), { recursive: true });
    const busyDir = fs.realpathSync(path.join(tmp, 'qa-busy'));
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 10000) => waitFor(expr, what, { timeout: ms, soft: true });
    const open = async (width = 1400, mobile = false) => {
        await Emulation.setDeviceMetricsOverride({ width, height: mobile ? 844 : 900, deviceScaleFactor: 1, mobile });
        const l = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await l;
        await coralReady();
    };
    const api = (method, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(async r => ({ status: r.status, body: await r.json().catch(() => ({})) }))`);
    const HDR = (name, list = 'live-sessions-list') => `document.querySelector('#${list} .session-group-header[data-group-name="${name}"]')`;
    const groupInfo = (name, list) => ev(`(() => { const h = ${HDR(name, list)}; if (!h) return null;
        const items = []; for (let n = h.nextElementSibling; n && !n.classList.contains('session-group-header'); n = n.nextElementSibling) if (n.matches('.session-group-item')) items.push(n.textContent.trim().slice(0, 40));
        const menu = [...h.querySelectorAll('.sidebar-kebab-menu .overflow-menu-item')].map(b => b.textContent.trim());
        return { items, menu, count: (h.querySelector('.session-group-count') || {}).textContent || '' }; })()`);
    const menuClick = (name, label) => ev(`(() => { const h = ${HDR(name)}; h.querySelector('.group-kebab-btn').click();
        const b = [...h.querySelectorAll('.sidebar-kebab-menu .overflow-menu-item')].find(x => x.textContent.trim() === ${JSON.stringify(label)}); if (!b) return false; b.click(); return true; })()`);
    let busySid = null;
    try {
        await Page.enable();
        await open();
        const start = await api('GET', '/api/pinned-folders');
        check('no pinned folders to start with', start.status === 200 && Array.isArray(start.body.folders) && start.body.folders.length === 0, JSON.stringify(start));

        // ── Open folder from the home page ─────────────────────────────────
        const card = await ev(`(() => { const c = document.getElementById('welcome-open-folder'); return c && { text: c.textContent.replace(/\\s+/g, ' ').trim(), shown: c.getClientRects().length > 0 }; })()`);
        check('the home page has an Open folder card', card && card.shown && /Open folder/.test(card.text), JSON.stringify(card));
        await ev(`document.getElementById('welcome-open-folder').click(); true`);
        await until(`getComputedStyle(document.getElementById('open-folder-modal')).display !== 'none' && document.activeElement && document.activeElement.id === 'open-folder-dir'`, 'the Open folder dialog with focus in the path');
        const dlg = await ev(`({ shown: getComputedStyle(document.getElementById('open-folder-modal')).display !== 'none', focus: document.activeElement && document.activeElement.id,
            role: document.getElementById('open-folder-modal').getAttribute('role'), label: document.getElementById('open-folder-title').textContent })`);
        check('the dialog opens as a labelled dialog, focus in the folder field', dlg.shown && dlg.focus === 'open-folder-dir' && dlg.role === 'dialog' && dlg.label === 'Open folder', JSON.stringify(dlg));
        await ev(`document.getElementById('open-folder-dir').value = ${JSON.stringify(path.join(tmp, 'missing'))}; window.submitOpenFolder(); true`);
        await until(`!document.getElementById('open-folder-error').hidden`, 'the refusal');
        const refused = await ev(`({ err: document.getElementById('open-folder-error').textContent, open: getComputedStyle(document.getElementById('open-folder-modal')).display !== 'none' })`);
        const afterBad = await api('GET', '/api/pinned-folders');
        check('a folder that does not exist is refused with the reason, nothing pinned', /not found/i.test(refused.err) && refused.open && afterBad.body.folders.length === 0, JSON.stringify({ refused, afterBad }));
        await ev(`document.getElementById('open-folder-dir').value = ${JSON.stringify(pinDir)}; true`);
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await until(`getComputedStyle(document.getElementById('open-folder-modal')).display === 'none' && !!${HDR('qa-pinned')}`, 'the pinned folder in the list');
        let g = await groupInfo('qa-pinned');
        check('Enter pins it: the folder is listed with no agents under it', !!g && g.items.length === 0, JSON.stringify(g));
        check('its menu launches into it, has Unpin folder, and no Kill All while empty',
            !!g && ['Add Agent', 'Add Custom Agent', 'Add Terminal', 'Unpin folder'].every(x => g.menu.includes(x)) && !g.menu.includes('Kill All'), JSON.stringify(g && g.menu));
        const looks = await ev(`(() => { const a = ${HDR('qa-pinned')}; return { cls: a.className, icon: !!a.querySelector('.folder-group-icon') }; })()`);
        check('it looks like any folder entry (same header and folder icon)', looks.cls === 'session-group-header' && looks.icon, JSON.stringify(looks));

        // ── The +New chooser ───────────────────────────────────────────────
        await ev(`window.showLaunchModal(); true`);
        await until(`getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`, 'the launch chooser');
        const chooser = await ev(`(() => { const cards = [...document.querySelectorAll('#launch-step-chooser .launch-type-card')].filter(c => c.getClientRects().length);
            const tops = cards.map(c => Math.round(c.getBoundingClientRect().top));
            return { labels: cards.map(c => c.querySelector('.launch-type-label').textContent.trim()), oneRow: new Set(tops).size === 1 }; })()`);
        check('the +New chooser offers Open Folder, the four choices in one row', chooser.labels.includes('Open Folder') && chooser.labels.length === 4 && chooser.oneRow, JSON.stringify(chooser));
        await ev(`document.getElementById('launch-type-open-folder').click(); true`);
        await until(`getComputedStyle(document.getElementById('open-folder-modal')).display !== 'none'`, 'the Open folder dialog from +New');
        const fromNew = await ev(`({ dialog: getComputedStyle(document.getElementById('open-folder-modal')).display !== 'none', launch: getComputedStyle(document.getElementById('launch-modal')).display })`);
        check('choosing it closes the chooser and opens the Open folder dialog', fromNew.dialog && fromNew.launch === 'none', JSON.stringify(fromNew));
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await until(`getComputedStyle(document.getElementById('open-folder-modal')).display === 'none'`, 'Escape closes it');
        check('Escape closes the dialog', await ev(`getComputedStyle(document.getElementById('open-folder-modal')).display === 'none'`));

        // ── A reload keeps it; the search and the phone list ───────────────
        await open();
        await until(`!!${HDR('qa-pinned')}`, 'the pin after a reload');
        check('a reload keeps the pin (it lives on the server)', await ev(`!!${HDR('qa-pinned')}`));
        await ev(`window.filterLiveAgents('zzz-no-match'); true`);
        const hidden = await ev(`!${HDR('qa-pinned')}`);
        await ev(`window.filterLiveAgents('qa-pin'); true`);
        const matched = await ev(`!!${HDR('qa-pinned')}`);
        await ev(`window.filterLiveAgents(''); true`);
        check('a search hides an empty pinned folder unless its name matches', hidden && matched, JSON.stringify({ hidden, matched }));
        await open(390, true);
        await until(`!!${HDR('qa-pinned', 'mobile-session-list')}`, 'the pin in the phone list');
        g = await groupInfo('qa-pinned', 'mobile-session-list');
        check('the phone list shows it, with no agents, count 0', !!g && g.items.length === 0 && g.count === '0', JSON.stringify(g));
        await open();

        // ── Launch into it from its menu ───────────────────────────────────
        await until(`!!${HDR('qa-pinned')}`, 'the pinned folder');
        check("its menu's Add Terminal launches into the pinned folder", await menuClick('qa-pinned', 'Add Terminal'));
        await until(`(() => { const h = ${HDR('qa-pinned')}; const n = h && h.nextElementSibling; return !!n && n.matches('.session-group-item'); })()`, 'the terminal under the pinned folder', 15000);
        const live = await api('GET', '/api/sessions/live');
        const sessions = live.body.sessions || live.body;
        const launched = (sessions || []).find(s => s.working_directory === pinDir);
        check('the launched terminal runs in the pinned folder', !!launched, JSON.stringify((sessions || []).map(s => s.working_directory)));
        g = await groupInfo('qa-pinned');
        check('with an agent in it, the entry keeps Unpin folder and gets Kill All', !!g && g.menu.includes('Unpin folder') && g.menu.includes('Kill All'), JSON.stringify(g && g.menu));
        if (launched) await api('POST', `/api/sessions/live/${encodeURIComponent(launched.name)}/kill`, { session_id: launched.session_id, agent_type: 'terminal' });

        // ── Pin from an existing folder's menu; it stays when its agent ends ─
        const r = await api('POST', '/api/sessions/launch', { working_dir: busyDir, agent_type: 'terminal', display_name: 'Terminal' });
        busySid = r.body.session_id;
        await until(`!!${HDR('qa-busy')}`, 'the busy folder');
        g = await groupInfo('qa-busy');
        check('a folder with agents offers Pin folder', !!g && g.menu.includes('Pin folder') && !g.menu.includes('Unpin folder'), JSON.stringify(g && g.menu));
        await menuClick('qa-busy', 'Pin folder');
        await until(`(async () => (await (await fetch('/api/pinned-folders')).json()).folders.length === 2)()`, 'two pins');
        await api('POST', `/api/sessions/live/${encodeURIComponent(r.body.session_name)}/kill`, { session_id: busySid, agent_type: 'terminal' });
        busySid = null;
        await until(`(() => { const h = ${HDR('qa-busy')}; if (!h) return false; const n = h.nextElementSibling; return !n || !n.matches('.session-group-item') || n.textContent.includes('Done'); })()`, 'the busy folder after its agent ended', 15000);
        await open();
        await until(`!!${HDR('qa-busy')}`, 'the pinned busy folder after a reload');
        g = await groupInfo('qa-busy');
        check('pinned, it stays in the list after its last agent ends', !!g && g.items.length === 0, JSON.stringify(g));

        // ── Unpin ──────────────────────────────────────────────────────────
        await menuClick('qa-busy', 'Unpin folder');
        await until(`!${HDR('qa-busy')}`, 'the unpinned folder gone');
        await menuClick('qa-pinned', 'Unpin folder');
        await until(`!${HDR('qa-pinned')}`, 'the other gone too');
        const end = await api('GET', '/api/pinned-folders');
        check('Unpin folder removes them from the list and the server', end.body.folders.length === 0 && !(await ev(`!!${HDR('qa-busy')} || !!${HDR('qa-pinned')}`)), JSON.stringify(end));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        try {
            const pins = await api('GET', '/api/pinned-folders');
            for (const f of (pins.body.folders || [])) await api('DELETE', '/api/pinned-folders', { path: f.path });
        } catch { /* best effort */ }
        await client.close();
        fs.rmSync(tmp, { recursive: true, force: true });
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
