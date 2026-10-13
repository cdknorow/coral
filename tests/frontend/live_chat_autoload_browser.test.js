// Live chat paging (#3021): small first page, older history autoloaded on
// scroll-up, keyset paging (before=<start>) so messages appended meanwhile do
// not duplicate, and fast agent switching that never shows a stale transcript.
//
// /chat is answered by an in-page mock that follows the server's semantics:
// offset counts back from the end, before=<absolute index> ends the page there,
// and the response carries start (the page's first absolute index). The
// "old server" mode drops start and ignores before, like a remote that has not
// been updated, which the client must page with offset.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;

// The first page must be smaller than the old 400-message page.
const MAX_FIRST_PAGE = 200;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const waitFor = async (expression, what, timeoutMs = 5000) => {
        const until = Date.now() + timeoutMs;
        for (;;) {
            if (await ev(expression)) return;
            if (Date.now() > until) throw new Error(`timed out waiting for ${what}`);
            await new Promise(r => setTimeout(r, 20));
        }
    };
    const sleep = ms => new Promise(r => setTimeout(r, ms));

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();

        await ev(`(async () => {
            const { state } = await import('/static/state.js');
            const liveChat = await import('/static/live_chat.js');
            const sessions = await import('/static/sessions.js');
            window.T = { state, liveChat, sessions };

            // A visible, fixed-size scroller, so scroll events and the
            // sentinel's IntersectionObserver behave as in the app.
            let c = document.getElementById('live-history-messages');
            if (!c) { c = document.createElement('div'); c.id = 'live-history-messages'; }
            const host = document.createElement('div');
            host.id = 'qa-chat-host';
            host.style.cssText = 'position:fixed;left:0;top:0;width:700px;height:600px;z-index:99999;background:#fff;display:flex;flex-direction:column';
            // The app's CSS hides the transcript outside chat mode.
            c.style.cssText = 'flex:1;overflow-y:auto;min-height:0';
            c.style.setProperty('display', 'block', 'important');
            c.style.setProperty('visibility', 'visible', 'important');
            host.appendChild(c);
            document.body.appendChild(host);

            const filler = 'lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua';
            const make = (prefix, i, short) => {
                const text = prefix + i + ' ' + (short ? 'lorem' : filler);
                return i % 2 === 0 ? { type: 'user', content: text } : { type: 'assistant', text };
            };

            const mock = window.__chatMock = {
                sessions: {},   // name -> { prefix, msgs, delay, olderDelay, failOlder, oldServer }
                requests: [],
                add(name, prefix, n, opts = {}) {
                    const msgs = [];
                    for (let i = 0; i < n; i++) msgs.push(make(prefix, i, opts.short));
                    this.sessions[name] = { prefix, msgs, delay: 0, olderDelay: 0, failOlder: 0, oldServer: false, refuseBefore: 0, refuseBody: { error: 'path not allowed' }, failStatus: 500, failBody: { error: 'boom' }, server: 'local', ...opts };
                    state.liveSessions = (state.liveSessions || []).filter(s => s.name !== name).concat([{
                        session_id: name + '-id', name, display_name: name, agent_type: 'claude', working_directory: '/tmp/' + name,
                        server: this.sessions[name].server,
                    }]);
                },
                append(name, k) {
                    const s = this.sessions[name];
                    for (let j = 0; j < k; j++) s.msgs.push(make(s.prefix, s.msgs.length, s.short));
                },
            };

            const origFetch = window.fetch.bind(window);
            window.fetch = (url, opts = {}) => {
                const m = String(url).match(/\\/api\\/sessions\\/live\\/([^/?]+)\\/chat\\?(.*)$/);
                if (!m || !mock.sessions[decodeURIComponent(m[1])]) return origFetch(url, opts);
                const name = decodeURIComponent(m[1]);
                const s = mock.sessions[name];
                const p = new URLSearchParams(m[2]);
                const after = Number(p.get('after') || 0);
                const older = after === 0 && (p.has('before') || Number(p.get('offset') || 0) > 0);
                const req = { name, after, limit: p.get('limit'), offset: p.get('offset'), before: p.get('before'),
                    older, aborted: false, settled: false, at: performance.now() };
                mock.requests.push(req);
                const delay = older ? s.olderDelay : s.delay;
                return new Promise((resolve, reject) => {
                    const respond = () => {
                        req.settled = true;
                        // A route allowlist in front of the machine (the Cloud Hub tunnel) refusing before=.
                        if (s.refuseBefore && p.has('before')) {
                            resolve(new Response(JSON.stringify(s.refuseBody), { status: s.refuseBefore, headers: { 'content-type': 'application/json' } }));
                            return;
                        }
                        if (older && s.failOlder > 0) {
                            s.failOlder--;
                            resolve(new Response(JSON.stringify(s.failBody), { status: s.failStatus, headers: { 'content-type': 'application/json' } }));
                            return;
                        }
                        const all = s.msgs;
                        let body;
                        if (after > 0) {
                            body = { messages: all.slice(Math.min(after, all.length)), total: all.length };
                        } else {
                            const limit = Number(p.get('limit')) > 0 ? Number(p.get('limit')) : 100;
                            const end = (!s.oldServer && p.has('before'))
                                ? Math.min(Math.max(Number(p.get('before')), 0), all.length)
                                : Math.max(all.length - Number(p.get('offset') || 0), 0);
                            const start = Math.max(end - limit, 0);
                            body = { messages: all.slice(start, end), total: all.length, has_more: start > 0 };
                            if (!s.oldServer) body.start = start;
                        }
                        resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } }));
                    };
                    const timer = setTimeout(respond, delay);
                    if (opts.signal) {
                        if (opts.signal.aborted) { clearTimeout(timer); req.aborted = true; reject(new DOMException('aborted', 'AbortError')); return; }
                        opts.signal.addEventListener('abort', () => {
                            if (req.settled) return;
                            clearTimeout(timer);
                            req.aborted = true;
                            reject(new DOMException('aborted', 'AbortError'));
                        });
                    }
                });
            };

            // The rendered transcript as [prefix, index] pairs, in DOM order.
            window.__rendered = () => {
                const out = [];
                for (const el of document.querySelectorAll('#live-history-messages .message-text')) {
                    const m = el.textContent.match(/^\\s*([A-Z])(\\d+) lorem/);
                    if (m) out.push(m[1] + m[2]);
                }
                return out;
            };
            window.__atBottom = () => { const c = document.getElementById('live-history-messages'); return c.clientHeight > 0 && c.scrollHeight > c.clientHeight && c.scrollHeight - c.scrollTop - c.clientHeight < 60; };
        })()`);

        // Open a session the way the app does when an agent is picked.
        const open = name => ev(`(async () => {
            window.__chatMock.requests.length = 0;
            // No autoScroll reset here: a switch must open at the bottom on its own (#3029).
            await T.sessions.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(name + '-id')}, window.__chatMock.sessions[${JSON.stringify(name)}].server);
        })()`);
        const rendered = () => ev('window.__rendered()');
        const requests = () => ev('window.__chatMock.requests.map(r => ({...r}))');
        const expectRange = (ids, prefix, from, to, what) => {
            const want = [];
            for (let i = from; i < to; i++) want.push(prefix + i);
            assert.deepEqual(ids, want, `${what}: expected ${prefix}${from}..${prefix}${to - 1} contiguous (got ${ids.length} items, first ${ids[0]}, last ${ids[ids.length - 1]})`);
        };
        const scrollTop = () => ev(`(() => { const c = document.getElementById('live-history-messages'); c.scrollTop = 0; c.dispatchEvent(new Event('scroll')); })()`);
        const idle = () => waitFor(`window.__chatMock.requests.every(r => r.settled || r.aborted)`, 'all /chat requests to settle');

        // Stop the app's own 1s poll between cases; cases poll by hand.
        const stopPoll = () => ev('T.liveChat.stopLiveHistoryPoll()');

        console.log('1. Switching to an agent loads one small page from the end');
        await ev(`window.__chatMock.add('qa-a', 'A', 1000)`);
        await open('qa-a');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        let reqs = await requests();
        const initial = reqs.filter(r => !r.older && r.after === 0);
        assert.equal(initial.length, 1, 'one initial /chat request per switch');
        const pageSize = Number(initial[0].limit);
        assert.ok(pageSize > 0 && pageSize <= MAX_FIRST_PAGE, `first page limit=${initial[0].limit} should be 1..${MAX_FIRST_PAGE}`);
        expectRange(await rendered(), 'A', 1000 - pageSize, 1000, 'first page');
        assert.ok(await ev(`!!document.querySelector('#live-history-messages .chat-history-sentinel')`), 'sentinel shown while older history exists');
        assert.ok(await ev('window.__atBottom()'), 'a newly opened chat starts at the bottom');
        console.log(`   ✓ page=${pageSize}, sentinel present, at bottom`);

        console.log('2. Scrolling to the top autoloads exactly one older page and keeps the view anchored');
        await ev(`window.__chatMock.sessions['qa-a'].olderDelay = 150`);
        await ev(`window.__chatMock.requests.length = 0`);
        const anchorBefore = await ev(`(() => {
            const c = document.getElementById('live-history-messages');
            c.scrollTop = 0;
            const el = [...c.querySelectorAll('.message-text')].find(e => /^\\s*A\\d+ lorem/.test(e.textContent));
            el.dataset.qaAnchor = '1';
            return el.getBoundingClientRect().top - c.getBoundingClientRect().top;
        })()`);
        // Several scroll events while the page is in flight: still one fetch.
        for (let i = 0; i < 5; i++) { await scrollTop(); await sleep(10); }
        await waitFor(`window.__chatMock.requests.some(r => r.older && r.settled)`, 'older page');
        await idle();
        await sleep(50);
        reqs = await requests();
        assert.equal(reqs.filter(r => r.older).length, 1, `one older fetch while one is in flight (got ${reqs.filter(r => r.older).length})`);
        const olderSize = Number(reqs.find(r => r.older).limit);
        assert.ok(olderSize > 0, 'older page has a limit');
        assert.equal(reqs.find(r => r.older).before, String(1000 - pageSize), 'older page is requested with before=<start of the rendered page>');
        expectRange(await rendered(), 'A', Math.max(1000 - pageSize - olderSize, 0), 1000, 'after one older page');
        const anchorAfter = await ev(`(() => {
            const c = document.getElementById('live-history-messages');
            return c.querySelector('[data-qa-anchor]').getBoundingClientRect().top - c.getBoundingClientRect().top;
        })()`);
        assert.ok(Math.abs(anchorAfter - anchorBefore) <= 2, `view stays anchored (moved ${anchorAfter - anchorBefore}px)`);
        console.log('   ✓ one fetch, before= keyset, anchored');

        console.log('3. Scrolling to the top repeatedly reaches the first message, then stops');
        await ev(`window.__chatMock.sessions['qa-a'].olderDelay = 0`);
        for (let i = 0; i < 40 && await ev(`!!document.querySelector('#live-history-messages .chat-history-sentinel')`); i++) {
            await scrollTop();
            await sleep(30);
            await idle();
        }
        expectRange(await rendered(), 'A', 0, 1000, 'whole transcript');
        assert.equal(await ev(`!!document.querySelector('#live-history-messages .chat-history-sentinel')`), false, 'sentinel removed at the first message');
        await ev(`window.__chatMock.requests.length = 0`);
        await scrollTop();
        await sleep(300);
        assert.equal((await requests()).filter(r => r.older).length, 0, 'no older fetch once the start is reached');
        console.log('   ✓ m0..m999 contiguous, sentinel gone, no further fetches');

        console.log('4. Messages appended between the first page and an older page are not duplicated');
        await ev(`window.__chatMock.add('qa-b', 'B', 1000)`);
        await open('qa-b');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        await ev(`window.__chatMock.append('qa-b', 5)`);
        await ev('T.liveChat.refreshLiveHistory()');
        await waitFor(`window.__rendered().slice(-1)[0] === 'B1004'`, 'poll to append B1000..B1004');
        await scrollTop();
        await waitFor(`window.__chatMock.requests.some(r => r.older && r.settled)`, 'older page');
        await idle();
        await sleep(50);
        expectRange(await rendered(), 'B', Math.max(1000 - pageSize - olderSize, 0), 1005, 'after append + older page');
        console.log('   ✓ no duplicates, no gap');

        console.log('5. An old remote (no start, before ignored) is paged with offset');
        await ev(`window.__chatMock.add('qa-c', 'C', 1000, { oldServer: true })`);
        await open('qa-c');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        await scrollTop();
        await waitFor(`window.__chatMock.requests.some(r => r.older && r.settled)`, 'older page');
        await idle();
        await sleep(50);
        reqs = await requests();
        const olderOld = reqs.find(r => r.older);
        assert.equal(olderOld.offset, String(pageSize), 'without start the client pages with offset');
        expectRange(await rendered(), 'C', Math.max(1000 - pageSize - olderSize, 0), 1000, 'old-server older page');
        console.log('   ✓ offset fallback renders the right page');

        // Appends counted into offset: an old remote's offset page must not overlap.
        await ev(`window.__chatMock.add('qa-c2', 'Q', 1000, { oldServer: true })`);
        await open('qa-c2');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        await ev(`window.__chatMock.append('qa-c2', 5)`);
        await ev('T.liveChat.refreshLiveHistory()');
        await waitFor(`window.__rendered().slice(-1)[0] === 'Q1004'`, 'poll to append Q1000..Q1004');
        await scrollTop();
        await waitFor(`window.__chatMock.requests.some(r => r.older && r.settled)`, 'older page');
        await idle();
        await sleep(50);
        assert.equal((await requests()).find(r => r.older).offset, String(pageSize + 5), 'offset counts the appended messages');
        expectRange(await rendered(), 'Q', Math.max(1000 - pageSize - olderSize, 0), 1005, 'old server after append + older page');
        console.log('   ✓ old remote: appended messages counted into offset, no duplicates');

        console.log('6. Rapid switching with slow responses shows only the last agent');
        await ev(`(() => {
            const m = window.__chatMock;
            m.add('qa-d', 'D', 600, { delay: 400 });
            m.add('qa-e', 'E', 600, { delay: 250 });
            m.add('qa-f', 'F', 600, { delay: 60 });
            m.requests.length = 0;
            // No awaits between switches: each one supersedes the last.
            T.sessions.selectLiveSession('qa-d', 'claude', 'qa-d-id', 'local');
            T.sessions.selectLiveSession('qa-e', 'claude', 'qa-e-id', 'local');
            T.sessions.selectLiveSession('qa-f', 'claude', 'qa-f-id', 'local');
        })()`);
        await waitFor(`window.__rendered().length > 0`, 'final agent');
        await sleep(600); // past the slow agents' delays
        await stopPoll();
        let ids = await rendered();
        assert.ok(ids.every(x => x[0] === 'F'), `only qa-f is shown (saw ${[...new Set(ids.map(x => x[0]))].join(',')})`);
        expectRange(ids, 'F', 600 - pageSize, 600, 'final agent');
        reqs = await requests();
        const stale = reqs.filter(r => r.name !== 'qa-f' && r.after === 0 && !r.older);
        assert.ok(stale.length > 0 && stale.every(r => r.aborted), 'superseded agents\' first-page requests are aborted');
        assert.ok(await ev('window.__atBottom()'), 'final agent starts at the bottom');
        console.log(`   ✓ only F rendered; ${stale.length} stale requests aborted`);

        console.log('7. Switching while an older page is loading drops it');
        await ev(`window.__chatMock.add('qa-g', 'G', 1000)`);
        await ev(`window.__chatMock.add('qa-h', 'H', 300)`);
        await open('qa-g');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        await ev(`window.__chatMock.sessions['qa-g'].olderDelay = 500`);
        await scrollTop();
        await waitFor(`window.__chatMock.requests.some(r => r.older)`, 'older page in flight');
        const olderReq = (await requests()).findIndex(r => r.older);
        await ev(`(async () => {
            await T.sessions.selectLiveSession('qa-h', 'claude', 'qa-h-id', 'local');
        })()`);
        await waitFor(`window.__rendered().length > 0 && window.__rendered()[0][0] === 'H'`, 'qa-h');
        await sleep(650);
        await stopPoll();
        ids = await rendered();
        assert.ok(ids.every(x => x[0] === 'H'), 'no qa-g messages after switching away');
        const all = await ev('window.__chatMock.requests.map(r => ({...r}))');
        // The request list was cleared by open() only before qa-g; the older one is still in it.
        assert.ok(all[olderReq] && all[olderReq].older && all[olderReq].aborted, 'the in-flight older page was aborted');
        assert.ok(await ev('window.__atBottom()'), 'the new agent starts at the bottom');
        console.log('   ✓ older page aborted, new agent clean');

        console.log('8. Short transcripts need no paging; a failed older page does not loop');
        await ev(`window.__chatMock.add('qa-i', 'I', 10)`);
        await open('qa-i');
        await stopPoll();
        await waitFor(`window.__rendered().length === 10`, 'short transcript');
        assert.equal(await ev(`!!document.querySelector('#live-history-messages .chat-history-sentinel')`), false, 'no sentinel for a transcript that fits one page');
        await scrollTop();
        await sleep(200);
        assert.equal((await requests()).filter(r => r.older).length, 0, 'no older fetch for a short transcript');

        await ev(`window.__chatMock.add('qa-j', 'J', 1000, { failOlder: 1, server: 'qa-srv-fail' })`);
        await open('qa-j');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        await scrollTop();
        await waitFor(`window.__chatMock.requests.some(r => r.older && r.settled)`, 'failing older page');
        // Sitting at the top after a failure must not refetch in a loop.
        for (let i = 0; i < 10; i++) { await scrollTop(); await sleep(50); }
        const failedTries = (await requests()).filter(r => r.older).length;
        assert.ok(failedTries <= 2, `a failed older page is retried at most once without user action (got ${failedTries})`);
        await ev(`(() => { const s = document.querySelector('#live-history-messages .chat-history-sentinel'); const b = s && (s.matches('button') ? s : s.querySelector('button')); b && b.click(); })()`);
        await waitFor(`window.__rendered()[0] !== 'J${1000 - pageSize}'`, 'retry via the sentinel button');
        await idle();
        expectRange(await rendered(), 'J', Math.max(1000 - pageSize - olderSize, 0), 1000, 'after retry');
        console.log(`   ✓ ${failedTries} failed tries, retry button recovers`);

        console.log('9. A first page that does not fill the view keeps loading without a scroll');
        // One-line messages in a tall view: the first page leaves the sentinel in sight.
        await ev(`document.getElementById('qa-chat-host').style.height = '20000px'`);
        await ev(`window.__chatMock.add('qa-k', 'K', 1000, { short: true })`);
        await open('qa-k');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        {
        // 100 one-line messages cannot fill a 20000px view, so older pages must load without a scroll
        // (they may already have by now: the check is that they did, once each).
        await waitFor(`window.__chatMock.requests.some(r => r.older)`, 'an older page for a view the first page does not fill', 2000);
        // It keeps going until the view is filled or the start is reached, one page at a time.
        await waitFor(`(() => { const c = document.getElementById('live-history-messages'); return c.scrollHeight > c.clientHeight || !document.querySelector('#live-history-messages .chat-history-sentinel'); })()`, 'the view to fill', 5000);
        await idle();
        await sleep(200);
        await idle();
        const olderReqs = (await requests()).filter(r => r.older);
        const before = olderReqs.map(r => Number(r.before));
        assert.equal(new Set(before).size, before.length, `each older page is fetched once (before=${before.join(',')})`);
        const ids = await rendered();
        expectRange(ids, 'K', 1000 - ids.length, 1000, 'autofilled view');
        console.log(`   ✓ ${olderReqs.length} older pages loaded without a scroll, ${ids.length} msgs, no refetch`);
        }
        await ev(`document.getElementById('qa-chat-host').style.height = '600px'`);

        console.log('10. find-in-chat can still pull older pages');
        await ev(`window.__chatMock.add('qa-l', 'L', 1000)`);
        await open('qa-l');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        assert.equal(await ev('T.liveChat.chatHasOlder()'), true, 'chatHasOlder while older pages remain');
        await ev('T.liveChat.loadMoreHistory()');
        await idle();
        expectRange(await rendered(), 'L', Math.max(1000 - pageSize - olderSize, 0), 1000, 'loadMoreHistory page');
        console.log('   ✓ loadMoreHistory pages contiguously');

        console.log('11. Timing: switch to first paint (2000-message agent, reported)');
        await ev(`window.__chatMock.add('qa-t', 'T', 2000)`);
        await ev(`window.__chatMock.add('qa-u', 'U', 2000)`);
        const times = [];
        for (let i = 0; i < 6; i++) {
            const name = i % 2 ? 'qa-u' : 'qa-t';
            times.push(await ev(`(async () => {
                const t0 = performance.now();
                await T.sessions.selectLiveSession('${name}', 'claude', '${name}-id', 'local');
                void document.getElementById('live-history-messages').scrollHeight;
                await new Promise(r => requestAnimationFrame(() => setTimeout(r, 0)));
                T.liveChat.stopLiveHistoryPoll();
                return performance.now() - t0;
            })()`));
        }
        times.sort((a, b) => a - b);
        const median = (times[2] + times[3]) / 2;
        console.log(`   switch→paint median ${median.toFixed(1)}ms (min ${times[0].toFixed(1)}, max ${times[5].toFixed(1)}), page ${pageSize}`);
        assert.ok(median < 500, `switch→paint median ${median.toFixed(1)}ms under 500ms`);

        console.log('12. A server that refuses before= is paged by offset; a transient error does not switch it');
        // The refusals as they arrive: a Cloud Hub on an older allowlist answers 404 not_found; a
        // hub whose connector is older answers 403 path_not_allowed; 400 is a machine rejecting the
        // value; 502 with code path_not_allowed is accepted as well.
        for (const [status, body] of [
            [404, { error: 'not_found' }],
            [403, { error: 'path not allowed', code: 'path_not_allowed' }],
            [400, { error: 'before must be a non-negative integer' }],
            [502, { error: 'path not allowed', code: 'path_not_allowed' }],
        ]) {
            const name = 'qa-refuse-' + status;
            const opts = { refuseBefore: status, refuseBody: body, server: 'qa-srv-refuse-' + status };
            await ev(`window.__chatMock.add('${name}', 'R', 1000, ${JSON.stringify(opts)})`);
            await open(name);
            await stopPoll();
            await waitFor(`window.__rendered().length > 0`, 'first page');
            await ev(`window.__chatMock.append('${name}', 3)`);
            await ev('T.liveChat.refreshLiveHistory()');
            await waitFor(`window.__rendered().slice(-1)[0] === 'R1002'`, 'poll to append');
            await scrollTop();
            await waitFor(`window.__chatMock.requests.filter(r => r.older && r.settled).length >= 2`, 'refused page and its offset retry');
            await idle();
            await sleep(50);
            reqs = (await requests()).filter(r => r.older);
            assert.equal(reqs.length, 2, `${status}: one refused request and one retry (got ${reqs.length})`);
            assert.ok(reqs[0].before !== null && reqs[1].before === null, `${status}: the retry drops before=`);
            assert.equal(reqs[1].offset, String(pageSize + 3), `${status}: the retry pages by offset, counting appended messages`);
            expectRange(await rendered(), 'R', Math.max(1000 - pageSize - olderSize, 0), 1003, `${status}: after the refused page`);
            // The next page goes straight to offset: one refusal per server, not per page.
            await ev(`window.__chatMock.requests.length = 0`);
            await scrollTop();
            await waitFor(`window.__chatMock.requests.some(r => r.older && r.settled)`, 'next older page');
            await idle();
            await sleep(50);
            reqs = (await requests()).filter(r => r.older);
            assert.equal(reqs.length, 1, `${status}: next page is a single request`);
            assert.equal(reqs[0].before, null, `${status}: next page sends no before=`);
            expectRange(await rendered(), 'R', Math.max(1000 - pageSize - 2 * olderSize, 0), 1003, `${status}: second older page`);
            console.log(`   ✓ ${status}: refused once, offset fallback, no duplicates`);
        }
        // The offset retry is made once: when it fails too, paging stops until the reader asks.
        await ev(`window.__chatMock.add('qa-refuse-twice', 'W', 1000, { refuseBefore: 404, refuseBody: { error: 'not_found' }, failOlder: 1, failStatus: 404, failBody: { error: 'not_found' }, server: 'qa-srv-refuse-twice' })`);
        await open('qa-refuse-twice');
        await stopPoll();
        await waitFor(`window.__rendered().length > 0`, 'first page');
        await scrollTop();
        await waitFor(`window.__chatMock.requests.filter(r => r.older && r.settled).length >= 2`, 'refused page and its failed retry');
        for (let i = 0; i < 10; i++) { await scrollTop(); await sleep(80); }
        await idle();
        reqs = (await requests()).filter(r => r.older);
        assert.equal(reqs.length, 2, `a refused retry ends paging: one refused + one retry, nothing more (got ${JSON.stringify(reqs.map(r => ({ before: r.before, offset: r.offset })))})`);
        assert.ok(reqs[0].before !== null && reqs[1].before === null, 'the retry dropped before=');
        expectRange(await rendered(), 'W', 1000 - pageSize, 1000, 'nothing prepended after the failed retry');
        assert.ok(await ev(`(() => { const s = document.querySelector('#live-history-messages .chat-history-sentinel'); return !!(s && (s.matches('button') ? s : s.querySelector('button'))); })()`), 'the retry button stays');
        await ev(`(() => { const s = document.querySelector('#live-history-messages .chat-history-sentinel'); (s.matches('button') ? s : s.querySelector('button')).click(); })()`);
        await waitFor(`window.__rendered()[0] !== 'W${1000 - pageSize}'`, 'older page after a click');
        await idle();
        reqs = (await requests()).filter(r => r.older);
        assert.equal(reqs.length, 3, 'a click makes one request');
        assert.equal(reqs[2].before, null, 'the click pages by offset: the server stays refused');
        expectRange(await rendered(), 'W', Math.max(1000 - pageSize - olderSize, 0), 1000, 'after the click');
        console.log('   ✓ refused retry stops paging; the button pages by offset');

        // Transient errors are not refusals: no offset retry, and keyset paging continues there.
        for (const [status, body] of [[500, { error: 'boom' }], [502, { error: 'remote unreachable' }], [429, { error: 'rate limited' }], [401, { error: 'unauthorized' }]]) {
            const name = 'qa-transient-' + status;
            const opts = { failOlder: 1, failStatus: status, failBody: body, server: 'qa-srv-transient-' + status };
            await ev(`window.__chatMock.add('${name}', 'X', 1000, ${JSON.stringify(opts)})`);
            await open(name);
            await stopPoll();
            await waitFor(`window.__rendered().length > 0`, 'first page');
            await scrollTop();
            await waitFor(`window.__chatMock.requests.some(r => r.older && r.settled)`, 'failing older page');
            await idle();
            await sleep(100);
            reqs = (await requests()).filter(r => r.older);
            assert.equal(reqs.length, 1, `${status}: a transient error is not retried without before= (requests: ${JSON.stringify(reqs.map(r => ({ before: r.before, offset: r.offset })))})`);
            await ev(`(() => { const s = document.querySelector('#live-history-messages .chat-history-sentinel'); const b = s && (s.matches('button') ? s : s.querySelector('button')); b && b.click(); })()`);
            await waitFor(`window.__rendered()[0] !== 'X${1000 - pageSize}'`, `${status}: older page after the error`);
            await idle();
            reqs = (await requests()).filter(r => r.older);
            assert.notEqual(reqs[reqs.length - 1].before, null, `${status}: the server is still paged with before=`);
            expectRange(await rendered(), 'X', Math.max(1000 - pageSize - olderSize, 0), 1000, `${status}: after the error and a retry`);
            console.log(`   ✓ ${status} (${body.error}): no fallback, keyset paging continues`);
        }

        console.log('\nAll live chat autoload checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
