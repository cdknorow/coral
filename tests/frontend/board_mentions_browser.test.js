// @mentions in board posts (#5964) on the real board over the API: a mention of a current board member is a styled span (weight and a tint, never colour alone), whole names with spaces longest first,
// never inside code or a link, not an email, the Operator's own name stronger; 4.5:1 in both themes, no sideways scroll at 390. The board's Markdown export is the raw text (no span). Drives Chrome over CDP.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const B = 'mentions-' + Date.now().toString(36);
    const api = (m, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(m)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    const shot = async n => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; require('node:fs').mkdirSync(d, { recursive: true }); require('node:fs').writeFileSync(`${d}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        for (const [id, t] of [['boss', 'Orchestrator'], ['sec1', 'Security Reviewer Windows'], ['sec2', 'Security Reviewer'], ['qa', 'QA Engineer']]) assert.equal(await api('POST', `/api/board/${B}/subscribe`, { subscriber_id: id, job_title: t, session_name: id + '-s' }), 200);
        const posts = [
            ['boss', '@Security Reviewer Windows please review #1, then @Security Reviewer the rest.'],
            ['qa', '@Operator the build is green. cc @Orchestrator and @QA Engineer, @nobody and mail me at x@Orchestrator.example'],
            ['sec2', 'Run `@Orchestrator` in a shell, see [@Operator](https://example.com/x) and\n\n```\n@all in a block\n```\n\n@all: done.'],
        ];
        for (const [who, content] of posts) assert.equal(await api('POST', `/api/board/${B}/messages`, { subscriber_id: who, content }), 200);
        await Page.navigate({ url: `${BASE}/board/${B}` }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length >= 3`, 'the messages', 15000);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-mention').length >= 6`, 'the mentions are highlighted once the members are known', 15000);
        const got = await ev(`[...document.querySelectorAll('#mb-messages .mb-message-body')].map(b => [...b.querySelectorAll('.mb-mention')].map(s => s.textContent + (s.classList.contains('mb-mention-self') ? '*' : '') + (s.closest('code, pre, a') ? '!inside' : '')))`);
        const flat = got.flat();
        assert.deepEqual(got[0], ['@Security Reviewer Windows', '@Security Reviewer'], 'a name with spaces is one mention; the shorter member is its own');
        assert.deepEqual(got[1], ['@Operator*', '@Orchestrator', '@QA Engineer'], 'the Operator is stronger; @nobody and the email are not mentions');
        assert.deepEqual(got[2], ['@all'], 'not in the code span, the link or the block; the plain one is');
        assert.ok(!flat.some(x => /!inside/.test(x)));
        const style = await ev(`(() => { const a = document.querySelector('#mb-messages .mb-mention:not(.mb-mention-self)'), s = document.querySelector('#mb-messages .mb-mention-self'); const ca = getComputedStyle(a), cs = getComputedStyle(s); return { weight: ca.fontWeight, tint: ca.backgroundColor, selfTint: cs.backgroundColor, selfBar: cs.boxShadow, role: a.getAttribute('role') }; })()`);
        assert.ok(Number(style.weight) >= 600 && style.tint !== 'rgba(0, 0, 0, 0)', 'weight and a tint, not colour alone: ' + JSON.stringify(style));
        assert.notEqual(style.selfTint, style.tint, 'the own name is stronger'); assert.ok(style.selfBar !== 'none');
        assert.equal(style.role, null, 'text only: no role change');
        // the export is the raw Markdown: no span, the @ text as written
        const raw = await ev(`fetch('/api/board/${B}/messages/all?limit=10&format=dashboard').then(r => r.json()).then(d => JSON.stringify(d.messages.map(m => m.content)))`);
        assert.ok(!/mb-mention/.test(raw) && /@Security Reviewer Windows/.test(raw), 'the stored text has no markup');
        await installAudit(ev);
        for (const [w, h, mobile, tag] of [[1280, 900, false, 'desktop'], [390, 844, true, '390']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            for (const theme of ['dark', 'light']) {
                await setTheme(ev, theme);
                const au = await auditRegion(ev, ['#mb-messages .mb-message-body']);
                assert.ok(au.checked > 0 && !au.fails.length, `${tag} ${theme}: 4.5:1 ${JSON.stringify(au.fails).slice(0, 300)}`);
                assert.ok((await ev(`document.documentElement.scrollWidth - innerWidth`)) <= 0, `${tag}: no sideways scroll`);
                await shot(`mentions-${tag}-${theme}`);
            }
        }
        await setTheme(ev, 'dark');
        console.log('PASS board @mentions');
    } finally { try { await removeBoards(ev, [B]); } catch {} await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
