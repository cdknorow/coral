// #3723: the live chat's first page is 30 messages, and chat pages carry at most 2 KB of each long
// tool field (?trim=2048: an output, an edit's text, a written file), marked trimmed. Opening a
// card whose tool or output was shortened loads that one message whole (limit=1&before=<its
// index + 1>, no trim) and shows it; a message that changed underneath is not shown in its place.
//
// /chat is answered by an in-page mock that follows the server: limit/before/offset paging with
// start, and trim cutting tool_result content and tool_uses' write_content/old_string/new_string.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 5000) => waitFor(expr, what, { timeout: ms, soft: true });
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`(async () => {
            const { state } = await import(${S} + 'state.js');
            const liveChat = await import(${S} + 'live_chat.js');
            const sessions = await import(${S} + 'sessions.js');
            window.T = { state, liveChat, sessions };
            let c = document.getElementById('live-history-messages');
            if (!c) { c = document.createElement('div'); c.id = 'live-history-messages'; }
            const host = document.createElement('div');
            host.style.cssText = 'position:fixed;left:0;top:0;width:700px;height:600px;z-index:99999;background:#fff;display:flex;flex-direction:column';
            c.style.cssText = 'flex:1;overflow-y:auto;min-height:0';
            c.style.setProperty('display', 'block', 'important');
            host.appendChild(c);
            document.body.appendChild(host);

            // 40 plain messages, then a turn with long tool fields (indexes 40..45).
            const msgs = [];
            for (let i = 0; i < 40; i++) msgs.push(i % 2 ? { type: 'assistant', text: 'A' + i + ' lorem' } : { type: 'user', content: 'U' + i + ' lorem' });
            const oldText = Array.from({ length: 500 }, (_, i) => 'old line ' + i).join('\\n');
            const output = 'y'.repeat(5000) + ' END-OF-OUTPUT';
            msgs.push({ type: 'user', content: 'U40 please edit' });
            msgs.push({ type: 'assistant', text: 'A41 editing', tool_uses: [
                { name: 'Edit', tool_use_id: 'e1', input_summary: 'a.go', old_string: oldText, new_string: 'new line' },
                { name: 'Bash', tool_use_id: 'b1', command: 'ls' },
                { name: 'Write', tool_use_id: 'w1', input_summary: 'b.go', write_content: 'z'.repeat(4000) },
            ] });
            msgs.push({ type: 'tool_result', tool_use_id: 'b1', tool_name: 'Bash', content: output });
            msgs.push({ type: 'tool_result', tool_use_id: 'e1', tool_name: 'Edit', content: 'ok' });
            msgs.push({ type: 'tool_result', tool_use_id: 'w1', tool_name: 'Write', content: 'written' });
            msgs.push({ type: 'assistant', text: 'A45 done' });

            const cut = (s, n) => s.length > n ? s.slice(0, n) : s;
            const trimMsg = (m, n) => {
                let out = m;
                if (m.type === 'tool_result' && m.content.length > n) out = { ...m, content: cut(m.content, n), trimmed: true };
                if (m.tool_uses) {
                    let any = false;
                    const tools = m.tool_uses.map(t => {
                        let tc = t;
                        for (const f of ['write_content', 'old_string', 'new_string', 'patch']) {
                            if (typeof t[f] === 'string' && t[f].length > n) { tc = { ...tc, [f]: cut(t[f], n), trimmed: true }; any = true; }
                        }
                        return tc;
                    });
                    if (any) out = { ...m, tool_uses: tools, trimmed: true };
                }
                return out;
            };
            const mock = window.__mock = { msgs, requests: [], changed: {} };
            const real = window.fetch.bind(window);
            window.fetch = async (url, opts) => {
                const m = String(url).match(/\\/api\\/sessions\\/live\\/qa-trim\\/chat\\?(.*)$/);
                if (!m) return real(url, opts);
                const p = new URLSearchParams(m[1]);
                mock.requests.push(Object.fromEntries(p));
                const all = mock.msgs, after = Number(p.get('after') || 0);
                let body;
                if (after > 0) body = { messages: all.slice(after), total: all.length };
                else {
                    const limit = Number(p.get('limit')) || 100;
                    const end = p.has('before') ? Math.min(Number(p.get('before')), all.length) : Math.max(all.length - Number(p.get('offset') || 0), 0);
                    const start = Math.max(end - limit, 0);
                    let page = all.slice(start, end).map((x, i) => mock.changed[start + i] || x);
                    const trim = Number(p.get('trim')) || 0;
                    if (trim > 0) page = page.map(x => trimMsg(x, trim));
                    body = { messages: page, total: all.length, start, has_more: start > 0 };
                }
                return new Response(JSON.stringify(body), { headers: { 'content-type': 'application/json' } });
            };
            state.liveSessions = [{ session_id: 'qa-trim-id', name: 'qa-trim', display_name: 'qa-trim', agent_type: 'claude', working_directory: '/tmp/qa-trim', server: 'local' }];
            return true;
        })()`);

        await ev(`T.sessions.selectLiveSession('qa-trim', 'claude', 'qa-trim-id', 'local')`);
        await until(`!!document.querySelector('#live-history-messages .tool-call[data-tool-use-id="b1"]')`, 'the first page');
        await ev(`T.liveChat.stopLiveHistoryPoll(); true`);
        const first = await ev(`window.__mock.requests.find(r => r.after === '0' && !r.before)`);
        check('the first page asks for 30 messages', first && first.limit === '30', JSON.stringify(first));
        check('with trim=2048', first && first.trim === '2048');

        const card = id => `document.querySelector('#live-history-messages .tool-call[data-tool-use-id="${id}"]')`;
        const out = id => ev(`(${card(id)}.querySelector('.tool-card-output pre') || {}).textContent || ''`);
        const before = await out('b1');
        check('a long output comes shortened', before.length === 2048 && !before.includes('END-OF-OUTPUT'), String(before.length));
        check('and its card knows the message to load', (await ev(`${card('b1')}.dataset.trimResult`)) === '42');

        // ── Opening the card loads the output whole ───────────────────────
        let n = await ev(`window.__mock.requests.length`);
        await ev(`${card('b1')}.open = true; true`);
        await until(`(${card('b1')}.querySelector('.tool-card-output pre').textContent || '').includes('END-OF-OUTPUT')`, 'the full output');
        let reqs = (await ev(`window.__mock.requests`)).slice(n);
        check('opening it loads that one message, without trim', reqs.length === 1 && reqs[0].limit === '1' && reqs[0].before === '43' && !reqs[0].trim, JSON.stringify(reqs));
        check('and shows the whole output', (await out('b1')).length === 5000 + ' END-OF-OUTPUT'.length);
        n = await ev(`window.__mock.requests.length`);
        await ev(`(() => { const c = ${card('b1')}; c.open = false; return true; })()`);
        await ev(`new Promise(r => setTimeout(r, 50))`); // sleep-ok: the toggle event is queued
        await ev(`(() => { const c = ${card('b1')}; c.open = true; return true; })()`);
        await ev(`new Promise(r => setTimeout(r, 100))`); // sleep-ok: a second load would have been sent by now
        check('opening it again loads nothing', (await ev(`window.__mock.requests.length`)) === n);

        // ── A shortened edit ──────────────────────────────────────────────
        const dels = () => ev(`${card('e1')}.querySelectorAll('.diff-content .diff-line').length`);
        const delsBefore = await dels();
        check('a long edit comes shortened', delsBefore > 0 && delsBefore < 200 && (await ev(`${card('e1')}.dataset.trimUse`)) === '41', String(delsBefore));
        await ev(`${card('e1')}.open = true; true`);
        await until(`${card('e1')}.dataset.trimUse === undefined`, 'the full edit');
        const delsAfter = await dels();
        check('opening it shows the whole edit', delsAfter >= 500, `${delsBefore} → ${delsAfter} diff lines`);
        check('its output stays in the card', /\bok\b/.test(await out('e1')));

        // ── A message that changed underneath ─────────────────────────────
        await ev(`window.__mock.changed[41] = { type: 'assistant', text: 'something else', tool_uses: [{ name: 'Bash', tool_use_id: 'other', command: 'pwd' }] }; true`);
        await ev(`${card('w1')}.open = true; true`);
        await until(`!!${card('w1')}.querySelector('.tool-card-trim-note')`, 'the note');
        const note = await ev(`(${card('w1')}.querySelector('.tool-card-trim-note') || {}).textContent || ''`);
        check('a message that changed is not shown in its place: the card says it was shortened', /Shortened/.test(note) && !(await ev(`${card('w1')}.textContent.includes('pwd')`)), note);

        // ── Older pages are trimmed too ───────────────────────────────────
        n = await ev(`window.__mock.requests.length`);
        await ev(`T.liveChat.loadMoreHistory()`);
        await until(`window.__mock.requests.length > ${n}`, 'an older page');
        const older = (await ev(`window.__mock.requests`)).slice(n)[0];
        check('an older page asks with trim=2048 too', older && older.trim === '2048' && older.before === '16', JSON.stringify(older));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
