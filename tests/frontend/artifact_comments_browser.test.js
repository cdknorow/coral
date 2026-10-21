// Comments in the artifact viewer (#5182, phase 1), against a stubbed comments API that answers the way the server's one policy does (the routes are #5176's; the real server is run with them
// in the stack's own suites). Desktop and a 390 px phone: the panel lists threads with their quote, author, time, lines and state; the highlights are drawn for quotes that are found and an
// orphaned one says "text changed"; a selection shows a Comment button, the composer shows the quote and posts it with its context; reply, edit your own, resolve and reopen, delete your own (after a
// confirmation), nobody sees Delete on a comment of someone else; Open | Resolved | All; clicking a highlight or a card makes the other current; a read-only viewer (a Coral Hub view tier) gets the reason and
// no controls; a locked artifact is read only with a banner and the owner's Unlock; comments show as text, never markup; with no comments routes the viewer is as it was. The phone has a "Comments · N"
// button and a bottom sheet with 44 px actions, nothing scrolls sideways. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;
const HEX = 'd'.repeat(64);
const MD = ['# Migration plan', '', '## Steps', '', '1. Add the new columns behind a flag and backfill in batches of 500 rows.', '2. Switch reads to the new columns.', '', '## Risks', '', 'The backfill takes a write lock on large databases, so it must run off-peak.', '', 'Add a flag. Then wait. Add a flag again at the end.'].join('\n');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const STUB = `(() => {
        const real = window.fetch; window.__calls = []; window.__cs = [
            { id: 1, parent_id: null, anchor: { quote: 'backfill in batches of 500 rows.', prefix: 'columns behind a flag and', suffix: '2. Switch', start_line: 5, end_line: 5 }, body: 'Batches of 500 is too small. Try 5,000.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 120000).toISOString() },
            { id: 2, parent_id: 1, anchor: null, body: 'Agreed. Last run took 9 s per 5,000.', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a' }, created_at: new Date(Date.now() - 60000).toISOString() },
            { id: 3, parent_id: null, anchor: { quote: 'takes a write lock on large databases', prefix: 'The backfill', suffix: ', so it must run' }, body: 'Define off-peak: 02:00 to 05:00 UTC?', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a' }, created_at: new Date(Date.now() - 3600000).toISOString() },
            { id: 4, parent_id: null, anchor: { quote: 'Run the backfill nightly until it is done.' }, body: 'This step was reworded.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 259200000).toISOString() },
            { id: 5, parent_id: null, anchor: { quote: 'Switch reads to the new columns.' }, body: 'Fine.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 86400000).toISOString(), resolved_at: new Date().toISOString(), resolved_by: { issuer: 'local', subject: 'Operator' } },
        ]; window.__next = 6; window.__role = 'owner'; window.__locked = false; window.__missing = false;
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => {
            const url = String(u), method = (o && o.method) || 'GET';
            if (url.includes('/api/artifacts/${HEX}/comments')) {
                window.__calls.push({ method, url, body: o && o.body ? JSON.parse(o.body) : null });
                if (window.__missing) return json({ error: 'not found' }, 404);
                const me = window.__role, mine = c => (me === 'owner' && c.author.kind === 'owner'), sub = url.split('/comments')[1].split('?')[0];
                const can = c => ({ reply: me === 'owner' && !window.__locked && c.parent_id == null, edit: mine(c) && !window.__locked, delete: mine(c), resolve: me === 'owner' && !window.__locked && c.parent_id == null });
                if (method === 'GET') return json({ comments: window.__cs.filter(c => !c.deleted_at || window.__cs.some(r => r.parent_id === c.id && !r.deleted_at)).map(c => ({ ...c, deleted: !!c.deleted_at, body: c.deleted_at ? '' : c.body, mine: mine(c), can: can(c) })), locked: window.__locked, you: { kind: me === 'owner' ? 'owner' : 'hub', name: me === 'owner' ? 'Operator' : 'hub-user-3f2a', can_comment: me === 'owner' && !window.__locked, reason: me === 'owner' ? '' : 'Your Coral Hub access is view only.', can_lock: me === 'owner' }, limits: { body: 8192, quote: 2048, comments: 500 } });
                if (me !== 'owner' && sub !== '') return json({ error: 'Your Coral Hub access is view only.' }, 403);
                if (method === 'POST' && sub === '') { const b = JSON.parse(o.body); const c = { id: window.__next++, parent_id: b.parent_id == null ? null : b.parent_id, anchor: b.anchor, body: b.body, author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date().toISOString() }; window.__cs.push(c); return json({ ...c, can: can(c) }, 201); }
                const m = /^\\/(\\d+)(\\/(resolve|reopen))?$/.exec(sub); const c = m && window.__cs.find(x => x.id === Number(m[1]));
                if (m && c) {
                    if (method === 'PATCH') { c.body = JSON.parse(o.body).body; c.edited_at = new Date().toISOString(); return json({ ...c, can: can(c) }); }
                    if (method === 'DELETE') { if (!mine(c)) return json({ error: 'Only the author can delete a comment.' }, 403); c.deleted_at = new Date().toISOString(); return json({ ok: true }); }
                    if (m[3] === 'resolve') { c.resolved_at = new Date().toISOString(); return json({ ...c, can: can(c) }); }
                    if (m[3] === 'reopen') { delete c.resolved_at; return json({ ...c, can: can(c) }); }
                }
                if (sub === '/lock') { window.__locked = method === 'PUT'; return json({ ok: true }); }
                return json({ error: 'nope' }, 404);
            }
            if (url.includes('/api/artifacts/${HEX}')) return Promise.resolve(new Response(${JSON.stringify(MD)}, { status: 200, headers: { 'Content-Type': 'text/markdown' } }));
            return real(u, o);
        };
        return true; })()`;
    const rect = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return null; const r = e.getBoundingClientRect(); return { l: Math.round(r.left), t: Math.round(r.top), r: Math.round(r.right), b: Math.round(r.bottom), w: Math.round(r.width), h: Math.round(r.height) }; })()`);
    const open = async (extra) => {
        await ev(STUB);
        if (extra) await ev(extra);
        await ev(`(async () => { const v = await import(${S} + 'artifact_viewer.js'); v.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'plan.md' }); return true; })()`);
        await waitFor(`!!document.querySelector('#artifact-viewer .artifact-viewer-markdown')`, 'the document', 8000);
    };
    const load = async (w, h, mobile, theme) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await installAudit(ev); await setTheme(ev, theme);
    };
    const select = (text) => ev(`(() => { const body = document.querySelector('#artifact-viewer .artifact-viewer-body'); const w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT); let n; while ((n = w.nextNode())) { const i = n.nodeValue.indexOf(${JSON.stringify(text)}); if (i >= 0) { const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + ${JSON.stringify(text)}.length); const s = getSelection(); s.removeAllRanges(); s.addRange(r); return true; } } return false; })()`);
    const click = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`);
    const cards = () => ev(`[...document.querySelectorAll('.ac-card')].map(c => ({ id: c.dataset.id, text: c.textContent, quote: c.querySelector('.ac-quote').textContent, active: c.classList.contains('ac-active'), acts: [...c.querySelectorAll('.ac-act, .ac-replybox')].map(b => b.textContent) }))`);
    // an action of a thread's first entry (Resolve is in its header, Edit and Delete under its text)
    const topAct = (id, label) => `[...document.querySelectorAll('.ac-card[data-id="${id}"] > .ac-entry:first-of-type .ac-act')].find(b => b.textContent === ${JSON.stringify(label)})`;
    const lastCall = () => ev(`window.__calls[window.__calls.length - 1]`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        for (const theme of ['dark', 'light']) {
            await load(1280, 800, false, theme);
            await open();
            await waitFor(`document.querySelectorAll('.ac-card').length >= 3`, 'the threads', 5000);
            const at = `desktop, ${theme}`;
            const panel = await rect('.ac-panel');
            check(`${at}: the panel sits at the right of the document (${panel.w} px wide)`, panel && panel.w >= 300 && panel.r <= 1280);
            let cs = await cards();
            check(`${at}: Open lists the 3 open threads (the resolved one waits) with the count, quote, author and the reply under its thread`, cs.length === 3 && cs.map(c => c.id).join() === '1,3,4' && /Operator/.test(cs[0].text) && /Agreed\./.test(cs[0].text) && /hub-user-3f2a/.test(cs[0].text) && /Coral Hub/.test(cs[1].text) && /line 5/.test(cs[0].text), JSON.stringify(cs.map(c => c.id)));
            check(`${at}: a quote that is not in the text any more says "text changed" and keeps its words`, /text changed/.test(cs[2].text) && /Run the backfill nightly/.test(cs[2].quote));
            const hl = await ev(`(() => { const h = CSS.highlights.get('ac-comment'); return h ? h.size : 0; })()`);
            check(`${at}: the quotes that are found are highlighted in the document (${hl})`, hl === 2);
            check(`${at}: the actions are the viewer's: your own thread has Resolve, Edit, Delete and the reply box; the Coral Hub one has Resolve and the reply box only`, ['Resolve', 'Edit', 'Delete', 'Reply…'].every(a => cs[0].acts.includes(a)) && cs[1].acts.join() === 'Resolve,Reply…', JSON.stringify(cs.map(c => c.acts)));
            check(`${at}: no Delete on someone else's comment (the reply of hub-user-3f2a, the card of hub-user-3f2a)`, !cs[1].acts.includes('Delete') && !(await ev(`!!document.querySelector('.ac-reply .ac-act[class*=x]')`)) && !/Delete/.test(await ev(`document.querySelector('.ac-reply').textContent`)));
            // current: card -> highlight, highlight -> card
            await click('.ac-card[data-id="3"]');
            check(`${at}: a card made current marks its highlight (ac-active) and itself`, (await cards()).find(c => c.id === '3').active && (await ev(`!!CSS.highlights.get('ac-active')`)));
            // filter
            check(`${at}: the resolved thread is behind a "Show resolved (1)" toggle, pressed = false`, (await ev(`(() => { const b = document.querySelector('.ac-showresolved'); return b && b.textContent + '|' + b.getAttribute('aria-pressed'); })()`)) === 'Show resolved (1)|false');
            await click('.ac-showresolved'); cs = await cards();
            check(`${at}: Show resolved adds the resolved thread (its state and Reopen), after the open ones`, cs.length === 4 && cs[3].id === '5' && /resolved/.test(cs[3].text) && cs[3].acts.includes('Reopen') && (await ev(`document.querySelector('.ac-showresolved').textContent`)) === 'Hide resolved', JSON.stringify(cs.map(c => c.id)));
            await shot(`comments-desktop-resolved-${theme}`);
            await click('.ac-showresolved'); check(`${at}: and Hide resolved puts it away again`, (await cards()).length === 3);

            await shot(`comments-desktop-${theme}`);
            // the card, as Docs and Paper draw it
            const look = await ev(`(() => { const c = document.querySelector('.ac-card[data-id="1"]'); const es = [...c.querySelectorAll(':scope > .ac-entry')]; const top = es[0];
                return { entries: es.length, avatars: es.map(e => (e.querySelector('.ac-avatar') || {}).textContent), avatarHidden: es.every(e => e.querySelector('.ac-avatar').getAttribute('aria-hidden') === 'true'),
                    metaOrder: [...top.querySelectorAll('.ac-meta > span')].slice(0, 4).map(x => x.className.replace('ac-', '')).join(), time: top.querySelector('.ac-time').textContent, line: top.matches('.ac-entry:has(+ .ac-entry)') && getComputedStyle(top, '::after').width,
                    resolveIn: !!top.querySelector('.ac-headrow > .ac-resolve'), replyLast: c.lastElementChild.classList.contains('ac-replybox'), group: c.getAttribute('role'), label: c.getAttribute('aria-label'),
                    quoteFirst: c.firstElementChild.classList.contains('ac-quote') }; })()`);
            check(`${at}: a thread is one card: the quote, an entry per comment with an initials avatar, the name, a dot and the time in words, Resolve at the top right, the reply box last; the card is a labelled group`,
                look.entries === 2 && look.avatars.join() === 'O,HU' && look.avatarHidden && /^author,kind,dot,time$/.test(look.metaOrder) && /minutes? ago$/.test(look.time) && look.resolveIn && look.replyLast && look.quoteFirst && look.group === 'group' && /Comment by Operator/.test(look.label), JSON.stringify(look));
            check(`${at}: a thin line joins the avatars down a thread (2 px, on every entry but the last)`, look.line === '2px', JSON.stringify(look.line));
            // the card and its highlight light each other while the pointer is on either
            await ev(`document.querySelector('.ac-card[data-id="1"]').dispatchEvent(new PointerEvent('pointerenter')); true`);
            check(`${at}: the pointer on a card lights it and its text (ac-hover)`, await ev(`document.querySelector('.ac-card[data-id="1"]').classList.contains('ac-hover') && !!CSS.highlights.get('ac-hover')`));
            await ev(`document.querySelector('.ac-card[data-id="1"]').dispatchEvent(new PointerEvent('pointerleave')); true`);
            check(`${at}: and leaving puts both out`, await ev(`!document.querySelector('.ac-card.ac-hover') && !CSS.highlights.get('ac-hover')`));
            // Edit and Delete show on hover, on a current card and on focus (they are real buttons: the keyboard reaches them and focus shows them)
            await ev(`(() => { const a = document.querySelector('.ac-card[data-id="4"]'); a.classList.remove('ac-active'); return true; })()`);
            const vis = await ev(`(() => { const a = document.querySelector('.ac-card[data-id="4"] .ac-actions'); return getComputedStyle(a).opacity; })()`);
            check(`${at}: Edit and Delete of a card that is not current, hovered or focused are out of sight (opacity ${vis}) but in the page`, vis === '0' && (await ev(`!!document.querySelector('.ac-card[data-id="4"] .ac-actions .ac-act')`)));
            await ev(`document.querySelector('.ac-card[data-id="4"] .ac-actions .ac-act').focus(); true`);
            check(`${at}: and focus on one shows them`, (await ev(`getComputedStyle(document.querySelector('.ac-card[data-id="4"] .ac-actions')).opacity`)) === '1');
            // a selection -> Comment button -> composer -> post
            check(`${at}: the text is selected`, await select('write lock on large databases'));
            await waitFor(`!document.querySelector('.ac-selbtn').hidden`, 'the Comment button', 3000);
            await click('.ac-selbtn');
            await waitFor(`!!document.querySelector('.ac-composer .ac-input')`, 'the composer', 3000);
            check(`${at}: the composer shows the quote it will be made on`, /write lock on large databases/.test(await ev(`document.querySelector('.ac-composer .ac-quote').textContent`)));
            await ev(`(() => { const t = document.querySelector('.ac-composer .ac-input'); t.value = 'Is the lock per table or per database?'; t.dispatchEvent(new Event('input')); document.querySelector('.ac-composer').requestSubmit(); return true; })()`);
            await waitFor(`[...document.querySelectorAll('.ac-card')].some(c => /per table or per database/.test(c.textContent))`, 'the new card', 5000);
            let call = await ev(`window.__calls.filter(c => c.method === 'POST')[0]`);
            check(`${at}: the post carries the body and the anchor with its context`, call.body.body === 'Is the lock per table or per database?' && call.body.anchor.quote === 'write lock on large databases' && /backfill takes a/.test(call.body.anchor.prefix) && /, so it must run/.test(call.body.anchor.suffix), JSON.stringify(call.body.anchor));
            // reply
            await click('.ac-card[data-id="1"] .ac-replybox');
            await shot(`comments-desktop-reply-${theme}`);
            await ev(`(() => { const t = document.querySelector('.ac-composer .ac-input'); t.value = 'Thanks'; t.dispatchEvent(new Event('input')); document.querySelector('.ac-composer').requestSubmit(); return true; })()`);
            await waitFor(`[...document.querySelectorAll('.ac-reply')].some(c => /Thanks/.test(c.textContent))`, 'the reply', 5000);
            const rc = (await ev(`window.__calls.filter(c => c.method === 'POST').pop()`)).body;
            check(`${at}: a reply is posted with its thread's id and no anchor of its own`, rc.parent_id === 1 && !('anchor' in rc), JSON.stringify(rc));
            // edit own
            await ev(`${topAct(1, 'Edit')}.click(); true`);
            await ev(`(() => { const t = document.querySelector('.ac-composer .ac-input'); t.value = 'Batches of 500 is far too small.'; t.dispatchEvent(new Event('input')); document.querySelector('.ac-composer').requestSubmit(); return true; })()`);
            await waitFor(`[...document.querySelectorAll('.ac-card')].some(c => /far too small/.test(c.textContent) && /edited/.test(c.textContent))`, 'the edit', 5000);
            // resolve / reopen
            await ev(`${topAct(3, 'Resolve')}.click(); true`);
            await waitFor(`!document.querySelector('.ac-card[data-id="3"]')`, 'the thread leaving the open ones', 5000);
            await click('.ac-showresolved');
            await ev(`${topAct(3, 'Reopen')}.click(); true`);
            await waitFor(`!document.querySelector('.ac-card[data-id="3"].ac-resolved')`, 'the thread reopened', 5000);
            await click('.ac-showresolved');
            check(`${at}: Resolve puts a thread behind Show resolved and Reopen brings it back`, !!(await ev(`!!document.querySelector('.ac-card[data-id="3"]')`)));
            // delete own, after the confirmation
            await ev(`${topAct(6, 'Delete')}.click(); true`);
            await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 3000);
            check(`${at}: Delete asks first`, !(await ev(`window.__calls.some(c => c.method === 'DELETE')`)));
            await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            await waitFor(`!document.querySelector('.ac-card[data-id="6"]')`, 'the deleted card gone', 5000);
            check(`${at}: the delete went to the server for that comment`, /\/6$/.test((await ev(`window.__calls.filter(c => c.method === 'DELETE').pop()`)).url));
            // a thread with replies that is deleted stays as a tombstone, its replies kept
            await ev(`${topAct(1, 'Delete')}.click(); true`);
            await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 3000);
            await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            await waitFor(`/Comment deleted/.test((document.querySelector('.ac-card[data-id="1"]') || {}).textContent || '')`, 'the tombstone', 5000);
            const tomb = await ev(`(() => { const c = document.querySelector('.ac-card[data-id="1"]'); return { replies: c.querySelectorAll('.ac-reply').length, acts: [...c.querySelectorAll(':scope > .ac-entry:first-of-type .ac-act, :scope > .ac-replybox')].length }; })()`);
            check(`${at}: a deleted thread that has replies reads "Comment deleted", keeps its replies and offers no actions`, tomb.replies >= 1 && tomb.acts === 0, JSON.stringify(tomb));
            const au = await auditRegion(ev, ['.ac-panel']);
            check(`${at}: the panel reads at 4.5:1`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
        }

        // the team the viewer is opened from goes with the requests; with no team context, none
        await load(1280, 800, false, 'dark');
        await open();
        await waitFor(`document.querySelectorAll('.ac-card').length >= 3`, 'cards', 5000);
        check('opened with no team context the requests carry no board', !(await ev(`window.__calls.some(c => /board=/.test(c.url) || (c.body && c.body.board))`)));
        await load(1280, 800, false, 'dark');
        await ev(`import(${S} + 'state.js').then(m => { m.state.selectedTeam = '@box/team-x'; return true; })`);
        await open();
        await waitFor(`document.querySelectorAll('.ac-card').length >= 3`, 'cards', 5000);
        await click('.ac-whole');
        await ev(`(() => { const t = document.querySelector('.ac-composer .ac-input'); t.value = 'On the team'; t.dispatchEvent(new Event('input')); document.querySelector('.ac-composer').requestSubmit(); return true; })()`);
        await waitFor(`window.__calls.some(c => c.method === 'POST')`, 'the post', 5000);
        check('opened from a team the list and the post carry its bare board name', (await ev(`window.__calls[0].url`)).includes('board=team-x') && (await ev(`window.__calls.find(c => c.method === 'POST').body.board`)) === 'team-x');

        // text, never markup
        await load(1280, 800, false, 'dark');
        await open(`window.__cs.push({ id: 90, parent_id: null, anchor: { whole: true }, body: '<img src=x onerror="window.__pwn=1"> & <b>bold</b>', author: { kind: 'owner', issuer: 'local', subject: '<i>me</i>' }, created_at: new Date().toISOString() }); true`);
        await waitFor(`document.querySelectorAll('.ac-card').length >= 3`, 'cards', 5000);
        const xss = await ev(`(() => { const c = document.querySelector('.ac-card[data-id="90"]'); return { imgs: c.querySelectorAll('img, b, i').length, pwn: !!window.__pwn, text: c.querySelector('.ac-body').textContent, whole: c.querySelector('.ac-quote').textContent }; })()`);
        check('a comment body and author name are text: no element made, nothing run', xss.imgs === 0 && !xss.pwn && /<img src=x/.test(xss.text) && xss.whole === 'The whole document', JSON.stringify(xss));

        // read-only (a Coral Hub view tier) and locked
        await load(1280, 800, false, 'light');
        await open(`window.__role = 'hubview'; true`);
        await waitFor(`document.querySelectorAll('.ac-card').length >= 3`, 'cards', 5000);
        const ro = await ev(`({ reason: (document.querySelector('.ac-readonly') || {}).textContent, composer: !!document.querySelector('.ac-composer'), acts: document.querySelectorAll('.ac-act').length, whole: !!document.querySelector('.ac-whole'), lock: !!document.querySelector('.ac-lock') })`);
        await select('write lock on large databases'); await new Promise(r => setTimeout(r, 400)); // sleep-ok: the selection button waits a beat after the selection changes
        check('a read-only viewer gets the reason, no action buttons, no composer, no whole-document button, no lock', /view only/.test(ro.reason) && !ro.composer && ro.acts === 0 && !ro.whole && !ro.lock, JSON.stringify(ro));
        check('and a selection shows no Comment button', await ev(`document.querySelector('.ac-selbtn').hidden`));
        await load(1280, 800, false, 'dark');
        await open(`window.__locked = true; true`);
        await waitFor(`document.querySelectorAll('.ac-card').length >= 3`, 'cards', 5000);
        const lk = await ev(`({ note: (document.querySelector('.ac-locked') || {}).textContent, acts: document.querySelectorAll('.ac-act').length, unlock: (document.querySelector('.ac-lock') || {}).textContent })`);
        check('locked: a banner, no actions but the owner\'s own Delete, and the owner can Unlock', /locked/.test(lk.note) && lk.acts === 2 && lk.unlock === 'Unlock comments', JSON.stringify(lk));
        await click('.ac-lock'); await waitFor(`!document.querySelector('.ac-locked')`, 'unlocked', 4000);
        check('Unlock sends DELETE .../lock and the panel is writable again', /\/lock$/.test((await ev(`window.__calls.filter(c => c.method === 'DELETE').pop()`)).url) && !!(await ev(`!!document.querySelector('.ac-whole')`)));

        // no comments routes: the viewer is as it was
        await load(1280, 800, false, 'dark');
        await open(`window.__missing = true; true`);
        await new Promise(r => setTimeout(r, 500)); // sleep-ok: the comments request fails and the panel stays out; there is no event for "nothing happened"
        const gone = await ev(`({ hidden: document.querySelector('.ac-panel').hidden, doc: !!document.querySelector('#artifact-viewer .artifact-viewer-markdown'), wide: document.querySelector('#artifact-viewer').classList.contains('ac-on') })`);
        check('with no comments routes the panel stays out and the document is shown', gone.hidden && gone.doc && !gone.wide, JSON.stringify(gone));

        // the phone
        for (const theme of ['dark', 'light']) {
            await load(390, 844, true, theme);
            await open();
            await waitFor(`!document.querySelector('.ac-toggle').hidden`, 'the Comments button', 5000);
            const at = `390 px, ${theme}`;
            const tg = await rect('.ac-toggle');
            check(`${at}: a "Comments · 4" button of 44 px is in the header`, tg && tg.h >= 44 && /Comments · 4/.test(await ev(`document.querySelector('.ac-toggle').textContent`)), JSON.stringify(tg));
            const p0 = await rect('.ac-panel');
            check(`${at}: the sheet starts shut, below the screen (top ${p0.t} >= 844)`, p0.t >= 844);
            await ev(`document.querySelector('.ac-toggle').click(); true`);
            await new Promise(r => setTimeout(r, 400)); // sleep-ok: the sheet slides in over 200 ms
            const p1 = await rect('.ac-panel');
            check(`${at}: the sheet opens inside the screen (${p1.t}..${p1.b} of 844), its content scrolls, nothing sideways`, p1.b <= 844 + 1 && p1.t > 100 && (await ev(`document.documentElement.scrollWidth - document.documentElement.clientWidth`)) === 0, JSON.stringify(p1));
            const tiny = await ev(`[...document.querySelectorAll('.ac-panel button')].filter(b => b.getClientRects().length).map(b => { const r = b.getBoundingClientRect(); return [b.textContent, Math.round(r.height)]; }).filter(x => x[1] < 44)`);
            check(`${at}: every button in the sheet is 44 px tall`, tiny.length === 0, JSON.stringify(tiny));
            await ev(`document.querySelector('.ac-sheet-close').click(); true`);
            await select('write lock on large databases');
            await waitFor(`!document.querySelector('.ac-selbtn').hidden`, 'the Comment button by the selection', 3000);
            await click('.ac-selbtn');
            await waitFor(`!!document.querySelector('.ac-composer .ac-input')`, 'the composer', 3000);
            await new Promise(r => setTimeout(r, 400)); // sleep-ok: the sheet slides in
            const cmp = await rect('.ac-composer');
            check(`${at}: Comment on a selection opens the sheet on the composer (inside the screen: ${cmp.t}..${cmp.b})`, cmp.b <= 845 && cmp.t >= 0);
            await shot(`comments-phone-${theme}`);
            const au = await auditRegion(ev, ['.ac-panel']);
            check(`${at}: the sheet reads at 4.5:1`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
            await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 }); await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
            await waitFor(`!!document.querySelector('.ac-composer') === false || document.getElementById('artifact-viewer').hidden`, 'Escape closing the composer or the viewer', 3000);
        }
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS artifact comments: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
