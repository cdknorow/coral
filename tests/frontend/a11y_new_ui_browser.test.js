// An accessibility pass over the views added on Oct 19 (#5252): the artifact viewer with its comments panel and sheet, the multi-select card of "Claude is asking", the preview's full screen and the Display
// panel's hint. At desktop width and at 390 px, in both themes: keyboard only (Tab, Shift+Tab, Enter, Space, Escape) and the accessibility tree (CDP Accessibility.getFullAXTree): every control reachable and named,
// a closed sheet not reachable, checkboxes with their state from the terminal, a comment reachable from its highlight and the highlight from its card, focus returned to the opener on close and kept on a control
// that is redrawn, a posted comment and a submitted answer announced once, and 4.5:1 text contrast. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const { stubFetch } = require('./helpers/comments_stub');
const BASE = require('./test_base').BASE;
const HEX = 'd'.repeat(64);
const MD = ['# Migration plan', '', '## Steps', '', '1. Add the new columns behind a flag and backfill in batches of 500 rows.', '2. Switch reads to the new columns.', '', '## Risks', '', 'The backfill takes a write lock on large databases, so it must run off-peak.', '', 'Add a flag. Then wait. Add a flag again at the end.'].join('\n');
const LABELS = ['Feels like a slideshow', "Doesn't show the game", 'Tone/story is off', 'Looks cheap/amateur', 'Type something'];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select, firstPage } = await connect();
    const { Input, Accessibility, DOM } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const pageSettle = require('./helpers/wait').bind(ev).settle;
    const settle = ms => ms >= 300 ? pageSettle(ms) : new Promise(r => setTimeout(r, ms)); // sleep-ok: a pause under 300 ms after a key stays fixed (nothing to wait for); a longer one ends as soon as the page's geometry holds still (helpers/wait settle, #4768), at most ms
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };

    // ── keys, as a keyboard sends them
    const KEYS = { Tab: [9, 'Tab'], Enter: [13, 'Enter'], ' ': [32, 'Space'], Escape: [27, 'Escape'], ']': [221, 'BracketRight'], '[': [219, 'BracketLeft'] };
    const press = async (key, mods = 0) => {
        const [code, name] = KEYS[key];
        const text = key === 'Enter' ? '\r' : key.length === 1 ? key : undefined;
        await Input.dispatchKeyEvent({ type: text ? 'keyDown' : 'rawKeyDown', key, code: name, windowsVirtualKeyCode: code, modifiers: mods, text });
        await Input.dispatchKeyEvent({ type: 'keyUp', key, code: name, windowsVirtualKeyCode: code, modifiers: mods });
        await settle(60);
    };
    const type = async text => { await Input.insertText({ text }); await settle(40); };
    const focused = () => ev(`(() => { const a = document.activeElement; if (!a || a === document.body) return { none: true };
        const r = a.getBoundingClientRect(); const cs = getComputedStyle(a);
        return { tag: a.tagName.toLowerCase(), cls: String(a.className || '').split(' ').slice(0, 2).join('.'), id: a.id || '', label: (a.getAttribute('aria-label') || a.textContent || a.placeholder || '').trim().replace(/\\s+/g, ' ').slice(0, 40),
            onScreen: r.width > 0 && r.height > 0 && r.right > 0 && r.left < innerWidth && r.bottom > 0 && r.top < innerHeight, hidden: cs.visibility === 'hidden' || !a.getClientRects().length, ariaChecked: a.getAttribute('aria-checked') }; })()`);
    const desc = f => f.none ? 'nothing' : `${f.tag}${f.id ? '#' + f.id : ''}.${f.cls} "${f.label}"`;

    // ── the accessibility tree under a root: the interactive nodes with their name, role and state
    const INTERACTIVE = new Set(['button', 'textbox', 'checkbox', 'link', 'combobox', 'tab', 'searchbox', 'switch', 'radio', 'menuitem']);
    const axUnder = async sel => {
        await DOM.enable(); await Accessibility.enable();
        const { root } = await DOM.getDocument({ depth: 0 });
        const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: sel });
        if (!nodeId) return null;
        const { node } = await DOM.describeNode({ nodeId });
        const { nodes } = await Accessibility.getFullAXTree();
        const byId = new Map(nodes.map(n => [n.nodeId, n]));
        const start = nodes.find(n => n.backendDOMNodeId === node.backendNodeId);
        if (!start) return { nodes: [], missing: true };
        const out = []; const walk = n => { if (!n.ignored) out.push(n); for (const c of n.childIds || []) { const k = byId.get(c); if (k) walk(k); } }; walk(start);
        const prop = (n, k) => { const p = (n.properties || []).find(x => x.name === k); return p ? p.value.value : undefined; };
        return { nodes: out.map(n => ({ role: n.role && n.role.value, name: (n.name && n.name.value) || '', checked: prop(n, 'checked'), live: prop(n, 'live'), expanded: prop(n, 'expanded'), pressed: prop(n, 'pressed'), id: n.backendDOMNodeId })) };
    };
    const unnamed = ax => ax.nodes.filter(n => INTERACTIVE.has(n.role) && !n.name.trim());

    // ── the page
    const setup = async (view) => {
        if (view.phone) { await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true }); }
        else { await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false }); }
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await installAudit(ev); await setTheme(ev, view.theme);
        await ev(stubFetch(HEX, MD));
    };
    // an opener button outside the viewer, focused as a click on a link to the artifact would leave it
    const openViewer = async () => {
        await ev(`(() => { let b = document.getElementById('a11y-opener'); if (!b) { b = document.createElement('button'); b.id = 'a11y-opener'; b.textContent = 'Open plan.md'; b.style.cssText = 'position:fixed;left:4px;bottom:4px;z-index:5'; document.body.appendChild(b); } b.focus(); return true; })()`);
        await ev(`(async () => { const v = await import(${S} + 'artifact_viewer.js'); v.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'plan.md' }); return true; })()`);
        await waitFor(`!!document.querySelector('#artifact-viewer .artifact-viewer-markdown') && document.querySelectorAll('#artifact-viewer .ac-card').length >= 3`, 'the document and its comments', 8000); await settle(500);
    };
    const tabOrder = async (max = 40) => {
        const seen = [];
        for (let i = 0; i < max; i++) { await press('Tab'); const f = await focused(); const k = desc(f); if (seen.length && seen[0].k === k && i > 1) break; seen.push({ k, f }); }
        return seen;
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const view of [
            { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' }, { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'light' },
            { name: '390px', w: 390, h: 844, phone: true, theme: 'dark' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'light' },
        ]) {
            const tag = `${view.name} ${view.theme}`;
            console.log(tag);

            // ═══ 1. the viewer and its comments
            await setup(view); await openViewer();
            let f = await focused();
            check(`${tag}: the viewer takes focus on its Close button`, f.cls.includes('artifact-viewer-close'), desc(f));
            let ax = await axUnder('#artifact-viewer');
            check(`${tag}: every control in the viewer has a name (${JSON.stringify(unnamed(ax))})`, ax && unnamed(ax).length === 0);
            if (view.phone) {
                // the sheet is shut: nothing in it can be reached, by Tab or by a screen reader
                const order = await tabOrder(30);
                const inPanel = order.filter(o => /ac-(filter|whole|lock|act|cancel|send|input)|ac-sheet-close/.test(o.f.cls || ''));
                check(`${tag}: with the comments sheet shut, Tab never lands in it (${inPanel.map(o => o.k).join(' | ')})`, inPanel.length === 0);
                check(`${tag}: and never on a control that is not on screen`, order.every(o => o.f.none || o.f.onScreen), JSON.stringify(order.filter(o => !o.f.none && !o.f.onScreen).map(o => o.k)));
                const shutAx = ax.nodes.filter(n => /^(Open|Resolved|All|Reply|Resolve|Comment on the whole document)/.test(n.name));
                check(`${tag}: and the accessibility tree does not offer them (${shutAx.map(n => n.name).join(', ')})`, shutAx.length === 0);
                await ev(`document.querySelector('.ac-toggle').focus(); true`); await press('Enter'); await settle(500);
                f = await focused();
                check(`${tag}: the Comments toggle opens the sheet and says so (aria-expanded)`, await ev(`document.querySelector('.ac-toggle').getAttribute('aria-expanded')`) === 'true');
            }
            ax = await axUnder('#artifact-viewer .ac-panel');
            check(`${tag}: the comments panel is named, its filters have a pressed state`, ax.nodes.some(n => n.role === 'complementary' && /comments/i.test(n.name)) && ax.nodes.filter(n => n.role === 'button' && /^(Open|Resolved|All)$/.test(n.name) && n.pressed !== undefined).length === 3, JSON.stringify(ax.nodes.filter(n => n.role === 'button').map(n => [n.name, n.pressed])));
            // the cards: a comment reaches its text in the document from the keyboard (a control on the card), and the document reaches the cards
            const jump = await ev(`(() => { const c = document.querySelector('.ac-card[data-id="1"]'); const b = c && c.querySelector('.ac-jump'); return b ? { label: b.getAttribute('aria-label') || b.textContent.trim(), tag: b.tagName } : null; })()`);
            check(`${tag}: a comment card has a keyboard control that shows its text in the document (${JSON.stringify(jump)})`, jump && jump.tag === 'BUTTON' && /document/i.test(jump.label));
            if (jump) {
                await ev(`document.querySelector('.ac-card[data-id="3"] .ac-jump').focus(); true`); await press('Enter'); await settle(300);
                const act = await ev(`({ active: document.querySelector('.ac-card.ac-active')?.dataset.id, doc: document.activeElement === document.querySelector('.artifact-viewer-body') || document.querySelector('.artifact-viewer-body').contains(document.activeElement) })`);
                check(`${tag}: Enter on it makes that comment current and moves focus to the document (${JSON.stringify(act)})`, act.active === '3' && act.doc);
            }
            // the document, to the cards: ] and [ step through the comments from the document
            const region = await ev(`(() => { const b = document.querySelector('.artifact-viewer-body'); return { tab: b.tabIndex, role: b.getAttribute('role'), label: b.getAttribute('aria-label'), keys: b.getAttribute('aria-keyshortcuts') }; })()`);
            check(`${tag}: the document is a focusable region with a name and says its shortcuts (${JSON.stringify(region)})`, region.tab >= 0 && !!region.role && !!region.label && /\]/.test(region.keys || ''));
            await ev(`document.querySelector('.artifact-viewer-body').focus(); document.querySelector('.ac-card.ac-active')?.classList.remove('ac-active'); true`);
            await press(']'); await settle(200);
            const step1 = await ev(`document.querySelector('.ac-card.ac-active')?.dataset.id || null`);
            await press(']'); await settle(200);
            const step2 = await ev(`document.querySelector('.ac-card.ac-active')?.dataset.id || null`);
            check(`${tag}: ] from the document makes a comment current, and again the next one (${step1}, ${step2})`, !!step1 && !!step2 && step1 !== step2);

            // posting a comment from the keyboard: announced once, in a polite live region
            if (view.phone) await ev(`document.querySelector('.ac-panel').classList.contains('ac-sheet-open') || document.querySelector('.ac-toggle').click(); true`);
            await ev(`document.querySelector('.ac-whole')?.click(); true`); await settle(200);
            await ev(`window.__announce = []; (() => { const l = document.querySelector('#artifact-viewer [role=status][aria-live=polite], #artifact-viewer [aria-live=polite]'); if (l) new MutationObserver(() => window.__announce.push(l.textContent.trim())).observe(l, { childList: true, characterData: true, subtree: true }); return !!l; })()`);
            f = await focused();
            check(`${tag}: the composer's field has focus after "Comment on the whole document"`, f.tag === 'textarea', desc(f));
            await type('Looks right to me.'); await press('Enter', 2); // Ctrl+Enter submits
            await waitFor(`document.querySelectorAll('#artifact-viewer .ac-card').length >= 4`, 'the posted comment', 4000); await settle(400);
            const ann = await ev(`window.__announce.filter(Boolean)`);
            check(`${tag}: the posted comment is announced once in a live region (${JSON.stringify(ann)})`, ann.length === 1 && /comment/i.test(ann[0]));
            await shot(`${tag} comments`);
            // contrast, both themes
            const au = await auditRegion(ev, ['#artifact-viewer']);
            check(`${tag}: the viewer, its comments and the sheet have 4.5:1 text contrast (${JSON.stringify((au.fails || []).slice(0, 3))})`, (au.fails || []).length === 0);

            // closing: the sheet's close button returns focus to the toggle; Escape closes the viewer and returns it to the opener
            if (view.phone) {
                await ev(`document.querySelector('.ac-sheet-close').focus(); true`); await press('Enter'); await settle(400);
                f = await focused();
                check(`${tag}: closing the sheet returns focus to the Comments toggle (${desc(f)})`, f.cls.includes('ac-toggle'));
            }
            await press('Escape'); await settle(300);
            f = await focused();
            check(`${tag}: Escape closes the viewer and focus is back on the control that opened it (${desc(f)})`, f.id === 'a11y-opener');
        }
        // ═══ 2. the multi-select card, the Display hint and the preview's full screen
        for (const view of [
            { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' }, { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'light' },
            { name: '390px', w: 390, h: 844, phone: true, theme: 'dark' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'light' },
        ]) {
            const tag = `${view.name} ${view.theme}`;
            console.log(tag + ' (card, hint, preview)');
            await setup(view);
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('qa-multi', 'M', 5, { sleeping: false, board_project: 'team-multi' }); window.__m.add('qa-bare', 'B', 3, { sleeping: false, board_project: 'team-multi' }); true`);
            await select('qa-multi'); await firstPage(); await settle(600);

            // ── the multi-select card
            await ev(`(() => {
                const inner = window.__innerFetch || (window.__innerFetch = window.fetch); const labels = ${JSON.stringify(LABELS)};
                window.__term = { checked: [true, true, false, true, false], gone: false, bodies: [] };
                const screen = () => window.__term.gone ? null : ({ question: "What's not working about the trailer?", options: labels.map((label, i) => ({ n: i + 1, label, selected: i === 0, action: 'select', checked: window.__term.checked[i] || undefined })), multi: true, submit: { label: 'Submit', focused: false } });
                const json = (status, body) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }));
                const prev = window.fetch;
                window.fetch = (url, opts = {}) => { const u = String(url);
                    if (/\\/api\\/sessions\\/live\\/[^/?]+\\/chat\\?/.test(u)) return prev(url, opts).then(async r => { const d = await r.clone().json().catch(() => null); if (!d) return r; const sc = screen(); if (sc) d.prompt_options = sc; d.pending = window.__term.pending || null; return new Response(JSON.stringify(d), { status: r.status, headers: { 'content-type': 'application/json' } }); });
                    if (!/\\/api\\/sessions\\/live\\/[^/?]+\\/answer-prompt(?:\\?|$)/.test(u)) return prev(url, opts);
                    const b = JSON.parse(opts.body); window.__term.bodies.push(b);
                    if (b.submit) { window.__term.gone = true; return json(200, { ok: true }); }
                    window.__term.checked[b.n - 1] = !window.__term.checked[b.n - 1];
                    return json(200, { ok: true, prompt_options: screen() }); };
                return true; })()`);
            await ev(`import(${S} + 'state.js').then(st => { const row = st.state.liveSessions.find(s => s.session_id === 'qa-multi-id'); Object.assign(row, { waiting_for_input: true, waiting_summary: 'Notification: Claude is asking' });
                window.__term.pending = { tool_name: 'AskUserQuestion', input: { questions: [{ question: 'x', multiSelect: true, options: ${JSON.stringify(LABELS)}.map(label => ({ label, description: '' })) }] } }; return true; })`);
            await waitFor(`document.querySelectorAll('#live-history-messages > .chat-needs-input .cni-check').length === 5`, `${tag}: the multi-select card`, 8000);
            let axc = await axUnder('#live-history-messages > .chat-needs-input');
            const boxes = axc.nodes.filter(n => n.role === 'checkbox');
            check(`${tag}: the five options are checkboxes with a name and the state the terminal shows (${JSON.stringify(boxes.map(b => [b.name, b.checked]))})`, boxes.length === 5 && boxes.every(b => b.name.trim()) && boxes.map(b => String(b.checked)).join() === 'true,true,false,true,false');
            check(`${tag}: every control on the card has a name (${JSON.stringify(unnamed(axc))})`, unnamed(axc).length === 0);
            await ev(`document.querySelector('.cni-check[data-n="3"]').focus(); (() => { const q = document.querySelector('.chat-needs-input .cni-question'); q.__same = true;
                window.__says = []; let l = document.getElementById('coral-prompt-live'); if (!l) { window.__liveMissing = true; } else new MutationObserver(() => window.__says.push(l.textContent.trim())).observe(l, { childList: true, characterData: true, subtree: true }); return true; })()`);
            await press(' ');
            await waitFor(`document.querySelector('.cni-check[data-n="3"]').getAttribute('aria-checked') === 'true'`, `${tag}: Space ticks the box`, 4000); await settle(300);
            f = await focused();
            check(`${tag}: after Space the focus is still on that checkbox (${desc(f)})`, !f.none && f.cls.includes('cni-check') && /Tone/.test(f.label));
            const same = await ev(`!!document.querySelector('.chat-needs-input .cni-question').__same`);
            check(`${tag}: a toggle changes the box in place: the card is not drawn again (a drawn-again alert is read out whole each time)`, same);
            let says = await ev(`window.__says.filter(Boolean)`);
            check(`${tag}: the toggle is announced once, by name and state (${JSON.stringify(says)})`, says.length === 1 && /Tone\/story is off/.test(says[0]) && /checked|ticked/i.test(says[0]));
            await ev(`window.__says = []; document.querySelector('.cni-submit').focus(); true`); await press('Enter');
            await waitFor(`window.__term.bodies.some(b => b.submit)`, `${tag}: Submit was sent`, 4000);
            await ev(`import(${S} + 'state.js').then(st => { const row = st.state.liveSessions.find(s => s.session_id === 'qa-multi-id'); Object.assign(row, { waiting_for_input: false, waiting_summary: null }); window.__term.pending = null; return true; })`);
            await waitFor(`!document.querySelector('#live-history-messages > .chat-needs-input')`, `${tag}: the card goes`, 6000); await settle(300);
            says = await ev(`window.__says.filter(Boolean)`);
            check(`${tag}: the submitted answer is announced once (${JSON.stringify(says)})`, says.length === 1 && /answer|submitted|sent/i.test(says[0]));
            f = await focused();
            check(`${tag}: and focus is on the message box, not lost with the card (${desc(f)})`, f.id === 'command-input');

            // ── the Display panel's hint
            const hintView = view.phone ? '#mobile-agent-list' : '.sidebar';
            await ev(`(() => { const b = document.querySelector(${JSON.stringify(hintView + ' [data-display-button]')}); if (b && b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`); await settle(300);
            await ev(`import(${S} + 'state.js').then(st => { for (const s of st.state.liveSessions) { s.branch = ''; s.model = ''; } return true; })`);
            await ev(`document.querySelector(${JSON.stringify(hintView + ' [data-display-input="branches"]')}).click(); true`); await settle(400);
            const hint = await ev(`(() => { const h = document.querySelector(${JSON.stringify(hintView + ' [data-display-hint]')}); return h ? { text: h.textContent.trim(), role: h.getAttribute('role'), live: h.getAttribute('aria-live'), hidden: h.hidden } : null; })()`);
            check(`${tag}: the Display hint says how many agents have no branch, and is a polite live region (${JSON.stringify(hint)})`, hint && !hint.hidden && /no branch/i.test(hint.text) && (hint.role === 'status' || hint.live === 'polite'));
            const aud = await auditRegion(ev, [hintView + ' [data-display-panel]', '#live-history-messages']);
            check(`${tag}: the Display panel and the chat have 4.5:1 text contrast (${JSON.stringify((aud.fails || []).slice(0, 3))})`, (aud.fails || []).length === 0);

            // ── the preview's full screen (a phone's)
            if (view.phone) {
                await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); const { showView } = await import(st + 'utils.js');
                    { const real = window.fetch; window.fetch = (u, o) => /\\/api\\/artifacts\\/a{64}/.test(String(u)) ? Promise.resolve(new Response('# Release notes\\n\\n' + 'A line of the notes.\\n\\n'.repeat(20), { status: 200, headers: { 'Content-Type': 'text/markdown' } })) : real(u, o); }
                    state.currentSession = { type: 'live', name: 'qa-multi', session_id: 'qa-multi-id', agent_type: 'claude' }; showView('live-session-view'); window.PP.setPreviewSession('qa-multi-id');
                    window.PP.openArtifactTab('coral://artifacts/' + 'a'.repeat(64), { name: 'a.md', mediaType: 'text/markdown' }); return true; })()`);
                await waitFor(`getComputedStyle(document.getElementById('preview-pane')).display !== 'none'`, 'the preview pane', 8000); await settle(500);
                axc = await axUnder('#preview-pane');
                check(`${tag}: every control in the preview has a name (${JSON.stringify(unnamed(axc))})`, unnamed(axc).length === 0);
                const mx = await ev(`(() => { const b = document.getElementById('preview-max-btn'); return { label: b.getAttribute('aria-label'), pressed: b.getAttribute('aria-pressed') }; })()`);
                await ev(`document.getElementById('preview-max-btn').focus(); true`); await press('Enter'); await settle(500);
                const full = await ev(`(() => { const p = document.getElementById('preview-pane'); const b = document.getElementById('preview-max-btn'); return { role: p.getAttribute('role'), modal: p.getAttribute('aria-modal'), label: p.getAttribute('aria-label'), pressed: b.getAttribute('aria-pressed'), btn: b.getAttribute('aria-label'), inside: p.contains(document.activeElement) }; })()`);
                check(`${tag}: Enter on the button takes the preview full screen as a named modal dialog, the button says it is pressed and what it does now (${JSON.stringify(mx)} -> ${JSON.stringify(full)})`, full.role === 'dialog' && full.modal === 'true' && !!full.label && full.pressed === 'true' && full.btn !== mx.label && full.inside);
                let stays = true; const visited = [];
                for (let i = 0; i < 20 && stays; i++) { await press('Tab'); stays = await ev(`document.getElementById('preview-pane').contains(document.activeElement)`); visited.push(desc(await focused())); }
                check(`${tag}: in full screen Tab stays inside the preview (${visited.slice(0, 6).join(' | ')} ...)`, stays);
                await press('Escape'); await settle(400);
                f = await focused();
                check(`${tag}: Escape leaves full screen and focus is back on the full-screen button (${desc(f)})`, f.id === 'preview-max-btn' && await ev(`document.getElementById('preview-max-btn').getAttribute('aria-pressed')`) === 'false');
            }
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close();
        process.exit(failed ? 1 : 0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
