// What a screen reader gets from the live dashboard (#4360, from the #4356 audit): no icon glyph is exposed as text (every visible .material-icons is
// aria-hidden, itself or through an ancestor), and every control a person can focus has a name that is not an icon's ligature. The markup written in
// templates and scripts is covered by icon_aria_hidden.test.mjs; this reads the DOM the page actually builds, including icons a script creates.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const AUDIT = `(() => { const root = window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body; if (!root) return { missing: true };
    const hiddenByAncestor = e => { for (let p = e; p; p = p.parentElement) if (p.getAttribute && p.getAttribute('aria-hidden') === 'true') return true; return false; };
    const vis = e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden' && getComputedStyle(e).display !== 'none';
    const name = e => { const al = e.getAttribute('aria-label'); if (al && al.trim()) return al.trim(); const by = e.getAttribute('aria-labelledby'); if (by) { const t = by.split(/\\s+/).map(id => (document.getElementById(id) || {}).textContent || '').join(' ').trim(); if (t) return t; }
        const clone = e.cloneNode(true); clone.querySelectorAll('[aria-hidden="true"], .material-icons').forEach(n => n.remove()); const own = (clone.textContent || '').replace(/\\s+/g, ' ').trim(); if (own) return own;
        const ph = e.getAttribute('placeholder'); if (ph && ph.trim()) return ph.trim(); const ti = e.getAttribute('title'); return (ti || '').trim(); };
    const id = e => (e.id ? '#' + e.id : e.tagName.toLowerCase() + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/).slice(0, 2).join('.') : ''));
    const loud = [...root.querySelectorAll('.material-icons')].filter(e => vis(e) && !hiddenByAncestor(e) && !e.getAttribute('aria-label')).map(e => id(e) + ' "' + e.textContent.trim() + '"');
    const unnamed = [...root.querySelectorAll('button, a[href], summary, [role=button], [role=tab], select, input:not([type=hidden])')].filter(e => vis(e) && !hiddenByAncestor(e) && !e.disabled && !name(e) && !(e.id && document.querySelector('label[for="' + CSS.escape(e.id) + '"]')) && !e.closest('label')).map(id);
    return { loud: loud.slice(0, 6), nLoud: loud.length, unnamed: unnamed.slice(0, 6), nUnnamed: unnamed.length }; })()`;

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: views and dialogs fill from several answers and settle over frames; there is no completion event
    const problems = [];
    const scan = async (name, root) => {
        await ev(`window.__scanRoot = ${JSON.stringify(root || '')}; true`);
        const r = await ev(AUDIT);
        if (r.missing) { problems.push(`${name}: scene not found`); return; }
        if (r.nLoud) problems.push(`${name}: ${r.nLoud} icon(s) read as text: ${r.loud.join('; ')}`);
        if (r.nUnnamed) problems.push(`${name}: ${r.nUnnamed} control(s) with no name: ${r.unnamed.join('; ')}`);
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('icon-bot', 'A', 6, { sleeping: false }); true`); await sleep(900); // sleep-ok: views and dialogs fill from several answers; there is no completion event
        await scan('welcome (no agent open)');
        for (const tab of ['history', 'workflows', 'jobs', 'tokens', 'docs', 'board']) { await ev(`window.switchNavTab('${tab}'); true`); await sleep(900); await scan('tab ' + tab); } // sleep-ok: views and dialogs fill from several answers; there is no completion event
        await ev(`window.switchNavTab('agents'); window.selectLiveSession('icon-bot', 'claude', 'icon-bot-id', 'local'); true`); await sleep(1500); // sleep-ok: views and dialogs fill from several answers; there is no completion event
        await scan('agent chat and its header');
        await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await sleep(700); // sleep-ok: views and dialogs fill from several answers; there is no completion event
        for (const tab of ['files', 'browse', 'artifacts', 'tasks', 'notes', 'activity']) { await ev(`window.switchAgenticTab('${tab}', 'top'); true`); await sleep(700); await scan('side panel ' + tab, '#agentic-state'); } // sleep-ok: views and dialogs fill from several answers; there is no completion event
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await sleep(1500); // sleep-ok: views and dialogs fill from several answers; there is no completion event
        await scan('settings', '#settings-modal'); await ev(`window.hideSettingsModal(); true`);
        await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await sleep(900); // sleep-ok: views and dialogs fill from several answers; there is no completion event
        await scan('launch dialog', '#launch-modal');
        if (problems.length) throw new Error('\n' + problems.join('\n'));
        console.log('   ✓ no icon is read as text and every control is named, on the welcome screen, every top tab, the agent view, the side panel and the Settings and Launch dialogs');
    } finally { await client.close(); }
})().catch(e => { console.error(e.message || e); process.exitCode = 1; });
