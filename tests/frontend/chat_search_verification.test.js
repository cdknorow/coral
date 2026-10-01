import assert from 'node:assert/strict';

// Independent Frontend Verification for Task #1801
// Tests chat search rendering, escaping, UTF-16 highlights, generation drops,
// locators, deep navigation, and documents the status mismatch defect.

// Mock DOM environment
globalThis.window = globalThis;
globalThis.location = { search: '' };
globalThis.localStorage = { getItem: () => null, setItem: () => {} };

const container = {
    hidden: false,
    textContent: '',
    children: [],
    replaceChildren(...args) {
        this.children = [...args];
        this.textContent = args.map(a => a.textContent || '').join('');
    },
    appendChild(child) {
        this.children = this.children || [];
        this.children.push(child);
    }
};

const list = { hidden: false };

globalThis.document = {
    getElementById(id) {
        if (id === 'chat-search-results') return container;
        if (id === 'history-sessions-list') return list;
        return null;
    },
    createElement(tag) {
        const el = {
            tagName: tag,
            className: '',
            _textContent: '',
            get textContent() { return this._textContent; },
            set textContent(v) { this._textContent = String(v); },
            get innerHTML() {
                if (this._innerHTML != null) return this._innerHTML;
                return (this._textContent || '')
                    .replace(/&/g, '&amp;')
                    .replace(/</g, '&lt;')
                    .replace(/>/g, '&gt;')
                    .replace(/"/g, '&quot;')
                    .replace(/'/g, '&#39;');
            },
            set innerHTML(v) { this._innerHTML = v; },
            attributes: {},
            dataset: {},
            children: [],
            listeners: {},
            setAttribute(name, val) { this.attributes[name] = String(val); },
            getAttribute(name) { return this.attributes[name]; },
            addEventListener(type, fn) {
                this.listeners[type] = this.listeners[type] || [];
                this.listeners[type].push(fn);
            },
            dispatchEvent(event) {
                const handlers = this.listeners[event.type] || [];
                for (const h of handlers) h(event);
            },
            click() {
                this.dispatchEvent({ type: 'click' });
            },
            appendChild(child) {
                this.children.push(child);
            },
            replaceChildren(...args) {
                this.children = [...args];
            }
        };
        return el;
    }
};

const { highlightExcerpt, searchChats } = await import('../../coral-go/internal/server/frontend/static/chat_search.js');

console.log('--- Beginning Chat Search Frontend Independent Verification ---');

// 1. Safe Highlight & UTF-16 Offsets
{
    console.log('[TEST 1] Safe Highlight & UTF-16 Offsets');
    const dangerousExcerpt = '<script>alert("xss")</script> and normal text';
    // Match "script" [1, 7] and "normal" [34, 40]
    const html = highlightExcerpt(dangerousExcerpt, [[1, 7], [34, 40]]);
    assert.ok(html.includes('&lt;<mark>script</mark>&gt;'), 'Must safely escape tags while highlighting inside');
    assert.ok(!html.includes('<script>'), 'Must NOT emit raw <script> tags');
    assert.ok(html.includes('<mark>normal</mark>'), 'Must highlight second match');

    // Emoji and surrogate pairs: "🚀 rocket"
    // 🚀 is 2 UTF-16 code units. "rocket" is from offset 3 to 9.
    const emojiExcerpt = '🚀 rocket';
    const emojiHtml = highlightExcerpt(emojiExcerpt, [[3, 9]]);
    assert.ok(emojiHtml.includes('🚀 <mark>rocket</mark>'), 'Emoji surrogate pair preserved without offset shift');
    console.log('  PASS: Safe highlight and UTF-16 offsets verified');
}

// 2. Query Generation Counter / Late Response Drops
{
    console.log('[TEST 2] Race Condition & Late Response Dropping');
    let resolveFirst;
    const firstPromise = new Promise(r => { resolveFirst = r; });

    let fetchCount = 0;
    globalThis.fetch = async (url) => {
        fetchCount++;
        if (url.includes('q=first')) {
            await firstPromise;
            return {
                ok: true,
                json: async () => ({ status: 'partial', results: [{ session_id: 'sid-slow', hits: [] }] })
            };
        }
        if (url.includes('q=second')) {
            return {
                ok: true,
                json: async () => ({ status: 'partial', results: [{ session_id: 'sid-fast', hits: [] }] })
            };
        }
        return { ok: false, status: 500 };
    };

    // Issue first query (slow)
    const p1 = searchChats('first');
    // Rapidly issue second query (fast)
    const p2 = searchChats('second');

    await p2;
    // container should now show second results
    const resultsAfterFast = container.children.filter(c => c.className === 'chat-search-result');
    assert.equal(resultsAfterFast.length, 1);
    assert.ok(resultsAfterFast[0].innerHTML.includes('sid-fast'), 'Second fast response displayed');

    // Now resolve the first slow query
    resolveFirst();
    await p1;

    // container should STILL show second results, NOT overwritten by first
    const resultsAfterLate = container.children.filter(c => c.className === 'chat-search-result');
    assert.equal(resultsAfterLate.length, 1);
    assert.ok(resultsAfterLate[0].innerHTML.includes('sid-fast'), 'Late first response was dropped by generation check');
    console.log('  PASS: Generation check successfully drops stale late responses');
}

// 3. Navigation Delegation & Locators
{
    console.log('[TEST 3] Locator Dispatch for Agent Sessions and Group Chats');
    let agentNav = null;
    let boardNav = null;
    window.selectHistorySession = async (sid, loc) => { agentNav = { sid, loc }; };
    window.selectBoardProject = (proj, msgId) => { boardNav = { proj, msgId }; };

    globalThis.fetch = async () => ({
        ok: true,
        json: async () => ({
            status: 'partial', // using partial so bug in line 90 does not block rendering here
            results: [
                {
                    session_id: 'sid-agent-42',
                    type: 'agent',
                    hits: [
                        {
                            role: 'user',
                            timestamp: '2026-09-30T10:00:00Z',
                            excerpt: 'inspect agent locator',
                            match_offsets: [[8, 13]],
                            locator: { session_id: 'sid-agent-42', message_index: 85 }
                        }
                    ]
                },
                {
                    session_id: 'board:platform',
                    project: 'platform',
                    type: 'group',
                    hits: [
                        {
                            role: 'board',
                            timestamp: '2026-09-30T11:00:00Z',
                            excerpt: 'board message locator',
                            match_offsets: [[0, 5]],
                            locator: { project: 'platform', message_id: 1055 }
                        }
                    ]
                }
            ]
        })
    });

    await searchChats('locator');

    const resultCards = container.children.filter(c => c.className === 'chat-search-result');
    assert.equal(resultCards.length, 2, 'Rendered 2 result cards');

    // Click agent hit button
    const agentHitBtn = resultCards[0].children.find(c => c.className === 'chat-search-hit');
    assert.ok(agentHitBtn, 'Found agent hit button');
    agentHitBtn.click();
    assert.deepEqual(agentNav, {
        sid: 'sid-agent-42',
        loc: { session_id: 'sid-agent-42', message_index: 85 }
    }, 'Agent hit click correctly delegates to selectHistorySession with exact message_index locator');

    // Click group hit button
    const boardHitBtn = resultCards[1].children.find(c => c.className === 'chat-search-hit');
    assert.ok(boardHitBtn, 'Found board hit button');
    boardHitBtn.click();
    assert.deepEqual(boardNav, {
        proj: 'platform',
        msgId: 1055
    }, 'Group board hit click correctly delegates to selectBoardProject with exact message_id locator');

    console.log('  PASS: Locators correctly routed to selectHistorySession and selectBoardProject');
}

// 4. Verification of Repaired Status Contract (complete, partial, unavailable, ok)
{
    console.log('[TEST 4] Status Contract Verification (complete, partial, unavailable, ok)');

    // 4A: status="complete" -> renders results without error or partial note
    globalThis.fetch = async () => ({
        ok: true,
        json: async () => ({
            status: 'complete',
            total: 1,
            results: [
                {
                    session_id: 'sid-complete',
                    type: 'agent',
                    hits: [{ role: 'user', excerpt: 'complete status search', match_offsets: [[0, 8]], locator: { session_id: 'sid-complete', message_index: 10 } }]
                }
            ]
        })
    });
    await searchChats('complete');
    assert.equal(container.textContent, '', 'No error text on complete');
    const completeHits = container.children.filter(c => c.className === 'chat-search-result');
    assert.equal(completeHits.length, 1, 'Rendered 1 result on complete');
    console.log('  PASS: status="complete" successfully renders search results');

    // 4B: status="partial" -> renders results AND partial note
    globalThis.fetch = async () => ({
        ok: true,
        json: async () => ({
            status: 'partial',
            total: 1,
            results: [
                {
                    session_id: 'sid-partial',
                    type: 'agent',
                    hits: [{ role: 'user', excerpt: 'partial status search', match_offsets: [[0, 7]], locator: { session_id: 'sid-partial', message_index: 2 } }]
                }
            ]
        })
    });
    await searchChats('partial');
    const partialBanner = container.children.find(c => c.className === 'chat-search-status');
    assert.ok(partialBanner, 'Partial note must be present');
    assert.equal(partialBanner.textContent, 'Some chat sources are unavailable.');
    const partialHits = container.children.filter(c => c.className === 'chat-search-result');
    assert.equal(partialHits.length, 1, 'Rendered 1 result on partial');
    console.log('  PASS: status="partial" renders partial note and search results');

    // 4C: status="unavailable" -> displays 'Search is temporarily unavailable.'
    globalThis.fetch = async () => ({
        ok: true,
        json: async () => ({
            status: 'unavailable',
            results: []
        })
    });
    await searchChats('unavailable');
    assert.equal(container.textContent, 'Search is temporarily unavailable.');
    console.log('  PASS: status="unavailable" displays temporary unavailability message');

    // 4D: legacy status="ok" -> rejected as invalid backend contract
    globalThis.fetch = async () => ({
        ok: true,
        json: async () => ({
            status: 'ok',
            results: []
        })
    });
    await searchChats('ok');
    assert.equal(container.textContent, 'Search is temporarily unavailable.');
    console.log('  PASS: legacy status="ok" rejected as invalid contract');
}

console.log('--- All Independent Frontend Verification Tests Executed ---');
