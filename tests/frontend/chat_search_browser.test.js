const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Refusing production port');

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;

    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        // Also run in the static-template fixture, where app.js is intentionally absent.
        await ev(`(async()=>{window.selectHistorySession=(await import('/static/sessions.js')).selectHistorySession;window.selectBoardProject=(await import('/static/message_board.js')).selectBoardProject;})()`);

        console.log('Testing repaired chat search UI in real browser session via CDP...');

        // 1. Verify search container and list exist
        const elementsExist = await ev(`(() => {
            const container = document.getElementById('chat-search-results');
            const list = document.getElementById('history-sessions-list');
            return { hasContainer: !!container, hasList: !!list };
        })()`);
        assert.ok(elementsExist.hasContainer, '#chat-search-results must exist in DOM');
        assert.ok(elementsExist.hasList, '#history-sessions-list must exist in DOM');

        // 2. Exercise searchChats with backend response status: "complete"
        const searchCompleteResult = await ev(`(async () => {
            const { searchChats } = await import('/static/chat_search.js');
            const origFetch = window.fetch;
            window.fetch = async (url) => {
                if (url.includes('/api/sessions/history/search')) {
                    return new Response(JSON.stringify({
                        status: 'complete',
                        total: 1,
                        results: [{
                            session_id: 'test-agent-sess-1',
                            type: 'agent',
                            last_timestamp: '2026-09-30T12:00:00Z',
                            hits: [{
                                role: 'user',
                                timestamp: '2026-09-30T12:00:00Z',
                                excerpt: 'reproduce search highlight in browser',
                                match_offsets: [[10, 16]],
                                locator: { session_id: 'test-agent-sess-1', message_index: 85 }
                            }]
                        }]
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }
                return origFetch(url);
            };

            await searchChats('search');
            window.fetch = origFetch;

            const container = document.getElementById('chat-search-results');
            const hit = container.querySelector('.chat-search-hit');
            const mark = hit ? hit.querySelector('mark') : null;
            const markStyle = mark ? getComputedStyle(mark) : null;
            return {
                text: container.textContent,
                hitCount: container.querySelectorAll('.chat-search-hit').length,
                markText: mark ? mark.textContent : null,
                markWeight: markStyle ? markStyle.fontWeight : null,
                isError: container.textContent === 'Search is temporarily unavailable.'
            };
        })()`);

        console.log('Search result with status="complete":', searchCompleteResult);
        assert.equal(searchCompleteResult.isError, false, 'status="complete" must NOT be treated as an error');
        assert.equal(searchCompleteResult.hitCount, 1, 'Rendered 1 hit card for status="complete"');
        assert.equal(searchCompleteResult.markText, 'search', 'Highlighted matched excerpt with <mark>');
        assert.equal(searchCompleteResult.markWeight, '700', 'Matched excerpt is visibly bold');

        // 3. Exercise searchChats with status: "partial"
        const searchPartialResult = await ev(`(async () => {
            const { searchChats } = await import('/static/chat_search.js');
            const origFetch = window.fetch;
            window.fetch = async (url) => {
                if (url.includes('/api/sessions/history/search')) {
                    return new Response(JSON.stringify({
                        status: 'partial',
                        total: 1,
                        results: [{
                            session_id: 'test-agent-sess-1',
                            type: 'agent',
                            last_timestamp: '2026-09-30T12:00:00Z',
                            hits: [{
                                role: 'user',
                                timestamp: '2026-09-30T12:00:00Z',
                                excerpt: 'reproduce search highlight in browser',
                                match_offsets: [[10, 16]],
                                locator: { session_id: 'test-agent-sess-1', message_index: 85 }
                            }]
                        }]
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }
                return origFetch(url);
            };

            await searchChats('search');
            window.fetch = origFetch;

            const container = document.getElementById('chat-search-results');
            const statusNote = container.querySelector('.chat-search-status');
            return {
                hitCount: container.querySelectorAll('.chat-search-hit').length,
                statusNote: statusNote ? statusNote.textContent : null
            };
        })()`);

        console.log('Search result with status="partial":', searchPartialResult);
        assert.equal(searchPartialResult.hitCount, 1, 'Rendered hit card for status="partial"');
        assert.equal(searchPartialResult.statusNote, 'Some chat sources are unavailable.');

        // 4. Exercise searchChats with status: "unavailable"
        const searchUnavailableResult = await ev(`(async () => {
            const { searchChats } = await import('/static/chat_search.js');
            const origFetch = window.fetch;
            window.fetch = async (url) => {
                if (url.includes('/api/sessions/history/search')) {
                    return new Response(JSON.stringify({
                        status: 'unavailable',
                        results: []
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }
                return origFetch(url);
            };

            await searchChats('broken');
            window.fetch = origFetch;

            const container = document.getElementById('chat-search-results');
            return { text: container.textContent };
        })()`);

        console.log('Search result with status="unavailable":', searchUnavailableResult);
        assert.equal(searchUnavailableResult.text, 'Search is temporarily unavailable.');

        // 5. REAL AGENT HISTORY NAVIGATION & REPEATED TEXT DISAMBIGUATION
        // Generates 100 turns in an agent transcript.
        // Turn 5 and Turn 85 share identical prefix text ("Repeated needle action").
        // Search hit specifically targets Turn 85.
        // Real selectHistorySession executes, renders full history, locates index 85,
        // and adds .chat-search-target + focuses the element.
        console.log('Testing real selectHistorySession navigation with repeated text...');
        const agentLandingResult = await ev(`(async () => {
            const { searchChats } = await import('/static/chat_search.js');

            // Generate 100 turns
            const transcript = [];
            for (let i = 0; i < 100; i++) {
                if (i === 5) {
                    transcript.push({ type: 'user', content: 'Repeated needle action at early turn 5' });
                } else if (i === 85) {
                    transcript.push({ type: 'assistant', content: 'Repeated needle action at deep target turn 85' });
                } else {
                    transcript.push({ type: i % 2 === 0 ? 'user' : 'assistant', content: 'Routine message turn ' + i });
                }
            }

            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (u.includes('/api/sessions/history/search')) {
                    return new Response(JSON.stringify({
                        status: 'complete',
                        total: 1,
                        results: [{
                            session_id: 'sid-agent-deep',
                            type: 'agent',
                            hits: [
                                {
                                    role: 'user',
                                    excerpt: 'Repeated needle action at early turn 5',
                                    match_offsets: [[0, 22]],
                                    locator: { session_id: 'sid-agent-deep', message_index: 5 }
                                },
                                {
                                    role: 'assistant',
                                    excerpt: 'Repeated needle action at deep target turn 85',
                                    match_offsets: [[0, 22]],
                                    locator: { session_id: 'sid-agent-deep', message_index: 85 }
                                }
                            ]
                        }]
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                if (u.includes('/api/sessions/history/sid-agent-deep')) {
                    // History detail endpoint returns full transcript (total absent in response)
                    return new Response(JSON.stringify({
                        session_id: 'sid-agent-deep',
                        agent_type: 'claude',
                        messages: transcript
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                // Default fallbacks for auxiliary tabs
                if (u.includes('/notes') || u.includes('/tags') || u.includes('/commits') ||
                    u.includes('/events') || u.includes('/tasks') || u.includes('/changes') || u.includes('/tokens')) {
                    return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                return origFetch(url, opts);
            };

            // Search for needle
            await searchChats('Repeated needle');

            // Find the second hit (message_index: 85)
            const hits = document.querySelectorAll('.chat-search-hit');
            if (hits.length < 2) throw new Error('Expected 2 hits for repeated needle search, found ' + hits.length);

            // Click the deep hit (index 85) using the REAL click handler
            hits[1].click();

            // Wait for selectHistorySession and requestAnimationFrame to settle
            await new Promise(r => setTimeout(r, 200));

            window.fetch = origFetch;

            const target85 = document.querySelector('#history-messages [data-message-index="85"]');
            const early5 = document.querySelector('#history-messages [data-message-index="5"]');
            const active = document.activeElement;

            return {
                historyViewShown: document.getElementById('history-session-view').style.display !== 'none',
                target85Found: !!target85,
                target85HasTargetClass: target85 ? target85.classList.contains('chat-search-target') : false,
                target85Text: target85 ? target85.textContent : '',
                early5HasTargetClass: early5 ? early5.classList.contains('chat-search-target') : false,
                activeElementIndex: active ? active.dataset.messageIndex : null
            };
        })()`);

        console.log('Agent deep navigation landing result:', agentLandingResult);
        assert.ok(agentLandingResult.historyViewShown, 'History session view must be shown');
        assert.ok(agentLandingResult.target85Found, 'Message element at index 85 must be rendered in DOM');
        assert.ok(agentLandingResult.target85HasTargetClass, 'Target message 85 must have .chat-search-target class');
        assert.ok(agentLandingResult.target85Text.includes('deep target turn 85'), 'Target message 85 text must match Turn 85');
        assert.equal(agentLandingResult.early5HasTargetClass, false, 'Turn 5 must NOT have target class (repeated text correctly disambiguated by message_index)');
        assert.equal(agentLandingResult.activeElementIndex, '85', 'Active focused element must be Turn 85');

        // 6. REAL GROUP BOARD NAVIGATION & OLDER PAGE LOADING
        // Board has 70 total messages. Initial page returns newest 50 (offset 20).
        // Target search hit is message ID 105 (in the older page 0..20).
        // Real selectBoardProject executes, notices message 105 is absent,
        // calls loadEarlierMessages(), renders older messages, adds .chat-search-target,
        // and scrolls message 105 into view.
        console.log('Testing real selectBoardProject navigation with older page loading...');
        const boardLandingResult = await ev(`(async () => {
            const { searchChats } = await import('/static/chat_search.js');

            // 70 board messages (IDs 101 to 170)
            const allBoardMsgs = [];
            for (let i = 101; i <= 170; i++) {
                allBoardMsgs.push({
                    id: i,
                    project: 'core-infra',
                    agent: 'infra-bot',
                    color: '#4caf50',
                    content: i === 105 ? 'CRITICAL: Database migration failure post-mortem 105' : 'Routine board message ' + i,
                    created_at: '2026-09-30T10:00:00Z'
                });
            }

            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (u.includes('/api/sessions/history/search')) {
                    return new Response(JSON.stringify({
                        status: 'complete',
                        total: 1,
                        results: [{
                            session_id: 'board:core-infra',
                            project: 'core-infra',
                            type: 'group',
                            hits: [{
                                role: 'board',
                                excerpt: 'CRITICAL: Database migration failure post-mortem 105',
                                match_offsets: [[10, 28]],
                                locator: { project: 'core-infra', message_id: 105 }
                            }]
                        }]
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                if (u.includes('/core-infra/messages')) {
                    const parsedUrl = new URL(u, window.location.origin);
                    const limit = parseInt(parsedUrl.searchParams.get('limit') || '50', 10);
                    const offset = parseInt(parsedUrl.searchParams.get('offset') || '0', 10);

                    // Count check (limit=1, offset=0)
                    if (limit === 1 && offset === 0) {
                        return new Response(JSON.stringify({ messages: allBoardMsgs.slice(-1), total: allBoardMsgs.length }), {
                            status: 200, headers: { 'Content-Type': 'application/json' }
                        });
                    }

                    // Page slice
                    const sliced = allBoardMsgs.slice(offset, offset + limit);
                    return new Response(JSON.stringify({ messages: sliced, total: allBoardMsgs.length }), {
                        status: 200, headers: { 'Content-Type': 'application/json' }
                    });
                }

                if (u.includes('/subscribers') || u.includes('/pause') || u.includes('/sleep') || u.includes('/projects')) {
                    return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                return origFetch(url, opts);
            };

            await searchChats('migration');

            const boardHit = document.querySelector('.chat-search-hit');
            if (!boardHit) throw new Error('Expected board hit button in DOM');

            // Click the board hit using REAL handler
            boardHit.click();

            // Wait for selectBoardProject, initial page fetch, automatic loadEarlierMessages(), and render settling
            await new Promise(r => setTimeout(r, 300));

            window.fetch = origFetch;

            const target105 = document.querySelector('#mb-messages [data-message-id="105"]');

            return {
                boardViewShown: document.getElementById('mb-board').style.display !== 'none',
                target105Found: !!target105,
                target105HasTargetClass: target105 ? target105.classList.contains('chat-search-target') : false,
                target105Text: target105 ? target105.textContent : ''
            };
        })()`);

        console.log('Board older-page navigation landing result:', boardLandingResult);
        assert.ok(boardLandingResult.boardViewShown, 'Message board view must be shown');
        assert.ok(boardLandingResult.target105Found, 'Older message 105 must be loaded and rendered in DOM');
        assert.ok(boardLandingResult.target105HasTargetClass, 'Target message 105 must receive .chat-search-target class');
        assert.ok(boardLandingResult.target105Text.includes('post-mortem 105'), 'Target message 105 text must match');

        console.log('ALL REAL LANDING AND NAVIGATION ASSERTIONS PASSED');
    } finally {
        await client.close();
    }
})();
