// The chat's history refresh (#4702): refreshLiveHistory (live_chat.js) is a 214-line function that loads an agent's conversation: the kept page of an agent seen a moment ago, the loading line, the first
// page and then only what came after, a busy answer, an error with its Retry, a transcript that got shorter, the open prompt, login and usage-limit notices that ride on the reply, and the pending
// bubbles, the working row, the empty state and the scroll afterwards. Splitting it must change nothing. This refreshes the history in every situation and records the chat, what was asked for, the
// load-more state and the notices. Compared with fixtures/live_history_golden.json (sha256 per step, with the readable lines). CORAL_UPDATE_GOLDEN=1 rewrites it. Drives Chrome over CDP through
// agent_view_harness.js against the isolated dev server; /chat is answered by the test.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'live_history_golden.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');

const MSGS = [
    { type: 'user', content: 'Please fix the build' }, { type: 'assistant', text: 'Looking at it now.' }, { type: 'user', content: 'and add a test' },
    { type: 'assistant', text: 'Done: <b>both</b>.' }, { type: 'user', content: '[Request interrupted by user]' }, { type: 'assistant', text: 'Stopped.' },
];

// [name, setup expression (page side, before the call), calls to make, optional follow-up expression]
const STEPS = [
    ['first load with messages and older ones', `__chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 4), total: 6, start: 2, has_more: true } };`, 1],
    ['a poll that brings new messages', `__chat.answer = { status: 200, body: { messages: __chat.msgs.slice(4), total: 6, start: 2, has_more: true } };`, 1],
    ['a poll with nothing new', `__chat.answer = { status: 200, body: { messages: [], total: 6, start: 2, has_more: true } };`, 1],
    ['an empty conversation', `__reset(); __chat.answer = { status: 200, body: { messages: [], total: 0, start: 0, has_more: false } };`, 1],
    ['the transcript got shorter: loaded again from its start', `__reset(); __chat.answer = { status: 200, body: { messages: __chat.msgs, total: 6, start: 0, has_more: false } };`, 1,
        `__chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 2), total: 2, start: 0, has_more: false } };`],
    ['the first load fails with the server error', `__reset(); __chat.answer = { status: 500, body: { error: 'the transcript is unreadable' } };`, 1],
    ['the first load fails with a reply that is not JSON', `__reset(); __chat.answer = { status: 200, raw: 'not json' };`, 1],
    ['Retry after a failure loads again', `__chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 2), total: 2, start: 0, has_more: false } }; document.querySelector('.chat-retry-btn').click();`, 0],
    ['the first load is refused as busy', `__reset(); __chat.answer = { status: 503, body: { error: 'busy' }, headers: { 'Retry-After': '30' } };`, 1],
    ['an open prompt, a login notice and a usage limit ride on the reply', `__reset(); __chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 2), total: 2, start: 0, has_more: false,
        pending: { name: 'Bash', input: { command: 'rm -rf x' } }, prompt_options: { question: 'Run it?', options: ['Yes', 'No'], review: [] }, needs_login: true, login_reason: 'expired', limit_notice: 'You hit the usage limit' } };`, 1],
    ['the kept page of an agent seen a moment ago shows at once', `__chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 4), total: 6, start: 2, has_more: true } }; __reset();`, 1,
        `__chat.answer = { status: 200, body: { messages: __chat.msgs.slice(4), total: 6, start: 2, has_more: true } };`],
    ['a kept page: the agent is switched while it builds', `__reset(); __chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 4), total: 6, start: 2, has_more: true } };`, 1],
    ['a kept page: its build throws, so the first page is loaded as for an agent not seen before', `__reset(); __chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 4), total: 6, start: 2, has_more: true } };`, 1],
    ['a switch while the answer is on its way renders nothing', `__reset(); __chat.delay = 150; __chat.answer = { status: 200, body: { messages: __chat.msgs.slice(0, 2), total: 2, start: 0, has_more: false } };`, 1,
        `__reset();`],
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, chat: await import(st + 'live_chat.js') };
            Object.defineProperty(document, 'hidden', { get: () => false });
            let c = document.getElementById('live-history-messages');
            if (!c) { c = document.createElement('div'); c.id = 'live-history-messages'; document.body.appendChild(c); }
            window.__chat = { msgs: ${JSON.stringify(MSGS)}, answer: null, delay: 0, requests: [] };
            const realAppend = Element.prototype.appendChild; Element.prototype.appendChild = function (n) { if (window.__throwBuild && !this.isConnected) throw new Error('the build was made to fail'); return realAppend.call(this, n); }; // a kept page is built off screen: this makes that build throw
            window.__reset = () => { T.chat.clearKeptChatPages(); T.chat.resetLiveHistory(); window.__chat.delay = 0; };
            window.__first = () => window.T.chat.refreshLiveHistory();
            const real = window.fetch;
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href);
                if (/\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(p.pathname)) {
                    const q = Object.fromEntries([...p.searchParams.entries()].filter(([k]) => ['after', 'limit', 'offset', 'trim'].includes(k)));
                    window.__chat.requests.push(JSON.stringify(q));
                    const a = window.__chat.answer || { status: 200, body: { messages: [], total: 0, start: 0 } };
                    const respond = () => new Response(a.raw !== undefined ? a.raw : JSON.stringify(a.body), { status: a.status, headers: Object.assign({ 'content-type': 'application/json' }, a.headers || {}) });
                    return new Promise((res, rej) => { const done = () => res(respond()); if (o.signal) o.signal.addEventListener('abort', () => rej(new DOMException('aborted', 'AbortError'))); setTimeout(done, window.__chat.delay); }); }
                return real(u, o); };
            T.state.currentSession = { name: 'agent-x', type: 'live', session_id: 'sess-x', agent_type: 'claude', working_directory: '/work/x' };
            T.state.autoScroll = true; window.__reset();
            return true; })()`);
        const SNAP = `(() => { const c = document.getElementById('live-history-messages'), T = window.T;
            const norm = s => s.replace(/\\d{1,2}:\\d{2}(:\\d{2})?\\s?(AM|PM)?/g, '<time>');
            return { html: norm(c.innerHTML), text: norm(c.textContent.replace(/\\s+/g, ' ').trim()), older: T.chat.chatHasOlder(), requests: window.__chat.requests.filter((r, i, a) => r !== a[i - 1]).join(' ; '), // a refused load retries as soon as it may: how often is timing, so repeats count once
               
                loading: !!c.querySelector('.loading-indicator'), busy: !!c.querySelector('.chat-history-busy'), error: !!c.querySelector('.chat-load-error'), more: !!c.querySelector('.load-more, .chat-load-more, [data-on-click*="load-more"]'),
                kids: c.children.length, classes: [...c.children].map(e => e.className.split(' ')[0]).join(',') }; })()`;
        for (const [name, setup, calls, after] of STEPS) {
            await ev(`window.__chat.requests.length = 0; ${setup}; true`);
            for (let i = 0; i < calls; i++) { await ev(`window.T.chat.refreshLiveHistory(); true`); await ev(`new Promise(r => setTimeout(r, 400))`); } // sleep-ok: the answer, the render and the retry land over several frames; there is no completion event
            if (calls === 0) await ev(`new Promise(r => setTimeout(r, 500))`); // sleep-ok: the retry's request and render land over several frames; there is no completion event
            if (name.startsWith('the kept page')) { await ev(`${after}; window.T.chat.refreshLiveHistory(); true`); await ev(`new Promise(r => setTimeout(r, 400))`); // sleep-ok: the poll's answer and render land over several frames
            await ev(`window.__chat.requests.length = 0; __chat.answer = { status: 200, body: { messages: [], total: 6, start: 2, has_more: true } }; window.T.chat.resetLiveHistory(); window.T.chat.refreshLiveHistory(); true`); await ev(`new Promise(r => setTimeout(r, 500))`); } // sleep-ok: the kept page is built in slices
            if (name.startsWith('a kept page: the agent is switched')) { await ev(`window.__chat.requests.length = 0; window.T.chat.resetLiveHistory(); window.T.chat.refreshLiveHistory(); window.T.chat.resetLiveHistory(); true`); await ev(`new Promise(r => setTimeout(r, 600))`); } // sleep-ok: the kept page is built in slices and the answer, if one were asked for, lands over frames
            if (name.startsWith('a kept page: its build throws')) { await ev(`window.__chat.requests.length = 0; window.__throwBuild = true; window.T.chat.resetLiveHistory(); window.T.chat.refreshLiveHistory(); true`); await ev(`new Promise(r => setTimeout(r, 700))`); await ev(`window.__throwBuild = false; true`); } // sleep-ok: the failed build, then the first page's request and render, land over frames
            if (name.startsWith('a switch while')) { await ev(`window.__chat.requests.length = 0; window.T.chat.refreshLiveHistory(); new Promise(r => setTimeout(r, 30)).then(() => window.__reset()); true`); await ev(`new Promise(r => setTimeout(r, 500))`); } // sleep-ok: the answer is delayed on purpose
            if (name.startsWith('the transcript got shorter')) { await ev(`${after}; true`); await ev(`window.T.chat.refreshLiveHistory(); true`); await ev(`new Promise(r => setTimeout(r, 600))`); } // sleep-ok: the reload is scheduled after the refresh ends
            out[name] = await ev(SNAP);
        }
        assert.match(out['first load with messages and older ones'].text, /Please fix the build/);
        assert.equal(out['the first load fails with the server error'].error, true);
        const hashes = {}, lines = {};
        for (const [name, s] of Object.entries(out)) { hashes[name] = sha(JSON.stringify(s)); lines[name] = `kids ${s.kids} [${s.classes}] | older ${s.older} | loading ${s.loading} busy ${s.busy} error ${s.error} | requests ${s.requests} | ${s.text.slice(0, 400)}`; }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what refreshLiveHistory showed and asked for at each step, for live_history_golden_browser.test.js: sha256 of the chat html, the requests and the load-more state, and the readable lines. CORAL_UPDATE_GOLDEN=1 rewrites it.', hashes, lines }, null, 2) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' steps');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same steps');
        for (const k of Object.keys(want.hashes)) if (hashes[k] !== want.hashes[k]) assert.fail(`${k}: changed.\n  now:  ${lines[k]}\n  was:  ${want.lines[k]}`);
        console.log(`PASS live history golden: ${Object.keys(want.hashes).length} steps identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
