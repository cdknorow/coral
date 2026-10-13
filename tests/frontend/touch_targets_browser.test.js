// Mobile a11y (#3073, Coral Hub mobile audit F2 + F4):  run via run_cdp_suites.mjs
// F4: on a coarse pointer (phone 390px, iPad 820px with touch) every visible
//     control is at least 44x44 CSS px; a fine pointer keeps the desktop sizes.
// F2: the expanded Agent UI panel is a named dialog with a 44px Close button;
//     Close and Escape both close it and return focus to the Expand button.
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));

// Visible, on-screen interactive elements smaller than 44px in either dimension.
const UNDERSIZED = `(() => {
    const out = [];
    for (const el of document.querySelectorAll('button, a[href], [role=button], input:not([type=hidden]), select, summary, [onclick]')) {
        const r = el.getBoundingClientRect(); const cs = getComputedStyle(el);
        if (!r.width || !r.height || cs.visibility === 'hidden' || cs.display === 'none') continue;
        if (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) continue;
        if (r.width >= 44 && r.height >= 44) continue;
        out.push(Math.round(r.width) + 'x' + Math.round(r.height) + ' ' + (el.id ? '#' + el.id : el.className) + ' [' + (el.getAttribute('aria-label') || el.title || el.textContent.trim()).slice(0, 30) + ']');
    }
    return out;
})()`;

(async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-touch-test-'));
    const bin = path.join(dir, 'coral-agent');
    execFileSync('go', ['build', '-o', bin, './cmd/coral-agent'], { cwd: path.resolve(__dirname, '../../coral-go') });
    const api = async (route, method = 'GET', body) => {
        const r = await fetch(BASE + route, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
        return r.json();
    };
    const launch = await api('/api/sessions/launch', 'POST', { working_dir: dir, agent_type: 'terminal', display_name: 'Touch QA' });
    const sid = launch.session_id;
    const sname = launch.session_name || launch.name || '';
    const env = { ...process.env, CORAL_URL: BASE, CORAL_SESSION_NAME: 'terminal-' + sid }; delete env.TMUX;
    const panelFile = path.join(dir, 'panel.html');
    fs.writeFileSync(panelFile, '<p>touch panel</p>');
    execFileSync(bin, ['ui', 'publish', '--id', 'touch', '--title', 'Touch Panel', '--file', panelFile], { env, encoding: 'utf8' });

    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        await ev(`new Promise(r => { const t = Date.now(); const w = () => (window._coralGetLiveSessions?.() || []).some(s => s.session_id === ${JSON.stringify(sid)}) || Date.now() - t > 8000 ? r() : setTimeout(w, 100); w(); })`);
        await ev(`window.selectLiveSession(${JSON.stringify(sname)}, 'terminal', ${JSON.stringify(sid)})`);
        await sleep(800);
    };
    const coarse = async on => {
        await Emulation.setEmulatedMedia({ features: on ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [{ name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }] });
        await Emulation.setTouchEmulationEnabled({ enabled: on, maxTouchPoints: on ? 5 : 1 });
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);

        // F4: coarse pointer, phone and iPad.
        await coarse(true);
        await open(390, 844, true);
        assert.ok(await ev(`!!document.querySelector('.mobile-send-btn')`), 'phone view has the composer (a session is selected)');
        assert.deepEqual(await ev(UNDERSIZED), [], 'phone 390: every control is at least 44x44');
        assert.ok(await ev(`document.documentElement.scrollWidth <= innerWidth`), 'phone 390: no horizontal overflow');
        console.log('PASS phone 390 coarse: no control under 44px, no overflow');

        await open(820, 1180, false);
        assert.deepEqual(await ev(UNDERSIZED), [], 'iPad 820: every control is at least 44x44');
        await ev(`document.querySelector('.tablet-sidebar-toggle')?.click()`); await sleep(400);
        const rows = await ev(`Array.from(document.querySelectorAll('.sidebar-kebab-btn, .folder-copy-btn')).map(e => e.getBoundingClientRect()).filter(r => r.width && r.height).map(r => [Math.round(r.width), Math.round(r.height)])`);
        assert.ok(rows.length >= 2, 'the sidebar shows row kebabs: ' + JSON.stringify(rows));
        for (const [w, h] of rows) assert.ok(w >= 44 && h >= 44, `sidebar row control ${w}x${h} under 44px`);
        console.log(`PASS iPad 820 coarse: no control under 44px; ${rows.length} sidebar row controls >=44`);

        // A fine pointer keeps the desktop look.
        await coarse(false);
        await open(1280, 800, false);
        const home = await ev(`(() => { const r = document.querySelector('.top-bar-btn').getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })()`);
        assert.ok(home[0] < 44 && home[1] < 44, 'desktop (fine pointer) top-bar buttons keep their size: ' + home);
        console.log('PASS desktop fine pointer unchanged (' + home + ')');

        // F2: the expanded Agent UI panel, on a touch tablet where the side panel is on screen.
        await coarse(true);
        await open(820, 1180, false);
        await ev(`(async () => { const ui = await import((window.__coralStatic||'/static/')+'agent_ui.js'); await ui.refreshAgentUI(); window.switchAgenticTab('agent-ui', 'top'); })()`);
        await sleep(600);
        // The panel list (home rows) is on screen before a panel is opened.
        assert.ok(await ev(`!!document.querySelector('.agent-ui-home-row') && document.querySelector('.agent-ui-home-row').getBoundingClientRect().height > 0`), 'the Agent UI panel list is shown');
        const homeSmall = await ev(UNDERSIZED);
        assert.deepEqual(homeSmall, [], 'iPad 820 with the Agent UI panel list shown: every control is at least 44x44: ' + JSON.stringify(homeSmall));
        await ev(`document.querySelector('.agent-ui-home-row').click()`); await sleep(300);
        const panelSmall = await ev(UNDERSIZED);
        assert.deepEqual(panelSmall, [], 'iPad 820 with the Agent UI panel open: every control is at least 44x44: ' + JSON.stringify(panelSmall));
        const expandOpen = `(() => { const b = Array.from(document.querySelectorAll('.agent-ui-card:not([hidden]) button')).find(x => x.getAttribute('aria-label') === 'Expand panel'); b.focus(); b.click(); })()`;
        const expandSize = await ev(`(() => { const b = Array.from(document.querySelectorAll('.agent-ui-card:not([hidden]) button')).find(x => x.getAttribute('aria-label') === 'Expand panel'); const r = b.getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })()`);
        assert.ok(expandSize[0] >= 44 && expandSize[1] >= 44, 'Expand is visible and at least 44x44: ' + expandSize);
        await ev(expandOpen); await sleep(200);
        const d = await ev(`(() => {
            const dlg = document.getElementById('agent-ui-expanded'); if (!dlg) return null;
            const label = document.getElementById(dlg.getAttribute('aria-labelledby'));
            const close = dlg.querySelector('.agent-ui-expanded-close'); const r = close.getBoundingClientRect();
            return { open: dlg.open, role: dlg.getAttribute('role'), name: label && label.textContent, closeLabel: close.getAttribute('aria-label'),
                     size: [Math.round(r.width), Math.round(r.height)], focused: document.activeElement === close, frame: !!dlg.querySelector('iframe') };
        })()`);
        assert.ok(d && d.open, 'the expanded panel opens');
        assert.equal(d.role, 'dialog');
        assert.equal(d.name, 'Touch Panel', 'the dialog is named by the panel title');
        assert.equal(d.closeLabel, 'Close Touch Panel');
        assert.ok(d.size[0] >= 44 && d.size[1] >= 44, 'Close is at least 44x44: ' + d.size);
        assert.ok(d.focused, 'focus moves to Close');
        assert.ok(d.frame, 'the panel frame is in the dialog');
        const afterClose = `({ gone: !document.getElementById('agent-ui-expanded'), back: document.activeElement && document.activeElement.getAttribute('aria-label') === 'Expand panel', frameInCard: !!document.querySelector('.agent-ui-card:not([hidden]) iframe') })`;
        await ev(`document.querySelector('#agent-ui-expanded .agent-ui-expanded-close').click()`); await sleep(100);
        assert.deepEqual(await ev(afterClose), { gone: true, back: true, frameInCard: true }, 'Close closes and returns focus to Expand');

        await ev(expandOpen); await sleep(200);
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await sleep(100);
        assert.deepEqual(await ev(afterClose), { gone: true, back: true, frameInCard: true }, 'Escape closes and returns focus to Expand');
        console.log('PASS F2 expanded panel: named dialog, 44px Close, Close/Escape return focus');
    } finally {
        await client.close();
        await api(`/api/sessions/live/${encodeURIComponent(sname)}/kill`, 'POST', { agent_type: 'terminal', session_id: sid }).catch(() => {});
        fs.rmSync(dir, { recursive: true, force: true });
    }
})().catch(e => {
    // One compact line, so the runner's output tail shows what differed.
    console.error('FAIL ' + e.message + (e.actual !== undefined ? ' | actual ' + JSON.stringify(e.actual) + ' | expected ' + JSON.stringify(e.expected) : ''));
    process.exitCode = 1;
});
