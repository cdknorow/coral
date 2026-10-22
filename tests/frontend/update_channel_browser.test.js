// Release channels in Settings > Updates and the update toast (#5558, design #5551, prototype #5554), against a stub of the status API: the channel group (Approved the default, Agentic), "Approved now" with its age
// only when the release carries an approval time, "already past the Approved release", the "Waiting for approval" section (collapsed, only on Agentic, at most five, each with the tag to type, the notes as plain text, a link
// that is a constant), the choice saved with Apply as PUT /api/settings { update_channel } and nothing when unchanged, a refused save, a server that sends no channel (no group), the Hub's read-only line (no radios, no
// waiting list), and the toast for an Agentic release ("Not yet approved"). Desktop and 390 px, both themes, 44 px targets, 4.5:1 text. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const APPROVE_URL = 'https://github.com/subgentic/coral/actions/workflows/approve-release.yml';
const NOW = Date.now(), day = n => new Date(NOW - n * 86400000).toISOString();
const base = { enabled: true, installed: '1.4.141', state: 'none', staged: null, relaunch: { available: false, reason: 'nothing_staged', restarts: [], ack: '' }, last_result: null, checked_at: new Date().toISOString(), auto: true, not_ready_reason: 'up_to_date' };
const approvedSt = { ...base, channel: 'approved', approved: { version: '1.4.142', approved_at: day(2) } };
const NOTES = 'Changes\n- Messaging tab drawn with the strip\n<img src=x onerror="window.__pwn=1"> <b>bold</b>';
const agenticSt = { ...base, channel: 'agentic', approved: { version: '1.4.142', approved_at: day(2) }, waiting: [{ version: '1.4.144', published_at: day(8), notes: NOTES }, { version: '1.4.143', published_at: day(3), notes: '' }] };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const settle = (ms, quietMs = 120) => require('./helpers/wait').settle(ev, ms, quietMs); // the page's geometry still for quietMs, or ms at most (helpers/wait.js); not a fixed sleep
    const press = async (key, code, vk) => { const text = key === 'Enter' ? '\r' : key === ' ' ? ' ' : undefined; await Input.dispatchKeyEvent({ type: text ? 'keyDown' : 'rawKeyDown', key, code, windowsVirtualKeyCode: vk, text }); await Input.dispatchKeyEvent({ type: 'keyUp', key, code, windowsVirtualKeyCode: vk }); };
    const STUB = `(() => { const real = window.fetch; window.__ch = window.__ch || { status: null, puts: [], putStatus: 200, check: null };
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => { const url = String(u), method = (o && o.method) || 'GET';
            if (url.includes('/api/cloud/status')) return json({ connected: false, hub_relaunch_updates: false });
            if (url.endsWith('/api/system/update')) return window.__ch.status ? json(window.__ch.status) : json({ error: 'none' }, 404);
            if (url.includes('/api/system/update-check')) return json(window.__ch.check || { available: false });
            if (method === 'PUT' && url.endsWith('/api/settings')) { const b = JSON.parse(o.body); window.__ch.puts.push(b); return json(b.update_channel ? { error: 'refused' } : { ok: true }, b.update_channel ? window.__ch.putStatus : 200); }
            return real(u, o); }; return true; })()`;
    const load = async (view, status, { hub = false } = {}) => {
        await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
        await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }, { name: 'prefers-reduced-motion', value: 'reduce' }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'update_flag.js').then(m => { m.setUpdatesEnabled(true); return true; })`);
        await installAudit(ev); await setTheme(ev, view.theme);
        await ev(STUB);
        await ev(`window.__ch.status = ${JSON.stringify(status)}; import(${S} + 'update_ui.js').then(m => m.startUpdateUi && m.startUpdateUi()).then(() => import(${S} + 'update_ui.js')).then(m => m.refreshUpdateStatus()).then(() => true)`);
        if (hub) await ev(`import(${S} + 'update_settings.js').then(m => m._setHubForTests(true)).then(() => true)`); // the Hub's view of the same pane (the page itself is not booted as the Hub)
        await ev(`window.showSettingsModal('updates'); true`);
        await waitFor(`!document.getElementById('settings-pane-updates')?.hidden && !!document.querySelector('#update-pane-body .update-status, #update-pane-body .update-managed')`, 'the Updates pane', 8000);
        await settle(200);
    };
    const pane = () => ev(`(() => { const q = s => document.querySelector('#update-pane-body ' + s); const all = s => [...document.querySelectorAll('#update-pane-body ' + s)];
        const w = q('.update-waiting');
        return { group: !!q('.update-channel'), radios: all('input[name=update-channel]').map(r => ({ v: r.value, checked: r.checked })), hubLine: (q('.update-channel .tb-sub') || {}).textContent || '', calm: all('.update-channel > .update-calm').filter(e => !e.hidden).map(e => e.textContent),
            waiting: !!w, open: w ? w.open : null, summary: w ? w.querySelector('summary').textContent : '', cards: w ? [...w.querySelectorAll('.update-status')].map(c => ({ title: c.querySelector('b').textContent, sub: c.querySelector('.update-status-text p').textContent, tag: (c.querySelector('code') || {}).textContent || '', href: c.querySelector('a').getAttribute('href'), target: c.querySelector('a').target, rel: c.querySelector('a').rel, label: c.querySelector('a').getAttribute('aria-label'), dot: c.dataset.k, notes: (c.querySelector('.update-notes pre') || {}).textContent || null, notesImgs: c.querySelectorAll('.update-notes img, .update-notes b').length })) : [],
            pwn: !!window.__pwn }; })()`);
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    const views = [{ name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' }, { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'light' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'dark' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'light' }];
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const view of views) {
            const tag = `${view.name} ${view.theme}`; console.log(tag);
            // 1. Approved (the default)
            await load(view, approvedSt);
            let p = await pane();
            check(`${tag}: Approved is the choice, with "Approved now" and its age, and no waiting list (${JSON.stringify(p.calm)})`, p.group && p.radios.map(r => `${r.v}:${r.checked}`).join() === 'approved:true,agentic:false' && p.calm[0] === 'Approved now: v1.4.142, approved 2 days ago.' && !p.waiting);
            check(`${tag}: the automatic-check note is not shown on Approved`, p.calm.every(t => !/still uses Approved/.test(t)));
            await shot(`channel-approved-${view.name}-${view.theme}`);
            // an Approved release with no approval time says no age; past the approved release says so
            await ev(`window.__ch.status = ${JSON.stringify({ ...approvedSt, approved: { version: '1.4.142', approved_at: '' }, past_approved: true })}; import(${S} + 'update_ui.js').then(m => m.refreshUpdateStatus()).then(() => import(${S} + 'update_settings.js')).then(m => m.showUpdatesPane()).then(() => true)`); await settle(300);
            p = await pane();
            check(`${tag}: no approval time: no age is claimed; past the approved release it says so (${JSON.stringify(p.calm)})`, p.calm[0] === 'Approved now: v1.4.142.' && p.calm.some(t => /already past the Approved release/.test(t)));
            // a server that sends no channel: no group
            await load(view, base);
            check(`${tag}: a server that sends no channel: no channel group`, !(await pane()).group);

            // 2. Agentic with releases waiting
            await load(view, agenticSt);
            p = await pane();
            check(`${tag}: Agentic is the choice; the section is collapsed and says how many wait (${p.summary})`, p.radios.map(r => `${r.v}:${r.checked}`).join() === 'approved:false,agentic:true' && p.waiting && p.open === false && p.summary === 'Waiting for approval (2)');
            check(`${tag}: the automatic-check note shows on Agentic`, p.calm.some(t => /Checking for updates automatically still uses Approved releases only/.test(t)));
            await click('#update-pane-body .update-waiting > summary'); await settle(200);
            p = await pane();
            const c0 = p.cards[0], c1 = p.cards[1];
            check(`${tag}: opened, each card says the version, its age and the tag to type (${c0.title} | ${c0.sub} | ${c0.tag})`, p.open && c0.title === 'Coral 1.4.144' && /Published 8 days ago \(over a week\), waiting for approval/.test(c0.sub) && c0.tag === 'v1.4.144' && c1.title === 'Coral 1.4.143' && /Published 3 days ago, waiting/.test(c1.sub) && c1.tag === 'v1.4.143');
            check(`${tag}: the link is the constant approval page, in a new tab with noopener, and says so in its name (${c0.href})`, p.cards.every(c => c.href === APPROVE_URL && c.target === '_blank' && /noopener/.test(c.rel) && /opens a new tab/.test(c.label)));
            check(`${tag}: a week or more is red, three days amber, with the words as well (${c0.dot}, ${c1.dot})`, c0.dot === 'bad' && c1.dot === 'warm');
            await click('#update-pane-body .update-notes > summary'); await settle(150);
            p = await pane();
            check(`${tag}: the notes are plain text: markup in them is shown as text, never drawn (${p.notesImgs} elements, pwn ${p.pwn})`, p.cards[0].notes === NOTES && p.cards[0].notesImgs === 0 && !p.pwn && p.cards[1].notes === null);
            await shot(`channel-agentic-waiting-${view.name}-${view.theme}`);
            const g = await ev(`(() => { const pane = document.getElementById('update-pane-body'); const t = [...pane.querySelectorAll('.update-waiting > summary, .update-notes > summary, .update-act, label.tb-opt')].filter(e => e.getClientRects().length).map(e => Math.round(e.getBoundingClientRect().height)); return { over: Math.max(0, pane.scrollWidth - pane.clientWidth), docOver: Math.max(0, document.documentElement.scrollWidth - innerWidth), minTarget: Math.min(...t) }; })()`);
            check(`${tag}: nothing wider than the pane or the page (${g.over}, ${g.docOver})`, g.over <= 0 && g.docOver <= 0);
            if (view.phone) check(`${tag}: every control is 44 px or more (${g.minTarget})`, g.minTarget >= 44);
            const au = await auditRegion(ev, ['#update-pane-body'], { focus: ['.update-waiting > summary', '.update-act', '#uc-approved'] });
            check(`${tag}: readable (4.5:1) with a focus ring (${au.checked} checked)`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
            // five at most, a version that is not one is dropped
            await ev(`window.__ch.status = ${JSON.stringify({ ...agenticSt, waiting: [...Array.from({ length: 6 }, (_, i) => ({ version: `1.4.${150 + i}`, published_at: day(1), notes: 'n' })), { version: '1.4.1"><img src=x>', published_at: day(1), notes: '' }] })}; import(${S} + 'update_ui.js').then(m => m.refreshUpdateStatus()).then(() => import(${S} + 'update_settings.js')).then(m => m.showUpdatesPane()).then(() => true)`); await settle(300);
            p = await ev(`document.querySelector('#update-pane-body .update-waiting > summary').textContent + '|' + document.querySelectorAll('#update-pane-body .update-waiting .update-status').length`);
            check(`${tag}: at most five are drawn and a version that is not a version is dropped (${p})`, p === 'Waiting for approval (5)|5');

            // 3. the choice: the list follows the radios, Apply saves one PUT, nothing when unchanged
            await load(view, agenticSt);
            await click('#uc-approved'); await settle(150);
            check(`${tag}: choosing Approved takes the waiting list and the note away; choosing Agentic brings them back`, !(await pane()).waiting && (await (async () => { await click('#uc-agentic'); await settle(150); const q = await pane(); return q.waiting && q.calm.some(t => /still uses Approved/.test(t)); })()));
            await ev(`window.__ch.puts.length = 0; window.applySettings(); true`); await settle(500);
            check(`${tag}: Apply with the channel unchanged sends no update_channel (${JSON.stringify(await ev('window.__ch.puts.filter(b => "update_channel" in b)'))})`, (await ev('window.__ch.puts.filter(b => "update_channel" in b).length')) === 0); // the dialog's own settings may go out; the channel must not
            await click('#uc-approved'); await ev(`window.__ch.puts.length = 0; window.applySettings(); true`); await waitFor(`window.__ch.puts.some(b => 'update_channel' in b)`, `${tag}: the save`, 6000);
            check(`${tag}: Apply saves exactly {"update_channel":"approved"} as its own request (${JSON.stringify(await ev('window.__ch.puts.filter(b => "update_channel" in b)'))})`, JSON.stringify(await ev('window.__ch.puts.filter(b => "update_channel" in b)')) === '[{"update_channel":"approved"}]');
            await load(view, approvedSt);
            await click('#uc-agentic'); await ev(`window.__ch.putStatus = 400; window.__ch.puts.length = 0; window.applySettings(); true`); await waitFor(`window.__ch.puts.some(b => 'update_channel' in b)`, `${tag}: the refused save`, 6000); await settle(400);
            check(`${tag}: a refused save is said in a toast and the dialog stays open`, await ev(`!!document.querySelector('.toast.error, .toast-error, #toast-container .error') || /Failed to save/.test(document.body.innerText)`) && await ev(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`));
            // keyboard: the radios are native; the summary opens with Enter
            await load(view, agenticSt);
            await ev(`document.querySelector('#update-pane-body .update-waiting > summary').focus(); true`); await press('Enter', 'Enter', 13); await settle(150);
            check(`${tag}: the waiting section opens from the keyboard`, (await pane()).open === true);

            // 4. the Hub: a line of text, no radios, no waiting list, even when the server sent one
            await load(view, agenticSt, { hub: true });
            p = await pane();
            check(`${tag}: through the Hub: "${p.hubLine}", no radios, no waiting list`, p.group && p.hubLine === 'Channel: Agentic (set on this computer)' && p.radios.length === 0 && !p.waiting);
            await shot(`channel-hub-${view.name}-${view.theme}`);
        }
        // 5. the toast, once (desktop dark)
        await load(views[0], agenticSt);
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); window.__ch.check = { available: true, latest: '1.4.143', current: '1.4.141', channel: 'agentic', approved: false, release_url: 'https://github.com/subgentic/coral/releases/tag/v1.4.143', release_notes: '- one\\n- two' }; import(${S} + 'update_check.js').then(m => m.checkForUpdates(true)).then(() => true)`);
        await waitFor(`!!document.getElementById('update-toast')`, 'the toast', 6000);
        let t = await ev(`document.getElementById('update-toast').innerText.replace(/\\s+/g, ' ')`);
        check(`the Agentic toast says the channel and "Not yet approved" (${t})`, /Coral v1\.4\.143 available \(Agentic channel\)/.test(t) && /Not yet approved\./.test(t));
        await shot('toast-agentic');
        await ev(`document.getElementById('update-toast').remove(); window.__ch.check = { available: true, latest: '1.4.143', current: '1.4.141', channel: 'agentic', approved: true, release_url: 'https://github.com/subgentic/coral/releases/tag/v1.4.143', release_notes: '- one' }; import(${S} + 'update_check.js').then(m => m.checkForUpdates(true)).then(() => true)`);
        await waitFor(`!!document.getElementById('update-toast')`, 'the toast again', 6000);
        t = await ev(`document.getElementById('update-toast').innerText.replace(/\\s+/g, ' ')`);
        check(`an approved release on Agentic says the channel and not "Not yet approved" (${t})`, /\(Agentic channel\)/.test(t) && !/Not yet approved/.test(t));
        await ev(`document.getElementById('update-toast').remove(); window.__ch.check = { available: true, latest: '1.4.143', current: '1.4.141', release_url: 'https://github.com/subgentic/coral/releases/latest', release_notes: '- one' }; import(${S} + 'update_check.js').then(m => m.checkForUpdates(true)).then(() => true)`);
        await waitFor(`!!document.getElementById('update-toast')`, 'the toast, no channel', 6000);
        t = await ev(`document.getElementById('update-toast').innerText.replace(/\\s+/g, ' ')`);
        check(`a server with no channel field: the toast is as before (${t})`, /Coral v1\.4\.143 available \(you have v1\.4\.141\)/.test(t) && !/Agentic|Not yet/.test(t));
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed}/${results.length} checks passed`);
        if (failed) process.exitCode = 1;
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
