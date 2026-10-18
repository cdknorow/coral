// A finished task's verification, quietly (#4564, operator feedback): in the board's Tasks list a small muted icon after the title only for Evidence attached,
// its words only in the tooltip and the accessible name, and nothing at all for a verified or an unverified one; in the task detail one plain muted line ("Verified by ..." or "Not independently verified (evidence attached)"), none for unverified, no chip; no filter in the
// list (the API's filter is for agents and coral-hub). The icon reaches 3:1 in both themes and fits a 390 px phone. The tasks are the real isolated server's; the verification field (Backend's #4563:
// {state, by, at}) is added to its answers in the page, and a task without the field gets nothing.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const BOARD = 'verify-4564-board';
// The server's field, by title: v- verified (two reviewers), e- evidence (by: []), u- unverified, n- none sent (an older server).
const ADD = `(() => {
    const prev = window.fetch;
    const verification = t => {
        const at = '2026-10-17T20:15:00Z';
        if (/^v-/.test(t.title)) return { state: 'verified', by: ['QA Engineer', 'Security Reviewer'], at };
        if (t.title === 'e-gate') return { state: 'evidence', by: ['gate:report'], at }; // a gate's report names itself (df6a32ff)
        if (/^e-/.test(t.title)) return { state: 'evidence', by: [], at }; // as #4563 sends it: no one confirmed it
        if (/^u-/.test(t.title)) return { state: 'unverified', by: [], at };
        return undefined;
    };
    window.fetch = async (url, opts = {}) => {
        const resp = await prev(url, opts);
        if (!/\\/api\\/board\\/[^/?]+\\/tasks(\\/\\d+)?(\\?|$)/.test(String(url)) || (opts.method && opts.method !== 'GET')) return resp;
        const body = await resp.clone().json().catch(() => null);
        if (!body) return resp;
        const tag = t => { if (t && typeof t === 'object' && t.title && t.status === 'completed') { const v = verification(t); if (v) t.verification = v; } return t; };
        if (Array.isArray(body)) body.forEach(tag); else if (Array.isArray(body.tasks)) body.tasks.forEach(tag); else tag(body);
        return new Response(JSON.stringify(body), { status: resp.status, headers: { 'content-type': 'application/json' } });
    };
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        await sleep(350); // sleep-ok: tab strips fade; a picture mid-fade shows two tabs lit
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const rows = () => ev(`[...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')].map(r => ({ title: r.querySelector('.board-task-desc').textContent.replace(/[a-z_]+$/, m => m).trim(), mark: (() => { const b = r.querySelector('.board-task-verify-mark'); return b ? { cls: b.className.replace('board-task-verify-mark', '').trim(), aria: b.getAttribute('aria-label'), title: b.title, role: b.getAttribute('role'), words: b.textContent.trim() } : null; })() }))`);
    // WCAG contrast of each mark's icon against what is behind it, counting every opacity on the way up (a dimmed row dims what is in it).
    const CONTRAST = `(() => {
        const lum = c => { const [r, g, b] = c.map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }); return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
        const parse = s => { const m = s.match(/rgba?\\(([^)]+)\\)/); if (!m) return null; const p = m[1].split(',').map(x => parseFloat(x)); return { rgb: p.slice(0, 3), a: p.length > 3 ? p[3] : 1 }; };
        const opacityOf = el => { let o = 1; for (let e = el; e; e = e.parentElement) o *= parseFloat(getComputedStyle(e).opacity); return o; };
        const bgOf = el => { let bg = [255, 255, 255]; const chain = []; for (let e = el; e; e = e.parentElement) chain.push(e); for (const e of chain.reverse()) { const c = parse(getComputedStyle(e).backgroundColor); if (c && c.a > 0) bg = bg.map((v, i) => Math.round(c.rgb[i] * c.a + v * (1 - c.a))); } return bg; };
        const ratio = (a, b) => { const l1 = lum(a), l2 = lum(b); return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05); };
        const out = {};
        for (const b of document.querySelectorAll('#board-task-list .board-task-verify-mark')) {
            const st = 'evidence';
            const bg = bgOf(b), o = opacityOf(b);
            const seen = rgb => rgb.map((v, i) => Math.round(v * o + bg[i] * (1 - o)));
            const icon = Math.round(ratio(seen(parse(getComputedStyle(b.querySelector('.material-icons')).color).rgb), bg) * 100) / 100;
            out[st] = Math.min(out[st] || 99, icon);
        }
        return out;
    })()`;

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear()`);
        await ev(ADD);
        // A board with finished tasks of each kind and an open one, through the real API.
        const post = async (path, body) => { const r = await api('POST', `/api/board/${BOARD}${path}`, body); assert.ok(r.status < 300, path + ': ' + JSON.stringify(r)); return r.body; };
        for (const title of ['v-one', 'v-two', 'e-one', 'e-gate', 'u-one', 'u-two', 'n-one']) {
            const t = await post('/tasks', { title, body: 'b ' + title, created_by: 'Operator' });
            await post('/tasks/claim', { subscriber_id: 'worker', task_id: t.id });
            await post('/tasks/' + t.id + '/complete', { subscriber_id: 'worker', message: 'done ' + title });
        }
        await post('/tasks', { title: 'open-one', body: 'still open', created_by: 'Operator' });
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board'`, 'the board page');
        await ev(`window.toggleAgenticPanel(true); document.getElementById('agentic-tab-tasks').click(); true`);
        await waitFor(`document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length === 8`, 'eight task rows', 10000, `JSON.stringify({ n: document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length, txt: document.getElementById('board-task-list').textContent.slice(0, 160), tab: document.getElementById('agentic-panel-tasks').className, bt: (T.state.currentBoardTasks || []).length, page: document.getElementById('agentic-state').dataset.page, proj: T.state.boardPageProject })`);

        console.log('1. Quiet: no chip, no words, no filter in the list; a small muted icon after the title only for evidence attached (the words are its tooltip and accessible name); nothing for a verified task, an unverified one, an open one, or one the server sent no state for');
        const norm = r => r.title.replace(/[a-z_]*$/, '').trim();
        const r1 = Object.fromEntries((await rows()).map(r => [r.title.match(/(?:v|e|u|n)-[\w-]+|open-one/)[0].replace(/(help_outline|description|verified_user)$/, ''), r.mark]));
        assert.equal(r1['v-one'], null, 'a verified task shows nothing'); assert.equal(r1['v-two'], null);
        assert.equal(r1['n-one'], null, 'no state from the server, nothing shown'); assert.equal(r1['open-one'], null, 'an open task has none');
        assert.equal(r1['e-one'].cls, 'evidence'); assert.equal(r1['e-one'].title, 'Evidence attached; no reviewer or check has confirmed it'); assert.equal(r1['e-one'].aria, r1['e-one'].title); assert.equal(r1['e-one'].role, 'img');
        assert.equal(r1['e-gate'].title, 'Evidence attached (gate:report); no reviewer or check has confirmed it');
        assert.equal(r1['u-one'], null, 'an unverified task shows nothing in the list'); assert.equal(r1['u-two'], null);
        assert.deepEqual(await ev(`({ chips: document.querySelectorAll('#board-task-list .board-task-verify').length, filter: document.querySelectorAll('.board-task-verify-select, .board-task-verify-filter').length,
            marks: [...document.querySelectorAll('#board-task-list .board-task-verify-mark')].map(m => m.textContent.trim().length > 0 && /^[a-z_]+$/.test(m.textContent.trim())).every(Boolean) })`), { chips: 0, filter: 0, marks: true }, 'no chip, no filter, and a mark is an icon with no words');
        await shot('verify-list-dark');

        console.log('2. The task detail has one plain muted line (no chip)');
        const detailLine = async title => {
            await ev(`[...document.querySelectorAll('#board-task-list .board-task-item')].find(r => r.textContent.includes(${JSON.stringify(title)})).click(); true`);
            await waitFor(`!!document.querySelector('#task-detail-content .task-detail-title') && document.querySelector('#task-detail-content .task-detail-title').textContent.includes(${JSON.stringify(title)})`, 'the detail of ' + title, 8000);
            if (title === 'e-one') await shot('verify-detail');
            const out = await ev(`({ line: (document.querySelector('#task-detail-content .task-detail-verification') || {}).textContent || null, chip: !!document.querySelector('#task-detail-content .board-task-verify, #task-detail-content .board-task-verify-mark') })`);
            await ev(`document.querySelector('#task-detail-modal [data-on-click="task-close"]').click(); true`);
            await sleep(120); // sleep-ok: the dialog closes over a frame
            return out;
        };
        let d = await detailLine('v-two'); assert.match(d.line, /^Verified by QA Engineer, Security Reviewer, /); assert.equal(d.chip, false);
        d = await detailLine('e-one'); assert.equal(d.line, 'Not independently verified (evidence attached)');
        d = await detailLine('e-gate'); assert.equal(d.line, 'Not independently verified (evidence attached: gate:report)');
        d = await detailLine('u-one'); assert.equal(d.line, null, 'an unverified task has no line');
        d = await detailLine('n-one'); assert.equal(d.line, null, 'no state from the server, no line');

        console.log('3. The icon reaches 3:1 in both themes');
        for (const theme of ['dark', 'light']) {
            await ev(`import((window.__coralStatic || '/static/') + 'settings_core.js').then(m => { m.clearCustomThemeVars(); m.applyTheme('${theme}'); return true; })`);
            await sleep(400); // sleep-ok: the theme is applied over a frame
            if (theme === 'light') await shot('verify-list-light');
            const c = await ev(CONTRAST);
            assert.deepEqual(Object.keys(c).sort(), ['evidence']);
            for (const [st, v] of Object.entries(c)) assert.ok(v >= 3, `${theme}: the ${st} icon is ${v}:1`);
        }

        console.log('4. A 390 px phone, touch: the icons fit the row and nothing scrolls sideways');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await sleep(400); // sleep-ok: the layout settles over frames after a viewport change; there is no completion event
        await ev(`window.toggleAgenticPanel(true); document.getElementById('agentic-tab-tasks').click(); true`);
        await waitFor(`document.querySelectorAll('#board-task-list .board-task-verify-mark').length === 2`, 'the icons on the phone', 8000);
        const phone = await ev(`(() => { const dock = document.getElementById('agentic-state').getBoundingClientRect();
            const over = [...document.querySelectorAll('#board-task-list .board-task-verify-mark')].filter(b => b.getBoundingClientRect().right > dock.right + 1 || b.getBoundingClientRect().left < dock.left - 1).length;
            return { over, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        assert.equal(phone.over, 0, 'no icon is cut off: ' + JSON.stringify(phone));
        assert.ok(phone.page <= 0, 'no sideways scroll: ' + JSON.stringify(phone));
        await shot('verify-list-phone');
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
