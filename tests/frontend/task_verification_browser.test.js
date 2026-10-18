// A finished task's verification badge (#4564): Verified (who confirmed it on hover), Evidence attached, Unverified ("Marked done by the agent; no reviewer or check confirmed it"), in the
// board's Tasks list (the right sidebar's Tasks tab) and the task's detail, with a filter (all, verified, evidence, unverified). A label, never a blocker: the text is the label (not colour alone),
// it reaches 4.5:1 in both themes, a screen reader reads its sentence, and it fits a 390 px phone. The tasks are the real isolated server's; the verification field (Backend's #4563: {state, by, at})
// is added to its answers in the page, and a task without the field gets no badge.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const BOARD = 'verify-4564-board';
// The server's field, by title: v- verified (two reviewers), e- evidence (one name), u- unverified, n- none sent (an older server).
const ADD = `(() => {
    const prev = window.fetch;
    const verification = t => {
        const at = '2026-10-17T20:15:00Z';
        if (/^v-/.test(t.title)) return { state: 'verified', by: ['QA Engineer', 'Security Reviewer'], at };
        if (/^e-/.test(t.title)) return { state: 'evidence', by: 'the completion message', at };
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
    const rows = () => ev(`[...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')].map(r => ({ title: r.querySelector('.board-task-desc').textContent.trim(), badge: (() => { const b = r.querySelector('.board-task-verify'); return b ? { text: b.lastChild.textContent.trim(), cls: b.className.replace('board-task-verify', '').trim(), aria: b.getAttribute('aria-label'), title: b.title, role: b.getAttribute('role') } : null; })() }))`);
    // WCAG contrast of the badge text against what is behind it, and of its icon.
    const CONTRAST = `(() => {
        const lum = c => { const [r, g, b] = c.map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }); return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
        const parse = s => { const m = s.match(/rgba?\\(([^)]+)\\)/); if (!m) return null; const p = m[1].split(',').map(x => parseFloat(x)); return { rgb: p.slice(0, 3), a: p.length > 3 ? p[3] : 1 }; };
        // What shows is the colour blended over what is behind it by every opacity on the way up (a dimmed row dims its badge).
        const opacityOf = el => { let o = 1; for (let e = el; e; e = e.parentElement) o *= parseFloat(getComputedStyle(e).opacity); return o; };
        const bgOf = el => { let bg = [255, 255, 255]; const chain = []; for (let e = el; e; e = e.parentElement) chain.push(e); for (const e of chain.reverse()) { const c = parse(getComputedStyle(e).backgroundColor); if (c && c.a > 0) bg = bg.map((v, i) => Math.round(c.rgb[i] * c.a + v * (1 - c.a))); } return bg; };
        const ratio = (a, b) => { const l1 = lum(a), l2 = lum(b); return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05); };
        const out = {};
        for (const b of document.querySelectorAll('#board-task-list .board-task-verify')) {
            const st = b.classList.contains('verified') ? 'verified' : b.classList.contains('evidence') ? 'evidence' : 'unverified';
            const bg = bgOf(b), o = opacityOf(b);
            const seen = rgb => rgb.map((v, i) => Math.round(v * o + bg[i] * (1 - o)));
            const worst = out[st] || { text: 99, icon: 99 };
            const text = Math.round(ratio(seen(parse(getComputedStyle(b).color).rgb), bg) * 100) / 100, icon = Math.round(ratio(seen(parse(getComputedStyle(b.querySelector('.material-icons')).color).rgb), bg) * 100) / 100;
            out[st] = { text: Math.min(worst.text, text), icon: Math.min(worst.icon, icon) };
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
        for (const title of ['v-one', 'v-two', 'e-one', 'u-one', 'u-two', 'n-one']) {
            const t = await post('/tasks', { title, body: 'b ' + title, created_by: 'Operator' });
            await post('/tasks/claim', { subscriber_id: 'worker', task_id: t.id });
            await post('/tasks/' + t.id + '/complete', { subscriber_id: 'worker', message: 'done ' + title });
        }
        await post('/tasks', { title: 'open-one', body: 'still open', created_by: 'Operator' });
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board'`, 'the board page');
        await ev(`window.toggleAgenticPanel(true); document.getElementById('agentic-tab-tasks').click(); true`);
        await waitFor(`document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length === 7`, 'seven task rows', 10000, `JSON.stringify({ n: document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length, txt: document.getElementById('board-task-list').textContent.slice(0, 160), tab: document.getElementById('agentic-panel-tasks').className, bt: (T.state.currentBoardTasks || []).length, page: document.getElementById('agentic-state').dataset.page, proj: T.state.boardPageProject })`);

        console.log('1. Each finished task shows its state in words, with who confirmed it on hover and in its accessible name; open tasks and a task the server sent no state for show none');
        const r1 = Object.fromEntries((await rows()).map(r => [r.title.replace(/^.*?((?:v|e|u|n)-\w+|open-one)$/, '$1'), r.badge]));
        assert.deepEqual(r1['v-one'].text, 'Verified'); assert.equal(r1['v-one'].cls, 'verified');
        assert.equal(r1['v-one'].aria, r1['v-one'].title, 'the hover text is the accessible name');
        assert.match(r1['v-one'].title, /^Verified by QA Engineer, Security Reviewer, /);
        assert.equal(r1['v-one'].role, 'img');
        assert.equal(r1['e-one'].text, 'Evidence attached'); assert.equal(r1['e-one'].cls, 'evidence');
        assert.match(r1['e-one'].title, /Evidence attached \(the completion message\); no reviewer or check has confirmed it/);
        assert.equal(r1['u-one'].text, 'Unverified'); assert.equal(r1['u-one'].cls, 'unverified');
        assert.equal(r1['u-one'].title, 'Marked done by the agent; no reviewer or check confirmed it');
        assert.equal(r1['n-one'], null, 'no state from the server, no badge');
        assert.equal(r1['open-one'], null, 'an open task has none');

        await shot('verify-list-dark');
        console.log('2. The task detail names it too');
        await ev(`[...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')].find(r => r.textContent.includes('v-two')).click(); true`);
        await waitFor(`!!document.querySelector('#task-detail-content .task-detail-meta .board-task-verify')`, 'the badge in the detail', 8000);
        assert.equal(await ev(`document.querySelector('#task-detail-content .task-detail-meta .board-task-verify').lastChild.textContent.trim()`), 'Verified');
        await ev(`document.querySelector('#task-detail-modal [data-on-click="task-close"]').click(); true`);

        console.log('3. The filter: all, verified, evidence attached, unverified; open tasks always stay; the choice is remembered');
        const select = `document.querySelector('.board-task-verify-select')`;
        assert.equal(await ev(`!!${select}`), true, 'the filter is there');
        const titles = async () => (await rows()).map(r => r.title.replace(/^.*?((?:v|e|u|n)-\w+|open-one)$/, '$1')).sort();
        const pick = async v => { await ev(`${select}.value = '${v}'; ${select}.dispatchEvent(new Event('change', { bubbles: true })); true`); await sleep(150); }; // sleep-ok: the list redraws on the change; no completion event
        await pick('verified'); assert.deepEqual(await titles(), ['open-one', 'v-one', 'v-two']);
        await pick('evidence'); assert.deepEqual(await titles(), ['e-one', 'open-one']);
        await pick('unverified'); assert.deepEqual(await titles(), ['open-one', 'u-one', 'u-two']);
        assert.equal(await ev(`localStorage.getItem('coral-task-verify-filter')`), 'unverified');
        await pick('all'); assert.equal((await titles()).length, 7);

        console.log('4. The text reaches 4.5:1 and the icon 3:1 in both themes');
        for (const theme of ['dark', 'light']) {
            await ev(`import((window.__coralStatic || '/static/') + 'settings_core.js').then(m => { m.clearCustomThemeVars(); m.applyTheme('${theme}'); return true; })`);
            await sleep(400); // sleep-ok: the theme is applied over a frame
            const c = await ev(CONTRAST);
            assert.deepEqual(Object.keys(c).sort(), ['evidence', 'unverified', 'verified']);
            if (theme === 'light') await shot('verify-list-light');
            for (const [st, v] of Object.entries(c)) { assert.ok(v.text >= 4.5, `${theme}: the ${st} text is ${v.text}:1`); assert.ok(v.icon >= 3, `${theme}: the ${st} icon is ${v.icon}:1`); }
        }

        console.log('5. A 390 px phone, touch: the badges and the filter fit, the filter is a 44 px target, nothing scrolls sideways');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await sleep(400); // sleep-ok: the layout settles over frames after a viewport change; there is no completion event
        await ev(`window.toggleAgenticPanel(true); document.getElementById('agentic-tab-tasks').click(); true`);
        await waitFor(`document.querySelectorAll('#board-task-list .board-task-verify').length === 5`, 'the badges on the phone', 8000);
        const phone = await ev(`(() => { const sel = document.querySelector('.board-task-verify-select').getBoundingClientRect(); const dock = document.getElementById('agentic-state');
            const over = [...document.querySelectorAll('#board-task-list .board-task-verify')].filter(b => b.getBoundingClientRect().right > dock.getBoundingClientRect().right + 1 || b.getBoundingClientRect().left < dock.getBoundingClientRect().left - 1).length;
            return { selH: Math.round(sel.height), over, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        assert.ok(phone.selH >= 44, 'the filter is a touch target: ' + JSON.stringify(phone));
        assert.equal(phone.over, 0, 'no badge is cut off: ' + JSON.stringify(phone));
        assert.ok(phone.page <= 0, 'no sideways scroll: ' + JSON.stringify(phone));
        await shot('verify-list-phone');
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
