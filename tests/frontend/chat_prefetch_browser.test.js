// A page opened at /agents/<session id> asks for that chat's first page when it loads, before the live list names the agent (#4326, chat_page.js).
// (1) The early request goes out while the list is still pending, is the ONLY first-page request (the loader takes it over instead of making its own),
// and the chat renders from it. (2) When the early request is refused (a 401, say) the loader makes its own and the chat still renders. (3) The hand-over
// rules: a different session, a different page size or trim, a Hub or cloud page, a remote server's agent and the pop-out are none of them handed over
// (and the early request is aborted); a switch away aborts a taken one; an untaken one ends after its time. Drives the real dashboard over CDP.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const SID = '3f2c1a9e-5b7d-4e0a-9c11-0a1b2c3d4e5f';
const OTHER = '9a8b7c6d-1e2f-4a3b-8c4d-5e6f7a8b9c0d';

(async () => {
    const { client, Page, ev, waitFor, sleep, PRELOAD } = await connect();
    const ok = name => console.log(`   ✓ ${name}`);
    // After the harness's mocks: log chat requests, hold the list for LIST_MS, and (when __refuseEarly) refuse the request named by the session id.
    const WRAP = `(() => { const inner = window.fetch; window.__t = { chats: [], listAt: 0, listDone: 0 }; window.__refuseEarly = false;
        window.fetch = (url, opts = {}) => { const u = String(url); const at = performance.now();
            if (/^\\/api\\/sessions\\/live\\/[^/?]+\\/chat\\?/.test(u) && /after=0/.test(u) && !/before=/.test(u) && /offset=0/.test(u)) {
                const seg = decodeURIComponent(u.split('/')[4].split('?')[0]); window.__t.chats.push({ seg, at, url: u });
                if (seg === '${SID}' && window.__refuseEarly) { window.__refuseEarly = false; return Promise.resolve(new Response('{"error":"dashboard_auth_required"}', { status: 401 })); }
            }
            if (/^\\/api\\/sessions\\/live(\\?.*)?$/.test(u) && (opts.method || 'GET').toUpperCase() === 'GET') {
                window.__t.listAt = window.__t.listAt || at;
                return new Promise(res => setTimeout(() => inner(url, opts).then(r => { window.__t.listDone = window.__t.listDone || performance.now(); res(r); }), 900)); }
            return inner(url, opts); }; })();`;
    const SEED = `(() => { window.__m.add('pf-agent', 'P', 40, { sleeping: false, session_id: '${SID}' }); })();`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: SEED });
        await Page.addScriptToEvaluateOnNewDocument({ source: WRAP });

        // 1. the early request is the only first-page request, goes out before the list answers, and the chat renders from it
        await Page.navigate({ url: BASE + '/agents/' + SID });
        await Page.loadEventFired();
        await waitFor(`!!document.querySelector('#live-history-messages .message-text')`, 'the chat to render from the first page', 15000);
        let t = await ev('window.__t');
        assert.equal(t.chats.length, 1, 'one first-page request, the early one: ' + JSON.stringify(t.chats.map(c => c.seg)));
        assert.equal(t.chats[0].seg, SID, 'it names the session by its id (the list had not named the agent yet)');
        assert.ok(t.listDone, 'the list answered');
        assert.ok(t.chats[0].at < t.listDone - 300, `it went out ${(t.listDone - t.chats[0].at).toFixed(0)} ms before the list answered`);
        assert.match(t.chats[0].url, /limit=30&offset=0&trim=2048|trim=2048&|limit=30/, 'the first page\'s own size');
        const texts = await ev(`[...document.querySelectorAll('#live-history-messages .message-text')].map(e => e.textContent.slice(0, 3))`);
        assert.ok(texts.includes('P39') || texts.some(x => x.startsWith('P3')), 'the newest messages are shown: ' + texts.slice(-3));
        ok('the early request is the only first-page request, sent before the list answered, and renders the chat');

        // 2. an early request that is refused is redone by the loader
        await Page.addScriptToEvaluateOnNewDocument({ source: `window.__refuseEarly = true;` });
        await Page.navigate({ url: BASE + '/agents/' + SID });
        await Page.loadEventFired();
        await waitFor(`!!document.querySelector('#live-history-messages .message-text')`, 'the chat to render after a refused early request', 15000);
        t = await ev('window.__t');
        assert.deepEqual(t.chats.map(c => c.seg), [SID, 'pf-agent'], 'the refused early request, then the loader\'s own by the agent\'s name: ' + JSON.stringify(t.chats.map(c => c.seg)));
        assert.ok(t.chats[1].at - t.listDone < 400, `the loader redid it at once, not at a later poll (${(t.chats[1].at - t.listDone).toFixed(0)} ms after the list)`);
        ok('a refused early request is redone by the loader at once');

        // 3. the hand-over rules, on the module with a stub fetch
        const rules = await ev(`(async () => {
            const m = await import((window.__coralStatic || '/static/') + 'chat_page.js');
            const made = []; const fetchFn = (url, o) => { const r = { url, signal: o.signal }; made.push(r); return new Promise(() => {}); };
            const q = (over = {}) => new URLSearchParams({ session_id: '${SID}', after: '0', offset: '0', limit: String(m.FIRST_PAGE), trim: String(m.CHAT_TRIM), ...over });
            const out = {};
            const start = (o = {}) => m.startChatPrefetch({ pathname: '/agents/${SID}', fetchFn, ...o });
            const last = () => made[made.length - 1];
            // match
            out.started = start(); await Promise.resolve();
            const taken = m.takeChatPrefetch({ session_id: '${SID}' }, q());
            out.matched = !!taken; out.matchedLive = !last().signal.aborted;
            out.takenOnce = m.takeChatPrefetch({ session_id: '${SID}' }, q()) === null;
            taken.abort(); out.switchAborts = last().signal.aborted;
            // each mismatch is refused and aborts the early request
            for (const [label, session, params, opts] of [
                ['otherSession', { session_id: '${OTHER}' }, q(), {}],
                ['otherLimit', { session_id: '${SID}' }, q({ limit: '100' }), {}],
                ['otherTrim', { session_id: '${SID}' }, q({ trim: '0' }), {}],
                ['olderPage', { session_id: '${SID}' }, q({ offset: '30' }), {}],
                ['afterPoll', { session_id: '${SID}' }, q({ after: '5' }), {}],
                ['paramsOtherSession', { session_id: '${SID}' }, q({ session_id: '${OTHER}' }), {}],
                ['remoteServer', { session_id: '${SID}' }, q(), { local: false }],
                ['cloud', { session_id: '${SID}' }, q(), { cloud: true }],
            ]) { start(); await Promise.resolve(); const n = made.length; const r = m.takeChatPrefetch(session, params, opts); out[label] = { refused: r === null, aborted: made[n - 1].signal.aborted }; }
            // pages that start nothing
            const before = made.length;
            out.noStart = [m.startChatPrefetch({ pathname: '/agents/@srv/${SID}', fetchFn }), m.startChatPrefetch({ pathname: '/agent/${SID}', fetchFn }), m.startChatPrefetch({ pathname: '/agents/${SID}', fetchFn, cloud: true }),
                m.startChatPrefetch({ pathname: '/agents/not-a-uuid', fetchFn }), m.startChatPrefetch({ pathname: '/', fetchFn })];
            out.noStartMade = made.length - before;
            // an untaken early request ends after its time
            start({ ttlMs: 60 }); await Promise.resolve(); const tl = last();
            for (let i = 0; i < 100 && !tl.signal.aborted; i++) await new Promise(r => setTimeout(r, 20));
            out.ttlAborted = tl.signal.aborted; out.ttlGone = m.takeChatPrefetch({ session_id: '${SID}' }, q()) === null;
            m.dropChatPrefetch();
            return out; })()`);
        assert.deepEqual({ started: rules.started, matched: rules.matched, matchedLive: rules.matchedLive, takenOnce: rules.takenOnce, switchAborts: rules.switchAborts },
            { started: true, matched: true, matchedLive: true, takenOnce: true, switchAborts: true }, JSON.stringify(rules));
        for (const k of ['otherSession', 'otherLimit', 'otherTrim', 'olderPage', 'afterPoll', 'paramsOtherSession', 'remoteServer', 'cloud']) assert.deepEqual(rules[k], { refused: true, aborted: true }, k + ': ' + JSON.stringify(rules[k]));
        assert.deepEqual(rules.noStart, [false, false, false, false, false], 'no early request for a remote agent, the pop-out, the Hub, a bad id or another page');
        assert.equal(rules.noStartMade, 0);
        assert.deepEqual({ a: rules.ttlAborted, g: rules.ttlGone }, { a: true, g: true }, 'an untaken early request is aborted after its time');
        ok('handed over once on an exact match; a switch aborts it; every mismatch, the Hub, a remote server, the pop-out and a bad id get none; an untaken one ends');
        console.log('\nPASS the deep-link chat prefetch');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
