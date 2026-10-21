// A phone and theme pass over the 129/130 UI (#5345), at 320 and 390 px in both themes, touch: the filter icon panels of the Tasks panel and the Team Artifacts list (with the one-row header and the file-type
// icons on its rows), the two overlay notice pills, the artifact comments sheet in the docked preview pane, and the owner-only refusal messages (the team settings note, the Prompt inspection, the Health
// check's refused save). Each scene: nothing wider than the screen, nothing clipped, every control 44 px (helpers/phone_scan.js), every text 4.5:1 (helpers/contrast.js), and one screenshot
// (CORAL_SCREENSHOT_DIR). Against the isolated dev server; the comments and the owner routes are stood in for.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { SCAN } = require('./helpers/phone_scan');
const { installAudit, setTheme, auditRegion } = require('./helpers/contrast');
const { STUB: COMMENTS_STUB, HEX } = require('./helpers/comments_scale');
const PANELS = require('./helpers/list_bars_panels');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

const CONTENT = ['# Pricing', '', 'The starter plan costs nine dollars a month and includes three seats for the whole team.', '', 'The team plan costs thirty dollars a month and includes twenty seats and the audit log.'].join('\n');
const FX = {
    filename: 'pricing.md', content: CONTENT, size: CONTENT.length, mediaType: 'text/markdown',
    comments: [
        { id: 1, parent_id: null, anchor: { quote: 'costs nine dollars a month', prefix: '', suffix: '' }, body: 'Is this per seat?', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 3600000).toISOString() },
        { id: 2, parent_id: 1, anchor: null, body: 'Per team.', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a', name: 'Reviewer' }, created_at: new Date(Date.now() - 60000).toISOString() },
        { id: 3, parent_id: null, anchor: { quote: 'costs thirty dollars a month', prefix: '', suffix: '' }, body: 'Annual discount?', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 7200000).toISOString() },
    ],
};
const HEALTH = { on: false, interval_min: 10, recipient: '', all_clear: true, default_reviewers: [], auto_delegate: { on: true }, claim_nudge: { on: true, after_min: 10, nudges: 2 }, status_nudge: { on: true, after_min: 20, escalate_after_min: 10 }, mention_renudge: { on: true, after_min: 10 }, held_notice: { on: true, after_min: 10 } };
const LONG_UNREACH = 'This agent\'s server (workstation-with-a-rather-long-name.lan) is not reachable right now: the Coral Hub tunnel to it dropped. Its agents keep running; this view updates when it is back.';
const LONG_WAIT = "⏳ This agent hasn't done anything yet — check the terminal below. Some agents ask permission to trust a folder the first time they run in it.";

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const problems = [], info = [];
    const settle = (ms, quietMs = 120) => require('./helpers/wait').settle(ev, ms, quietMs); // the page's geometry still for quietMs, or ms at most (helpers/wait.js); not a fixed sleep
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const ctx = { ev, waitFor, Page, Emulation, client, install, BASE, assert, settle: (c, l) => waitFor(c, l, 10000) };
    let tag = '';
    // one scene: the page scan under `root`, the contrast of the text in it, a screenshot
    const scene = async (name, root, { scan = true } = {}) => {
        await ev(`window.__scanRoot = ${JSON.stringify(root || '')}; true`);
        if (scan) {
            const r = await ev(SCAN);
            if (r.missing) { problems.push(`${tag} ${name}: scene not found`); return; }
            for (const [what, list, n] of [['wider than the screen', r.wide, r.nWide], ['clipped', r.clipped, r.nClipped], ['smaller than 44 px', r.small, r.nSmall]]) if (n) problems.push(`${tag} ${name}: ${n} ${what}: ${list.slice(0, 4).join('; ')}`);
            if (r.docOver > 0 && !root) problems.push(`${tag} ${name}: the page scrolls sideways by ${r.docOver} px`);
        }
        const a = await auditRegion(ev, [root || 'body']);
        info.push(`${tag} ${name}: ${a.checked} checked`);
        for (const f of a.fails) problems.push(`${tag} ${name}: contrast ${f.ratio} (needs ${f.need}) ${f.what} "${f.text}" ${f.sel} ${f.fg} on ${f.bg}`);
        await shot(`${tag} ${name}`);
    };
    const view = async (w, theme) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
    };
    const load = async (w, theme) => {
        await view(w, theme);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
    };
    const team = 'p130-' + Date.now().toString(36);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const key of ['tasks', 'artifacts']) await PANELS.find(p => p.key === key).prepare?.(ctx);
        for (const w of [320, 390]) for (const theme of ['light', 'dark']) {
            tag = `${w}px ${theme}`;
            console.log(tag);
            // 1. the filter panels (Tasks, Team Artifacts): the icon panel open under a one-row header, with the file icons on the rows
            for (const key of ['tasks', 'artifacts']) {
                const panel = PANELS.find(p => p.key === key);
                await view(w, theme); ctx.__phone = true;
                await panel.show(ctx, { w, theme, phone: true });
                await setTheme(ev, theme); await installAudit(ev); await settle(500);
                await scene(`${key} with its filter panel open`, key === 'tasks' ? '#agentic-state' : '#agentic-panel-team-artifacts');
                if (key === 'artifacts') {
                    const ic = await ev(`(() => { const rows = [...document.querySelectorAll('#agentic-panel-team-artifacts tr.team-artifact-row .team-artifact-name')]; return { n: rows.length, icons: rows.filter(r => { const i = r.querySelector('.explorer-file-icon'); return i && i.getAttribute('aria-hidden') === 'true' && Math.round(i.getBoundingClientRect().width) <= 20; }).length }; })()`);
                    if (!ic.n || ic.icons !== ic.n) problems.push(`${tag} artifacts: every name starts with its small hidden file icon (${JSON.stringify(ic)})`);
                }
            }
            // 2. the two overlay pills, with their longest texts, over an agent's view
            await load(w, theme);
            await ev(`window.__m.add('p130-bot', 'A', 4, { sleeping: false }); true`); await settle(500);
            await ev(`window.selectLiveSession('p130-bot', 'claude', 'p130-bot-id', 'local'); true`); await settle(1200);
            for (const [id, text] of [['server-unreachable-banner', LONG_UNREACH], ['waiting-banner', LONG_WAIT]]) {
                await ev(`import(${S} + 'dom_write.js').then(m => { for (const x of ['server-unreachable-banner', 'waiting-banner']) m.setPill(document.getElementById(x), false, ''); m.setPill(document.getElementById(${JSON.stringify(id)}), true, ${JSON.stringify(text)}); return true; })`);
                await waitFor(`!document.getElementById(${JSON.stringify(id)}).hidden`, `${tag} ${id}`, 4000); await settle(200);
                await scene(`the ${id} pill`, '.live-pills-stack');
                const box = await ev(`(() => { const b = document.getElementById(${JSON.stringify(id)}).getBoundingClientRect(); return { l: b.left, r: b.right, vw: innerWidth }; })()`);
                if (box.l < 0 || box.r > box.vw) problems.push(`${tag} ${id}: the pill is outside the screen ${JSON.stringify(box)}`);
            }
            // 3. the comments sheet in the docked preview pane
            await load(w, theme);
            await ev(COMMENTS_STUB(FX));
            await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); const { showView } = await import(st + 'utils.js');
                state.currentSession = { type: 'live', name: 'boss', session_id: 'boss-sid', agent_type: 'claude' }; showView('live-session-view'); window.PP.setPreviewSession('boss-sid');
                window.PP.openArtifactTab('coral://artifacts/' + ${JSON.stringify(HEX)}, { name: 'pricing.md', mediaType: 'text/markdown' }); return true; })()`);
            await waitFor(`!!document.querySelector('#preview-body .preview-pane-markdown') && document.querySelectorAll('#preview-main .ac-card').length > 0`, `${tag} the document and its comments`, 10000); await settle(500);
            await scene('the preview pane with its Comments button', '#preview-pane');
            await ev(`document.querySelector('#preview-toolbar .ac-toggle').click(); true`); await settle(600);
            await scene('the preview pane with the comments sheet open', '#preview-pane');
            await ev(`document.querySelector('#preview-main .ac-whole').click(); true`); await settle(400);
            await scene('the comments sheet with its composer', '#preview-pane');
            // 4. the owner-only refusal messages (team settings note, Prompt inspection, the Health check's refused save), stood in for
            const R = (status, code) => ({ status, code, error: 'x' });
            for (const [label, ans, expect] of [['team settings note', { settings: R(403, 'mode_too_low') }, '#team-settings-messaging .ct-unavailable'], ['Prompt inspection', { inspection: R(404, 'not_found') }, '.prompt-inspection-content'], ['Health check save', { put: R(403, 'owner_unverified') }, '#team-settings-health .health-state']]) {
                await load(w, theme);
                await ev(`(() => { const R0 = window.fetch; window.__ans = ${JSON.stringify(ans)}; const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
                    const refuse = a => json({ code: a.code, error: 'x' }, a.status);
                    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase(); const p = url.pathname;
                        if (p.startsWith('/api/board/') && p.endsWith('/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
                        if (p.startsWith('/api/board/') && p.endsWith('/working-mode/presets')) return json({ presets: [{ id: 'shared_checkout', name: 'Shared checkout', builtin: true, instructions: 'Share one checkout.' }] });
                        if (p.startsWith('/api/board/') && p.endsWith('/settings')) { const a = m === 'PUT' ? window.__ans.put : window.__ans.settings; if (a) return refuse(a); return json({ cross_team_messaging: false, cross_team_artifacts: false, health: ${JSON.stringify(HEALTH)} }); }
                        if (p === '/api/settings/prompt-inspection') { if (window.__ans.inspection) return refuse(window.__ans.inspection); return json({ orchestrator: { scope: 'global', applies: 'x', default: 'd', effective: 'e' }, worker: {}, }); }
                        if (p === '/api/settings' && m === 'GET') return json({ settings: { board_health_monitor: 'true' } });
                        return R0(u, o); }; return true; })()`);
                await ev(`window.__m.add('p130-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(team)} }); true`); await settle(600);
                await ev(`window.selectLiveSession('p130-bot', 'claude', 'p130-bot-id', 'local'); true`); await settle(900);
                await ev(`(async () => { const m = await import(${S} + 'team_working_mode.js'); await m.showTeamWorkingMode(${JSON.stringify(team)}, { workspace: true }); window.switchAgenticTab('team-settings', 'top'); return true; })()`);
                await ev(`(() => { const side = document.getElementById('agentic-state'); if (side.getBoundingClientRect().width === 0) window.toggleAgenticPanel(true); return true; })()`); // a phone shows the side panel as a drawer
                await waitFor(`document.getElementById('agentic-panel-team-settings').getClientRects().length > 0`, `${tag} the team settings are on screen`, 8000);
                await waitFor(`!!document.querySelector('#agentic-panel-team-settings .working-mode-form') && document.querySelector('#agentic-panel-team-settings .working-mode-form').dataset.initialState !== undefined`, `${tag} the team settings`, 10000); await settle(500);
                if (label === 'Health check save') {
                    await ev(`document.getElementById('team-settings-tab-health').click(); true`);
                    await waitFor(`!!document.querySelector('#team-settings-health form')`, `${tag} the Health check form`, 8000);
                    await ev(`(() => { const f = document.querySelector('#team-settings-health form'); const on = f.elements.on; on.checked = !on.checked; on.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
                    await ev(`document.querySelector('#team-settings-health form [type=submit]').click(); true`);
                    await waitFor(`document.querySelector('#team-settings-health .health-state').classList.contains('health-error')`, `${tag} the refusal`, 6000);
                }
                const said = await ev(`(() => { const e = document.querySelector(${JSON.stringify('#agentic-panel-team-settings ' + expect.replace('#team-settings-health', '#team-settings-health'))}) || document.querySelector(${JSON.stringify(expect)}); return e ? e.textContent.trim() : ''; })()`);
                if (!said || /undefined|\[object/.test(said)) problems.push(`${tag} ${label}: no refusal sentence shown (${JSON.stringify(said)})`);
                await scene(`the owner refusal: ${label}`, '#agentic-panel-team-settings');
            }
        }
        if (process.env.PASS_REPORT) { fs.writeFileSync(process.env.PASS_REPORT, problems.join('\n') + '\n'); fs.writeFileSync(process.env.PASS_REPORT + '.info', info.join('\n') + '\n'); console.log(problems.length + ' problems'); }
        else assert.deepEqual(problems, [], 'phone and theme pass problems:\n  ' + problems.join('\n  '));
        console.log('\nPASS phone and theme pass over the 130 UI at 320 and 390 px, both themes');
    } finally { await removeBoards(ev, [ctx.TBOARD, team].filter(Boolean)).catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
