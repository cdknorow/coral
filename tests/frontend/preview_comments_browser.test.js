// Comments in the docked preview pane (#5311): the artifacts tab's Preview opens a tab in the pane, not the viewer dialog, and the operator could not comment there. The pane now carries the viewer's own
// comments component for the tab that is on show: beside the document when the pane is 720 px wide or more (maximised), a bottom sheet over it below that (a narrow dock, a phone), with a Comments button
// in the toolbar in both layouts; highlights for the comments, the Comment button by a selection, a posted comment; none for a file, an image or no tab; torn down when the tab goes. Real server, the comments routes
// stood in for (helpers/comments_scale.js STUB). 1280 (docked, then maximised in its column) and 390 px.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme } = require('./helpers/contrast');
const { STUB, HEX } = require('./helpers/comments_scale');
const BASE = require('./test_base').BASE;

const CONTENT = ['# Pricing', '', 'The starter plan costs nine dollars a month and includes three seats for the whole team.', '', 'The team plan costs thirty dollars a month and includes twenty seats and the audit log.', '', 'Enterprise is quoted by sales and includes single sign-on and a named contact.'].join('\n');
const FX = {
    filename: 'pricing.md', content: CONTENT, size: CONTENT.length, mediaType: 'text/markdown',
    comments: [
        { id: 1, parent_id: null, anchor: { quote: 'costs nine dollars a month', prefix: '', suffix: '' }, body: 'Is this per seat?', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 3600000).toISOString() },
        { id: 2, parent_id: null, anchor: { quote: 'costs thirty dollars a month', prefix: '', suffix: '' }, body: 'Annual discount?', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 7200000).toISOString() },
        { id: 3, parent_id: null, anchor: { quote: 'Enterprise is quoted by sales' }, body: 'Done.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 86400000).toISOString(), resolved_at: new Date().toISOString() },
    ],
};
const IMG = 'f'.repeat(64);

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout settle after a resize or a tap
    await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
    const load = async (w, h, mobile, theme) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`); await setTheme(ev, theme);
        await ev(`(async()=>{(await import(${S} + 'safe_storage.js')).browserLocal.remove('coral-artifact-comments-open');})()`);
        await ev(STUB(FX));
        // a PNG for the image artifact, on top of the stub
        await ev(`(() => { const real = window.fetch; const png = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='), c => c.charCodeAt(0));
            window.fetch = (u, o) => String(u).includes('/api/artifacts/${IMG}') && !String(u).includes('/comments') ? Promise.resolve(new Response(png, { status: 200, headers: { 'Content-Type': 'image/png' } })) : real(u, o); return true; })()`);
        await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); const { showView } = await import(st + 'utils.js');
            state.currentSession = { type: 'live', name: 'boss', session_id: 'boss-sid', agent_type: 'claude' }; showView('live-session-view'); window.PP.setPreviewSession('boss-sid'); return true; })()`);
    };
    const open = async (hex, name, type) => {
        await ev(`window.PP.openArtifactTab('coral://artifacts/' + ${JSON.stringify(hex)}, { name: ${JSON.stringify(name)}, mediaType: ${JSON.stringify(type)} }); true`);
        await waitFor(`getComputedStyle(document.getElementById('preview-pane')).display !== 'none'`, 'the pane', { timeout: 8000 });
    };
    const state = () => ev(`(() => { const main = document.getElementById('preview-main'), p = main.querySelector('.ac-panel'), t = document.querySelector('#preview-toolbar .ac-toggle'); const pr = p && p.getBoundingClientRect(); const hl = CSS.highlights && CSS.highlights.get('ac-comment');
        return { w: main.clientWidth, panelHidden: !p || p.hidden, panelPos: p && getComputedStyle(p).position, panelW: pr && Math.round(pr.width), panelInert: !!(p && p.inert), sheetOpen: !!(p && p.classList.contains('ac-sheet-open')), cards: main.querySelectorAll('.ac-card').length, toggle: t && !t.hidden ? t.textContent : null,
            highlights: hl ? hl.size : 0, bodyRegion: document.getElementById('preview-body').getAttribute('role'), calls: window.__calls }; })()`);
    try {
        for (const [w, h, mobile, theme] of [[1280, 800, false, 'dark'], [390, 844, true, 'light']]) {
            const tag = `${w}px ${theme}`;
            await load(w, h, mobile, theme);
            await open(HEX, 'pricing.md', 'text/markdown');
            await waitFor(`!!document.querySelector('#preview-body .preview-pane-markdown') && document.querySelectorAll('#preview-main .ac-card').length > 0`, `${tag}: the document and its comments`, { timeout: 10000 });
            let s = await state();
            const sheet = s.w < 720;
            check(`${tag}: the comments start open with a toolbar toggle counting two open threads`, s.toggle === 'Comments (2)' && s.sheetOpen && !s.panelInert && (sheet ? s.panelPos === 'absolute' : s.panelPos !== 'absolute'), JSON.stringify(s).slice(0,300));
            check(`${tag}: the two open comments are highlighted in the document and the document is a named region`, s.highlights === 2 && s.bodyRegion === 'region', JSON.stringify({ h: s.highlights, r: s.bodyRegion }));
            if (sheet) {
                await settle(500); // the count-based default has already opened it
                s = await state();
                check(`${tag}: the Comments button opens the sheet with its cards (and the document behind is out of reach)`, s.sheetOpen && !s.panelInert && s.cards === 2, JSON.stringify(s).slice(0, 240));
                await shot(`preview-comments-sheet-${w}`);
                await ev(`document.querySelector('#preview-main .ac-sheet-close').click(); true`); await settle(500);
                check(`${tag}: its close button shuts it`, !(await state()).sheetOpen);
            }
            // a selection in the document: the Comment button, the composer, a posted comment
            await ev(`(() => { const w = document.createTreeWalker(document.getElementById('preview-body'), NodeFilter.SHOW_TEXT); let n; while ((n = w.nextNode())) { const i = n.nodeValue.indexOf('audit log'); if (i >= 0) { const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + 9); const sel = getSelection(); sel.removeAllRanges(); sel.addRange(r); return true; } } return false; })()`);
            await waitFor(`(() => { const b = document.querySelector('#preview-pane .ac-selbtn'); return !!b && !b.hidden; })()`, `${tag}: the Comment button by the selection`, { timeout: 5000 });
            await ev(`document.querySelector('#preview-pane .ac-selbtn').click(); true`); await settle(400);
            await waitFor(`!!document.querySelector('#preview-main .ac-composer .ac-input')`, `${tag}: the composer`, { timeout: 5000 });
            check(`${tag}: the composer quotes the selected text`, /audit log/.test(await ev(`document.querySelector('#preview-main .ac-composer .ac-quote').textContent`)));
            await ev(`(() => { const ta = document.querySelector('#preview-main .ac-composer .ac-input'); ta.value = 'Mention the audit log retention.'; ta.dispatchEvent(new Event('input')); document.querySelector('#preview-main .ac-composer').requestSubmit(); return true; })()`);
            await waitFor(`window.__cs.some(c => c.body === 'Mention the audit log retention.')`, `${tag}: the comment is posted`, { timeout: 8000 });
            const posted = await ev(`window.__cs.find(c => c.body === 'Mention the audit log retention.').anchor`);
            check(`${tag}: it is posted with its quote and context`, posted && /audit log/.test(posted.quote) && posted.start_line > 0, JSON.stringify(posted));
            await waitFor(`[...document.querySelectorAll('#preview-main .ac-card .ac-body')].some(e => e.textContent === 'Mention the audit log retention.')`, `${tag}: its card`, { timeout: 8000 });
            if (!mobile) {
                // a wide screen, the pane maximised in its column: 720 px or more, so the panel is beside the document and its toolbar button stays available
                await Emulation.setDeviceMetricsOverride({ width: 1920, height: 1000, deviceScaleFactor: 1, mobile: false }); await settle(400);
                await ev(`document.getElementById('preview-dock-btn').click(); true`); await settle(500);
                await ev(`document.getElementById('preview-max-btn').click(); true`); await settle(900);
                s = await state();
                check(`${tag}: maximised on a 1920 screen (${s.w} px wide) the panel is beside the document, 340 px, with its Comments button`, s.w >= 720 && !!s.toggle && s.panelPos !== 'absolute' && s.panelW >= 330 && !s.panelInert && !s.panelHidden && s.cards >= 2 && s.highlights >= 2, JSON.stringify(s).slice(0, 260));
                await shot('preview-comments-beside-1920');
                await ev(`document.getElementById('preview-max-btn').click(); true`); await settle(500);
                await ev(`document.getElementById('preview-dock-btn').click(); true`); await settle(300);
                await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await settle(400);
            }
            // an image artifact has no comments; the pane's own tabs: switching and closing tear them down
            await open(IMG, 'logo.png', 'image/png');
            await waitFor(`!!document.querySelector('#preview-body img, #preview-body .image-pane')`, `${tag}: the image`, { timeout: 8000 });
            s = await state();
            check(`${tag}: an image artifact has no comments (no panel, no button, no highlights, the body is no region)`, s.panelHidden && !s.toggle && s.highlights === 0 && s.bodyRegion === null, JSON.stringify(s).slice(0, 240));
            await ev(`window.PP.openArtifactTab('coral://artifacts/${HEX}', { name: 'pricing.md', mediaType: 'text/markdown' }); true`);
            await waitFor(`document.querySelectorAll('#preview-main .ac-card').length > 0 && ${JSON.stringify(true)}`, `${tag}: back on the document its comments are back`, { timeout: 8000 });
            check(`${tag}: switching back brings the comments and the highlights back`, (await state()).highlights >= 2);
            await ev(`window.PP.closeAllTabs(); true`); await settle(300);
            s = await state();
            check(`${tag}: closing every tab tears the comments down (no panel, no highlights, no button)`, s.panelHidden && s.highlights === 0 && !s.toggle, JSON.stringify(s).slice(0, 200));
        }
    } finally { /* the harness closes Chrome */ }
    const failed = results.filter(r => !r).length;
    console.log(`preview_comments: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
