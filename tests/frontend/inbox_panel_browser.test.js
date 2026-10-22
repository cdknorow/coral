// The shared Inbox component in Coral's own Inbox tab (#5754, design #5750): "Tasks for you" (each task opens inline to its full text and a reply box; the reply goes to
// POST /api/board/{project}/tasks/{id}/reply with {content} only) and "Read the whole message" on a Saved for later item. The routes are stood in for the page's fetch, as the
// server answers them (the reply route's three outcomes, the saved-message route's reason codes). Held: the rows, their names and states; the task text is rendered by the shared
// DOM-built renderer, hostile text is text; the reply sends exactly {content}, says what happened in words (sent and notified, saved but not notified, refused with the server's reason);
// the draft lives in memory only (not in localStorage or sessionStorage) and is gone after sign-out's memory purge; a viewer who cannot reply gets the reason and no box; the saved
// message opens and closes with the focus on its button and says why when there is nothing more; 44 px targets on a touch phone; both themes read at 4.5:1; no sideways scroll.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const STUB = `(() => {
    const real = window.fetch; window.__reqs = []; window.__pwn = undefined;
    const ago = ms => new Date(Date.now() - ms).toISOString();
    const TASKS = [
        { id: 1, board_id: 'team-a', board_label: 'team-a', title: 'Pick the release date', priority: 'high', status: 'pending', created_by: 'Orchestrator', created_at: ago(7200000), updated_at: ago(7200000), revision: 1 },
        { id: 2, board_id: 'team-a', board_label: 'team-a', title: 'Check <img src=x onerror="window.__pwn=1"> the logo', priority: 'medium', status: 'in_progress', created_by: 'Designer', created_at: ago(86400000 * 3), updated_at: ago(86400000 * 3) },
        { id: 3, board_id: 'team-b', board_label: 'team-b', title: 'Approve the budget', priority: 'low', status: 'review_pending', created_by: 'Lead', created_at: ago(60000), updated_at: ago(60000) },
    ];
    const BODY = { 1: 'Which day?\\n\\n- **Monday** the 4th\\n- Friday the 8th\\n\\nSee [the plan](https://example.com/plan) and [click](javascript:alert(1)).\\n\\n<script>window.__pwn=1</script>\\n\\n![t](https://evil.example/p.png)',
        2: 'Plain text.', 3: '' };
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
    const SAVED = [11, 12, 13, 14].map(id => ({ id, kind: 'agent', ref: 'sess-' + id, key: String(id).padStart(20, '0'), title: 'Agent ' + id, snippet: 'snippet ' + id, agent_type: 'claude', saved_at: ago(id * 1000), said_at: ago(id * 2000) }));
    const MSG = { 11: { text: 'The **whole** message\\n\\n- a\\n- b\\n\\n<b>not bold</b>', partial: false, reason: '' }, 12: { text: '', partial: false, reason: 'gone' },
        13: { text: 'Part of a very long message', partial: true, reason: '' }, 14: null };
    window.fetch = (u, o = {}) => {
        const url = new URL(String(u), location.origin), m = (o.method || 'GET').toUpperCase(), p = url.pathname;
        window.__reqs.push({ m, p, q: url.search, body: o.body ? String(o.body) : null });
        if (p === '/api/board/tasks' && m === 'GET') return json({ tasks: TASKS, total: TASKS.length });
        let r = /^\\/api\\/board\\/([^/]+)\\/tasks\\/(\\d+)$/.exec(p);
        if (r && m === 'GET' && window.__failTask === Number(r[2])) return json({ error: 'the board is busy' }, 500);
        if (r && m === 'GET') { const t = TASKS.find(x => x.id === Number(r[2])); return t ? json({ ...t, body: BODY[t.id] }) : json({ error: 'task not found' }, 404); }
        r = /^\\/api\\/board\\/([^/]+)\\/tasks\\/(\\d+)\\/reply$/.exec(p);
        if (r && m === 'POST') {
            const id = Number(r[2]);
            if (id === 1) return json({ ok: true, message_id: 9, task_id: 1, notified: ['Orchestrator'] });
            if (id === 2) return json({ ok: true, message_id: 10, task_id: 2, notified: [], warnings: ['creator_not_reachable'] });
            return json({ error: 'Your Coral Hub access is view only.' }, 403);
        }
        if (p === '/api/saved' && m === 'GET') return json({ items: SAVED, open: SAVED.length, done: 0 });
        r = /^\\/api\\/saved\\/(\\d+)\\/message$/.exec(p);
        if (r && m === 'GET') { const x = MSG[Number(r[1])]; return x ? json(x) : json({ error: 'the transcript could not be read' }, 500); }
        r = /^\\/api\\/artifacts\\/([0-9a-f]{64})\\/meta$/.exec(p);
        if (r && m === 'GET') {
            const c = r[1][0];
            if (c === 'a') return json({ id: r[1] });
            if (c === 'b') return json({ error: 'not yours' }, 403);
            if (c === 'c') return json({ error: 'not found' }, 404);
            return Promise.reject(new TypeError('network'));
        }
        if (p.startsWith('/api/sessions/history/')) return json({ found: true, index: 1 });
        return real(u, o);
    };
    return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined && !cond ? ' — ' + detail : ''}`); };
    const S = `(window.__coralStatic || '/static/')`;
    // CORAL_SCREENSHOT_DIR: a picture of each view (for review).
    // Only the message BODY is in an inset panel (#5882): a lighter surface, one clear step from the card, a 1px border that holds 3:1 against the row, the panel and the page; the meta line (with the
    // small "Open the task" link) above it and the reply below it are outside it; the card content, the panel and the reply box share one left and right edge; 8 px above, 12 px below.
    const insetOf = (sel, kind) => ev(`(() => { const panel = document.querySelector(${JSON.stringify(sel)}); if (!panel) return null;
        const rgb = c => { const m = /rgba?\\(([^)]+)\\)/.exec(c); if (!m) return null; const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; };
        const lum = c => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
        const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
        const r2 = n => Math.round(n * 100) / 100;
        const behind = el => { for (let e = el; e; e = e.parentElement) { const c = rgb(getComputedStyle(e).backgroundColor); if (c && c.a > 0.99) return c; } return rgb(getComputedStyle(document.body).backgroundColor); };
        const cs = getComputedStyle(panel), bg = rgb(cs.backgroundColor), border = rgb(cs.borderTopColor), row = behind(panel.parentElement), page = behind(panel.closest('.ibx-tasks, .saved-list, #saved-list') || panel.parentElement);
        const rect = panel.getBoundingClientRect();
        const out = { bw: cs.borderTopWidth, radius: parseFloat(cs.borderTopLeftRadius), pad: [parseFloat(cs.paddingTop), parseFloat(cs.paddingLeft)], differs: !!(bg && row && (bg.r !== row.r || bg.g !== row.g || bg.b !== row.b)),
            step: bg && row ? r2(ratio(bg, row)) : 0, borderOnRow: border && row ? r2(ratio(border, row)) : 0, borderOnPanel: border && bg ? r2(ratio(border, bg)) : 0, borderOnPage: border && page ? r2(ratio(border, page)) : 0 };
        if (${JSON.stringify(kind)} === 'tasks') {
            const region = panel.parentElement, meta = region.querySelector('.ibx-task-metarow'), ta = region.querySelector('.ibx-reply-input'), open = region.querySelector('.ibx-open'), head = region.closest('.ibx-task').querySelector('.ibx-task-title');
            const rr = region.getBoundingClientRect();
            out.onlyBody = [...panel.children].every(c => c.classList.contains('md') || c.classList.contains('ibx-empty')) && !panel.querySelector('textarea, .ibx-open, .ibx-task-facts, .ibx-reply');
            out.openIsLink = !!open && !panel.contains(open) && meta.contains(open) && parseFloat(getComputedStyle(open).fontSize) <= 14.5 && getComputedStyle(open).textTransform === 'none';
            out.edges = { panelVsReply: ta ? [Math.round(rect.left - ta.getBoundingClientRect().left), Math.round(ta.getBoundingClientRect().right - rect.right)] : null, panelVsTitle: Math.round(rect.left - head.getBoundingClientRect().left) };
            out.above = meta ? Math.round(rect.top - meta.getBoundingClientRect().bottom) : null;
            const headBtn = region.closest('.ibx-task').querySelector('.ibx-task-head'); out.headGap = meta ? Math.round(meta.getBoundingClientRect().top - headBtn.getBoundingClientRect().bottom) : null;
            const next = panel.nextElementSibling; out.below = next ? Math.round(next.getBoundingClientRect().top - rect.bottom) : null;
        } else {
            const wrap = panel.closest('.ibx-saved'), btn = wrap.querySelector('.ibx-saved-toggle'); let after = wrap.nextElementSibling; while (after && after.getBoundingClientRect().height === 0) after = after.nextElementSibling; // the next thing that takes room
            out.onlyBody = !panel.querySelector('button, .ibx-why');
            out.above = Math.round(rect.top - btn.getBoundingClientRect().bottom);
            out.below = after ? Math.round(after.getBoundingClientRect().top - rect.bottom) : null;
        }
        return out; })()`);
    const checkInset = async (dev, sel, kind) => {
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            await shot(`inbox-inset-${kind}-${/phone/.test(dev) ? 'phone' : 'desktop'}-${theme}`);
            const i = await insetOf(sel, kind);
            check(`${dev}, ${theme}, ${kind}: the message body alone is an inset panel (a lighter surface, a 1px border, rounded, padded)`, i && i.onlyBody === true && i.differs === true && i.step >= 1.15 && i.bw === '1px' && i.radius >= 4 && i.pad[0] >= 8 && i.pad[1] >= 10, JSON.stringify(i));
            check(`${dev}, ${theme}, ${kind}: its border holds 3:1 against the row, the panel and the page`, i && i.borderOnRow >= 3 && i.borderOnPanel >= 3 && i.borderOnPage >= 3, JSON.stringify(i));
            check(`${dev}, ${theme}, ${kind}: 8 px above it, 12 px below it`, i && Math.abs(i.above - 8) <= 1 && i.below !== null && Math.abs(i.below - 12) <= 1, JSON.stringify(i));
            if (kind === 'saved') {
                const sv = await ev(`(() => { const panel = document.querySelector(${JSON.stringify(sel)}); const li = panel.closest('.saved-item'); const snip = li.querySelector('.snip'), btn = li.querySelector('.ibx-saved-toggle'), acts = [...li.querySelectorAll('.acts .btn')];
                    return { snip: getComputedStyle(snip).display, toggle: btn.textContent, exp: btn.getAttribute('aria-expanded'), tops: acts.map(b => Math.round(b.getBoundingClientRect().top)), n: acts.length }; })()`);
                check(`${dev}, ${theme}: while the full message is shown the snippet is hidden, and the button is a toggle ("Show less", aria-expanded true)`, sv.snip === 'none' && sv.toggle === 'Show less' && sv.exp === 'true', JSON.stringify(sv));
                check(`${dev}, ${theme}: Open in Chats, Done and Remove are on one row`, sv.n === 3 && new Set(sv.tops).size === 1, JSON.stringify(sv));
            }
            if (kind === 'tasks') {
                check(`${dev}, ${theme}: 8 px from the header row to the meta line`, i && i.headGap !== null && Math.abs(i.headGap - 8) <= 1, JSON.stringify(i.headGap));
                check(`${dev}, ${theme}: the panel, the card's content and the reply box share one left and right edge`, i && i.edges.panelVsReply && i.edges.panelVsReply.every(d => Math.abs(d) <= 1) && Math.abs(i.edges.panelVsTitle) <= 1, JSON.stringify(i.edges));
                const ot = await ev(`(() => { const o = document.querySelector(${JSON.stringify(sel)}).closest('.ibx-task-body, .ibx-task').querySelector('.ibx-open'); return o ? Math.round(o.getBoundingClientRect().height) : 0; })()`);
                check(`${dev}, ${theme}: "Open the task" is a big enough target (${ot} px: 24 with a mouse, 44 on a touch phone)`, ot >= (/phone/.test(dev) ? 44 : 24), String(ot));
                check(`${dev}, ${theme}: "Open the task" is a small link on the meta line, not a heading inside the panel`, i && i.openIsLink === true, JSON.stringify(i));
            }
        }
    };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const open = async (w, mobile, theme) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await installAudit(ev); await setTheme(ev, theme);
        await ev(STUB);
        await ev(`localStorage.removeItem('coral.inbox.tab'); document.getElementById('nav-tab-hub-inbox') && (document.getElementById('nav-tab-hub-inbox').click(), true)`);
        await waitFor(`!!document.getElementById('inbox-local') && !document.getElementById('inbox-local').hidden`, 'the Inbox', 8000);
    };
    const tab = n => ev(`document.getElementById('inbox-tab-${n}').click(); true`);
    const rows = () => ev(`[...document.querySelectorAll('#inbox-tasks-host .ibx-task')].map(li => { const h = li.querySelector('.ibx-task-head'); const r = li.querySelector('.ibx-task-body'); return { title: li.querySelector('.ibx-task-title').textContent, meta: li.querySelector('.ibx-task-meta').textContent, expanded: h.getAttribute('aria-expanded'), controls: h.getAttribute('aria-controls'), regionOk: !!r && r.id === h.getAttribute('aria-controls') && !r.hasAttribute('role') && !r.hasAttribute('aria-label'), hidden: r.hidden }; })`);
    const head = i => ev(`document.querySelectorAll('#inbox-tasks-host .ibx-task-head')[${i}].click(); true`);
    const type = (i, text) => ev(`(() => { const t = document.querySelectorAll('#inbox-tasks-host .ibx-reply-input')[${i}]; t.value = ${JSON.stringify(text)}; t.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const send = i => ev(`document.querySelectorAll('#inbox-tasks-host .ibx-send')[${i}].click(); true`);
    const posts = () => ev(`window.__reqs.filter(r => r.m === 'POST')`);
    const status = i => ev(`document.querySelectorAll('#inbox-tasks-host .ibx-reply-status')[${i}].textContent`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await client.Emulation.setFocusEmulationEnabled({ enabled: true });

        for (const [dev, w, mobile, theme] of [['desktop, dark', 1280, false, 'dark'], ['phone 390, light', 390, true, 'light']]) {
            console.log(dev);
            await open(w, mobile, theme);
            await tab('tasks');
            await waitFor(`document.querySelectorAll('#inbox-tasks-host .ibx-task').length === 3`, 'three tasks', 8000);
            const q = await ev(`window.__reqs.find(r => r.p === '/api/board/tasks')`);
            check(`${dev}: the list asks for the Operator's open tasks, with a field list (no per-row request)`, q && /assigned_to=Operator/.test(q.q) && /status=pending/.test(q.q) && /fields=/.test(q.q) && (await ev(`window.__reqs.filter(r => /\\/tasks\\/\\d+$/.test(r.p)).length`)) === 0, JSON.stringify(q));
            const rs = await rows();
            check(`${dev}: a row per task: title, board, priority and age; each a button that says it is closed and which region it controls`, rs.length === 3 && rs[0].title === 'Pick the release date' && /team-a · high · 2 hours ago/.test(rs[0].meta) && rs.every(r => r.expanded === 'false' && r.regionOk && r.hidden) && /review/.test(rs[2].meta), JSON.stringify(rs));
            check(`${dev}: a title with markup is text: the second title is its own characters, no element was made, nothing ran`, rs[1].title === 'Check <img src=x onerror="window.__pwn=1"> the logo' && (await ev(`document.querySelectorAll('#inbox-tasks-host img').length === 0 && window.__pwn === undefined`)) === true, JSON.stringify(rs[1]));

            // the shared renderer is not loaded by the page: it loads once, when the first text is shown (#5818)
            check(`${dev}: before any text is shown the renderer is not loaded`, (await ev(`typeof window.AccountMarkdown`)) === 'undefined');
            // expand: the full task through the shared renderer
            await head(0);
            await waitFor(`!!document.querySelector('#inbox-tasks-host .ibx-task-body .md')`, 'the task text', 4000);
            check(`${dev}: opening the first task loaded the renderer, once`, (await ev(`typeof window.AccountMarkdown`)) === 'object' && (await ev(`performance.getEntriesByType('resource').filter(e => /vendor\\/safe-markdown\\.js/.test(e.name)).length`)) === 1);
            const body = await ev(`(() => { const r = document.querySelector('#inbox-tasks-host .ibx-task-body'); const tags = {}; r.querySelector('.md').querySelectorAll('*').forEach(e => { tags[e.tagName.toLowerCase()] = 1; }); tags.md = 1;
                return { expanded: document.querySelector('#inbox-tasks-host .ibx-task-head').getAttribute('aria-expanded'), shown: !r.hidden, tags: Object.keys(tags), text: r.textContent, anchors: [...r.querySelectorAll('.md a')].map(a => [a.getAttribute('href'), a.getAttribute('rel'), a.getAttribute('target')]), pwn: window.__pwn, req: window.__reqs.filter(x => /\\/tasks\\/1$/.test(x.p)).length }; })()`);
            check(`${dev}: it opens: aria-expanded true, the task read once, "From <creator>" and the status`, body.expanded === 'true' && body.shown && body.req === 1 && /From Orchestrator · pending/.test(body.text), JSON.stringify(body.text).slice(0, 160));
            check(`${dev}: the text is rendered (a list, bold) from a closed list of tags; raw HTML is shown as text and nothing ran`, ['ul', 'li', 'strong', 'p'].every(t => body.tags.includes(t)) && !body.tags.some(t => ['script', 'img', 'iframe', 'svg', 'form'].includes(t)) && body.text.includes('<script>window.__pwn=1</script>') && body.pwn === undefined, JSON.stringify(body.tags));
            check(`${dev}: a good link is a real link (noopener noreferrer, new tab) with its host shown; javascript: is text; the image is not loaded`, body.anchors.length === 1 && body.anchors[0][0] === 'https://example.com/plan' && body.anchors[0][1] === 'noopener noreferrer' && body.anchors[0][2] === '_blank' && body.text.includes('(example.com)') && body.text.includes('[click](javascript:alert(1))') && body.text.includes('(image not shown)'), JSON.stringify(body.anchors));

            // reply: {content} only, in words
            const label = await ev(`(() => { const t = document.querySelector('#inbox-tasks-host .ibx-reply-input'); const l = document.querySelector('#inbox-tasks-host .ibx-reply-label'); return { label: l.textContent, forOk: l.getAttribute('for') === t.id, sendDisabled: document.querySelector('#inbox-tasks-host .ibx-send').disabled, note: document.querySelector('#inbox-tasks-host .ibx-note').textContent }; })()`);
            check(`${dev}: the reply box is labelled by the task, Send waits for text, and the note says a reply changes nothing`, label.label === 'Your reply to Pick the release date' && label.forOk && label.sendDisabled && label.note === 'A reply does not complete or change the task.', JSON.stringify(label));
            await type(0, 'Friday the 8th, please.');
            await waitFor(`!document.querySelector('#inbox-tasks-host .ibx-send').disabled`, 'Send on', 2000);
            const store = await ev(`JSON.stringify(Object.entries(localStorage)) + JSON.stringify(Object.entries(sessionStorage))`);
            check(`${dev}: the draft is not in localStorage or sessionStorage`, !store.includes('Friday the 8th'), 'found in storage');
            await head(0); await head(0); // close, open again: the draft is still there (memory)
            check(`${dev}: closing and opening the task keeps the draft`, (await ev(`document.querySelector('#inbox-tasks-host .ibx-reply-input').value`)) === 'Friday the 8th, please.');
            await send(0);
            await waitFor(`/Reply sent/.test(document.querySelector('#inbox-tasks-host .ibx-reply-status').textContent)`, 'the answer', 4000);
            const sent = (await posts()).filter(r => /\/reply$/.test(r.p));
            check(`${dev}: the reply is posted to the task's reply route with {content} and nothing else`, sent.length === 1 && sent[0].p === '/api/board/team-a/tasks/1/reply' && JSON.stringify(JSON.parse(sent[0].body)) === JSON.stringify({ content: 'Friday the 8th, please.' }), JSON.stringify(sent));
            check(`${dev}: it says what happened ("Reply sent. Orchestrator notified."), clears the box and the draft`, (await status(0)) === 'Reply sent. Orchestrator notified.' && (await ev(`document.querySelector('#inbox-tasks-host .ibx-reply-input').value`)) === '', await status(0));
            check(`${dev}: the live region says it too`, /Reply sent/.test(await ev(`document.querySelector('#inbox-tasks-host .ibx-live').textContent`)));
            // #5770 (from the #5768 findings): one spoken line, the focus on the box, a turning chevron, tasks that stay open
            check(`${dev}: a sent reply is spoken once: only the panel's live region is a status, the line in the form is not`, (await ev(`document.querySelectorAll('#inbox-tasks-host [role=status]').length`)) === 1 && !(await ev(`document.querySelector('#inbox-tasks-host .ibx-reply-status').hasAttribute('role')`)));
            check(`${dev}: after Send the focus is on the (empty) reply box, not on the page`, await ev(`document.activeElement === document.querySelector('#inbox-tasks-host .ibx-reply-input')`));
            const chev = await ev(`(() => { const h = document.querySelectorAll('#inbox-tasks-host .ibx-task-head'); const t = e => getComputedStyle(e, '::after').transform; return { open: t(h[0]), closed: t(h[1]), content: getComputedStyle(h[1], '::after').content }; })()`);
            check(`${dev}: a chevron on each row turns when the task is open`, chev.content !== 'none' && chev.open !== chev.closed, JSON.stringify(chev));
            await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-task-head')[0].focus(); true`);
            await tab('needs'); await tab('tasks');
            await waitFor(`document.querySelectorAll('#inbox-tasks-host .ibx-task').length === 3 && !!document.querySelector('#inbox-tasks-host .ibx-task-body:not([hidden]) .ibx-reply-input')`, 'the open task is open again', 6000);
            check(`${dev}: leaving the tab and coming back keeps the open task open`, (await ev(`[...document.querySelectorAll('#inbox-tasks-host .ibx-task-head')].map(h => h.getAttribute('aria-expanded')).join()`)) === 'true,false,false');
            check(`${dev}: the page heading is the tab it is on`, (await ev(`document.getElementById('inbox-title').textContent`)) === 'Tasks for you');
            const lv = () => ev(`document.querySelector('#inbox-tasks-host .ibx-live').textContent.trim()`);
            await head(1);
            await waitFor(`!!document.querySelectorAll('#inbox-tasks-host .ibx-task-body')[1].querySelector('.ibx-reply-input')`, 'the second task', 4000);
            check(`${dev}: opening a task says it is open (the live region), and it is no longer busy`, /is open\./.test(await lv()) && (await ev(`!document.querySelectorAll('#inbox-tasks-host .ibx-task-body')[1].hasAttribute('aria-busy')`)));
            await ev(`window.__failTask = 3; true`); await head(2);
            await waitFor(`/Could not read the task/.test(document.querySelector('#inbox-tasks-host .ibx-live').textContent)`, 'the error is spoken', 4000);
            check(`${dev}: a task that cannot be read says so in the live region too: "${(await lv()).slice(0, 60)}"`, /board is busy/.test(await lv()));
            await ev(`window.__failTask = undefined; true`);
            await head(2); // closed again, as the steps below find it (opening it later reads it fresh)

            await shot(`inbox-tasks-${mobile ? 'phone' : 'desktop'}`);
            await checkInset(dev, '#inbox-tasks-host .ibx-task-body:not([hidden]) .ibx-panel', 'tasks');
            await setTheme(ev, theme);
            // creator not reachable, and a refusal
            await head(1); await waitFor(`!!document.querySelectorAll('#inbox-tasks-host .ibx-reply-input')[1]`, 'task 2 open', 4000);
            await type(1, 'Looks right.'); await send(1);
            await waitFor(`/saved on the board/.test(document.querySelectorAll('#inbox-tasks-host .ibx-reply-status')[1].textContent)`, 'the warning', 4000);
            check(`${dev}: when the creator cannot be told, it says the reply was saved but nobody was notified`, (await status(1)) === 'Reply saved on the board, but Designer could not be notified.');
            await head(2); await waitFor(`!!document.querySelectorAll('#inbox-tasks-host .ibx-reply-input')[2]`, 'task 3 open', 4000);
            check(`${dev}: a task with no text says so`, /This task has no text/.test(await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-task-body')[2].textContent`)));
            await type(2, 'Approved.'); await send(2);
            await waitFor(`/view only/.test(document.querySelectorAll('#inbox-tasks-host .ibx-reply-status')[2].textContent)`, 'the refusal', 4000);
            check(`${dev}: a refusal shows the server's own reason and keeps the draft`, (await status(2)) === 'Your Coral Hub access is view only.' && (await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-reply-input')[2].value`)) === 'Approved.');
            // sign-out's memory purge drops the drafts
            await ev(`import(${S} + 'local_store.js').then(m => m.forgetAccountMemory()); true`);
            await head(2); await head(2);
            check(`${dev}: after sign-out's memory purge the unsent draft is gone`, (await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-reply-input')[2].value`)) === '');

            // touch targets and sideways scroll
            const sizes = await ev(`(() => { const h = [...document.querySelectorAll('#inbox-tasks-host .ibx-task-head, #inbox-tasks-host .ibx-send')].map(e => Math.round(e.getBoundingClientRect().height)).filter(x => x > 0); return { h, over: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
            if (mobile) check(`${dev}: every row and Send is 44 px tall on a touch phone`, sizes.h.every(x => x >= 44), JSON.stringify(sizes));
            check(`${dev}: nothing scrolls sideways`, sizes.over <= 0, JSON.stringify(sizes));
            const au = await auditRegion(ev, ['#inbox-tasks-host']);
            check(`${dev}: the panel reads at 4.5:1`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 3)));

            // Saved for later: Read the whole message
            await tab('saved');
            await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 4`, 'four saved items', 8000);
            await waitFor(`document.querySelectorAll('#saved-list .ibx-saved-toggle').length === 4`, 'a control on each', 6000);
            const sv = await ev(`(() => { const b = document.querySelector('#saved-list .ibx-saved-toggle'); const r = document.getElementById(b.getAttribute('aria-controls')); return { text: b.textContent, exp: b.getAttribute('aria-expanded'), regionHidden: r.hidden, label: r.getAttribute('aria-label'), tag: b.tagName }; })()`);
            check(`${dev}: each saved item has a "Read the whole message" button (closed, controlling its text, which is not a landmark)`, sv.text === 'Read the whole message' && sv.exp === 'false' && sv.regionHidden && sv.label === null && sv.tag === 'BUTTON', JSON.stringify(sv));
            const sb = i => `document.querySelectorAll('#saved-list .ibx-saved-toggle')[${i}]`;
            await ev(`${sb(0)}.focus(); ${sb(0)}.click(); true`);
            await waitFor(`!!document.querySelector('#saved-list .ibx-saved-body .md')`, 'the whole message', 4000);
            const first = await ev(`(() => { const b = ${sb(0)}; const r = document.getElementById(b.getAttribute('aria-controls')); return { text: b.textContent, exp: b.getAttribute('aria-expanded'), focus: document.activeElement === b, body: r.textContent, strong: r.querySelectorAll('strong').length, li: r.querySelectorAll('li').length, bold: r.querySelectorAll('b').length, req: window.__reqs.filter(x => /\\/api\\/saved\\/11\\/message$/.test(x.p)).length }; })()`);
            check(`${dev}: it opens in place: the button becomes "Show less" (a toggle), aria-expanded true, the focus stays on the button, the text rendered (bold, a list), a tag in it shown as text`, first.text === 'Show less' && first.exp === 'true' && first.focus && first.strong === 1 && first.li === 2 && first.bold === 0 && first.body.includes('<b>not bold</b>') && first.req === 1, JSON.stringify(first));
            await ev(`${sb(0)}.click(); true`);
            check(`${dev}: a second press hides it again`, (await ev(`${sb(0)}.getAttribute('aria-expanded')`)) === 'false' && (await ev(`${sb(0)}.textContent`)) === 'Read the whole message', 'back to Read the whole message');
            await ev(`${sb(0)}.click(); true`);
            check(`${dev}: and opening it again does not read it a second time`, (await ev(`window.__reqs.filter(x => /\\/api\\/saved\\/11\\/message$/.test(x.p)).length`)) === 1);
            await ev(`${sb(1)}.click(); true`);
            await waitFor(`/deleted/.test(document.getElementById(${sb(1)}.getAttribute('aria-controls')).textContent)`, 'the gone reason', 4000);
            check(`${dev}: a message that is gone says so in words`, /^This message was deleted, so only the saved snippet is left\.$/.test(await ev(`document.getElementById(${sb(1)}.getAttribute('aria-controls')).textContent`)));
            await ev(`${sb(2)}.click(); true`);
            await waitFor(`/Part of a very long message/.test(document.getElementById(${sb(2)}.getAttribute('aria-controls')).textContent)`, 'the partial text', 4000);
            check(`${dev}: a long message says it is shown in part`, /Shown in part/.test(await ev(`document.getElementById(${sb(2)}.getAttribute('aria-controls')).textContent`)));
            await ev(`${sb(3)}.click(); true`);
            await waitFor(`/Could not read the message/.test(document.getElementById(${sb(3)}.getAttribute('aria-controls')).textContent)`, 'the error', 4000);
            check(`${dev}: a refusal says it could not read the message, with the server's reason`, /Could not read the message \(the transcript could not be read\)/.test(await ev(`document.getElementById(${sb(3)}.getAttribute('aria-controls')).textContent`)));
            await shot(`inbox-saved-${mobile ? 'phone' : 'desktop'}`);
            await checkInset(dev, '#saved-list .ibx-saved-body:not([hidden]) .ibx-panel', 'saved');
            await setTheme(ev, theme);
            const au2 = await auditRegion(ev, ['#saved-list']);
            check(`${dev}: the saved list with the messages open reads at 4.5:1`, au2.fails.length === 0, JSON.stringify(au2.fails.slice(0, 3)));
        }

        // a host that cannot reply (a Coral Hub viewer): the reason, no box. And the component's own rules.
        console.log('another host');
        await open(1280, false, 'dark');
        await ev(`(async () => { const m = await import(${S} + 'inbox_panel.js'); const host = document.createElement('div'); host.id = 'other-host'; document.body.append(host);
            const adapter = { listTasks: async () => [{ id: 5, project: 'p', board: 'p', title: 'T', priority: 'low', status: 'pending', created_by: 'X', updated_at: new Date().toISOString() }], getTask: async () => ({ body: 'text', created_by: 'X' }),
                replyToTask: async () => { throw new Error('should not be called'); }, canReply: () => ({ ok: false, reason: 'Your Coral Hub access is view only.' }),
                taskHref: () => '//evil.example/x' };
            window.__other = { m, inbox: m.mountInbox(host, adapter) }; return true; })()`);
        await waitFor(`!!document.querySelector('#other-host .ibx-task-head')`, 'the other host\'s task', 4000);
        await ev(`document.querySelector('#other-host .ibx-task-head').click(); true`);
        await waitFor(`!!document.querySelector('#other-host .ibx-task-body .ibx-note, #other-host .ibx-task-body textarea')`, 'the other host\'s task text', 4000);
        const other = await ev(`(() => { const { m, inbox } = window.__other; const host = document.getElementById('other-host');
            let threw = ''; try { m.mountInbox(document.createElement('div'), { listTasks() {} }); } catch (e) { threw = e.constructor.name; }
            let threw2 = ''; try { m.mountInbox(document.createElement('div'), 'not an adapter'); } catch (e) { threw2 = e.constructor.name; }
            let threw3 = ''; try { m.mountInbox(document.createElement('div'), { contract: 2, listTasks() {}, getTask() {}, replyToTask() {}, canReply() {} }); } catch (e) { threw3 = e.constructor.name; }
            const out = { contract: m.INBOX_CONTRACT, textarea: host.querySelectorAll('textarea').length, note: (host.querySelector('.ibx-reply .ibx-note') || host.querySelector('.ibx-note') || {}).textContent, link: host.querySelectorAll('a.ibx-open').length, threw, threw2, threw3,
                paths: [m.safePath('/ok'), m.safePath('//evil.example'), m.safePath('/a\\\\b'), m.safePath('/a b'), m.safePath('https://x.example/'), m.safePath('')] };
            inbox.destroy(); out.removed = !host.querySelector('.ibx'); return out; })()`);
        check('a viewer who cannot reply gets the reason and no box', other.textarea === 0 && other.note === 'Your Coral Hub access is view only.', JSON.stringify(other));
        check('an adapter\'s link that is not a same-origin path makes no link, a half adapter, a non-object and an adapter written for another contract are refused (TypeError), the contract is 1', other.link === 0 && other.threw === 'TypeError' && other.threw2 === 'TypeError' && other.threw3 === 'TypeError' && other.contract === 1, JSON.stringify(other));
        check('safePath accepts a same-origin absolute path only', JSON.stringify(other.paths) === JSON.stringify(['/ok', '', '', '', '', '']), JSON.stringify(other.paths));
        check('destroy removes the panel', other.removed === true);

        // artifact links (#5784): a coral://artifacts/<hex> in task text is a link only after GET /api/artifacts/<hex>/meta answered 200
        console.log('artifact links');
        const A = c => c.repeat(64);
        const L = (label, id) => `[${label}](coral://artifacts/${id})`;
        const text = [`ok ${L('ok', A('a'))} and ${L('again', A('a'))}`, `forbidden ${L('forbidden', A('b'))}`, `missing ${L('missing', A('c'))}`, `offline ${L('offline', A('d'))}`,
            `upper ${L('upper', A('A'))}`, `short ${L('short', 'a'.repeat(63))}`, `[trick](coral://artifacts/${A('a')}/../../x)`, `[pw](coral://artifacts/${A('e')}?x=1)`, `bare coral://artifacts/${A('f')}`].join('\n\n');
        await ev(`(async () => { window.__reqs.length = 0; const m = await import(${S} + 'inbox_panel.js'); const c = await import(${S} + 'inbox_coral.js'); const host = document.createElement('div'); host.id = 'link-host'; document.body.append(host);
            const adapter = { ...c.coralInboxAdapter, listTasks: async () => [{ id: 7, project: 'team a', board: 'x', title: 'Links', priority: 'low', status: 'pending', created_by: 'X', updated_at: window.__linkStamp, revision: window.__linkRev }], getTask: async () => { window.__reads = (window.__reads || 0) + 1; return { body: ${JSON.stringify(text)}, created_by: 'X' }; } };
            window.__linkStamp = '2026-10-21T10:00:00Z'; window.__linkRev = 1; window.__reads = 0;
            window.__link = m.mountInbox(host, adapter); return true; })()`);
        await waitFor(`!!document.querySelector('#link-host .ibx-task-head')`, 'the link host task', 4000);
        await ev(`document.querySelector('#link-host .ibx-task-head').click(); true`);
        await waitFor(`!!document.querySelector('#link-host .ibx-task-body .md')`, 'the link host text', 4000);
        const lk = await ev(`(() => { const b = document.querySelector('#link-host .ibx-task-body'); const metas = window.__reqs.filter(r => /\\/meta$/.test(r.p)).map(r => r.p.split('/')[3][0]);
            return { hrefs: [...b.querySelectorAll('.md a')].map(a => a.getAttribute('href')), metas, open: b.querySelector('a.ibx-open').getAttribute('href'), text: b.textContent }; })()`);
        check('only the id that answered 200 is a link, to the fixed /api/artifacts/<hex> path (both markdown links of it; b, c, d, e and the others are written as links too, so only the 200 can make one)', lk.hrefs.length === 2 && lk.hrefs.every(h => h === '/api/artifacts/' + 'a'.repeat(64)), JSON.stringify(lk.hrefs));
        check('403, 404, a network failure, an uppercase id, a short id, an id with a tail or a query and a bare id are not links: the 200 id has the only hrefs', lk.hrefs.length === 2 && !lk.hrefs.some(h => /\/\.\.|\?/.test(h)) && lk.text.includes('forbidden') && lk.text.includes('offline'), lk.text);
        check('one meta request per distinct lowercase 64-hex id (a, b, c, d, e, f: the tail and query ids are asked for their own hex, the bare one too), none for the uppercase or short ones', JSON.stringify([...lk.metas].sort()) === JSON.stringify(['a', 'b', 'c', 'd', 'e', 'f']), JSON.stringify(lk.metas));
        check('"Open the task" is the board path of the task, encoded', lk.open === '/board/team%20a', lk.open);
        const many = await ev(`(async () => { const c = await import(${S} + 'inbox_coral.js'); window.__reqs.length = 0;
            const ids = Array.from({ length: 30 }, (_, i) => 'a' + String(i).padStart(2, '0').repeat(1) + 'f'.repeat(61));
            const t = ids.concat(ids).map(x => 'coral://artifacts/' + x).join(' ');
            const pol = await c.artifactLinkPolicy(t);
            return { n: window.__reqs.filter(r => /\\/meta$/.test(r.p)).length, distinct: new Set(window.__reqs.map(r => r.p)).size, first: !!pol('coral://artifacts/' + ids[0]), beyond: pol('coral://artifacts/' + ids[25]) === undefined, junk: await c.artifactLinkPolicy(null).then(f => f('x')) === undefined }; })()`);
        check('at most 20 lookups, each id once, and an id past the 20th stays text', many.n === 20 && many.distinct === 20 && many.first && many.beyond && many.junk, JSON.stringify(many));
        // a refresh (the tab shown again) draws an open task whose updated_at did not change again without a read and without the lookups (#5803); a changed one is read and looked up again
        const metaCount = () => ev(`window.__reqs.filter(r => /\\/meta$/.test(r.p)).length`);
        const metasBefore = await metaCount();
        await ev(`(() => { const b = document.querySelector('#link-host .ibx-task-body'); b.dataset.mark = 'kept'; b.querySelector('textarea').value = 'half a reply'; b.querySelector('textarea').dispatchEvent(new Event('input', { bubbles: true })); window.__markNode = b.querySelector('.md'); return true; })()`);
        await ev(`window.__link.refresh(); true`);
        await waitFor(`document.querySelector('#link-host .ibx-task-head')?.getAttribute('aria-expanded') === 'true' && !document.querySelector('#link-host .ibx-task-body').hidden && !document.querySelector('#link-host .ibx-task-body [aria-busy]')`, 'the open task after a refresh', 4000);
        const again = await ev(`(() => { const b = document.querySelector('#link-host .ibx-task-body'); return { reads: window.__reads, same: b.querySelector('.md') === window.__markNode, draft: b.querySelector('textarea').value, hrefs: b.querySelectorAll('.md a').length }; })()`);
        check('a refresh with the task unchanged: no new read, no new lookup, the same drawn text, the draft and the links as they were', again.reads === 1 && (await metaCount()) === metasBefore && again.same && again.draft === 'half a reply' && again.hrefs === 2, JSON.stringify(again) + ' metas ' + metasBefore + '/' + await metaCount());
        await ev(`window.__linkStamp = '2026-10-21T11:00:00Z'; window.__link.refresh(); true`);
        await waitFor(`window.__reads === 2 && !!document.querySelector('#link-host .ibx-task-body .md') && !document.querySelector('#link-host .ibx-task-body [aria-busy]')`, 'the changed task read again', 4000);
        await waitFor(`window.__reqs.filter(r => /\\/meta$/.test(r.p)).length > ${metasBefore}`, 'the lookups again', 4000);
        check('a refresh with a changed updated_at reads the task and looks its links up again', (await metaCount()) > metasBefore && (await ev(`document.querySelector('#link-host .ibx-task-body textarea').value`)) === 'half a reply');
        // an amendment moves the revision and not updated_at: it is read again too
        const metasMid = await metaCount();
        await ev(`window.__linkRev = 2; window.__link.refresh(); true`);
        await waitFor(`window.__reads === 3 && !document.querySelector('#link-host .ibx-task-body [aria-busy]') && !!document.querySelector('#link-host .ibx-task-body .md')`, 'the amended task read again', 4000);
        await waitFor(`window.__reqs.filter(r => /\\/meta$/.test(r.p)).length > ${metasMid}`, 'the lookups again after an amendment', 4000);
        check('a refresh with a changed revision and the same updated_at reads the task and looks its links up again', (await metaCount()) > metasMid);
        // a task the list gives no revision for has no stamp: read every time
        await ev(`window.__linkRev = undefined; window.__link.refresh(); true`);
        await waitFor(`window.__reads === 4 && !document.querySelector('#link-host .ibx-task-body [aria-busy]')`, 'a task with no revision read again', 4000);
        await ev(`window.__link.refresh(); true`);
        await waitFor(`window.__reads === 5 && !document.querySelector('#link-host .ibx-task-body [aria-busy]')`, 'and again', 4000);
        check('a task with no revision (no stamp) is read on every refresh', (await ev(`window.__reads`)) === 5);
        await ev(`window.__link.destroy(); true`);
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { try { await client.close(); } catch (_) { /* closed */ } }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS inbox panel: ${results.length} checks`);
    process.exit(failed ? 1 : 0);
})();
