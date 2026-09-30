const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Refusing to run tests on production port 8420');

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

        console.log('1. Setting up test harness and mocks...');
        await ev(`(async () => {
            const { state } = await import('/static/state.js');
            const liveChat = await import('/static/live_chat.js');
            const sessions = await import('/static/sessions.js');
            const { platform } = await import('/static/platform/detect.js');

            window.testState = state;
            window.testLiveChat = liveChat;
            window.testSessions = sessions;
            window.testPlatform = platform;

            // Mock live sessions in state
            state.liveSessions = [
                {
                    session_id: 'agent-alpha-id',
                    name: 'agent-alpha',
                    display_name: 'Agent Alpha',
                    agent_type: 'codex',
                    working_directory: '/tmp/alpha',
                },
                {
                    session_id: 'agent-beta-id',
                    name: 'agent-beta',
                    display_name: 'Agent Beta',
                    agent_type: 'agy',
                    working_directory: '/tmp/beta',
                },
                {
                    session_id: 'agent-gamma-id',
                    name: 'agent-gamma',
                    display_name: 'Agent Gamma',
                    agent_type: 'claude',
                    working_directory: '/tmp/gamma',
                },
            ];

            // Setup DOM containers if not present
            let container = document.getElementById('live-history-messages');
            if (!container) {
                container = document.createElement('div');
                container.id = 'live-history-messages';
                document.body.appendChild(container);
            }
        })()`);

        console.log('2. Test Case: Normal chat load success clears loading indicator...');
        await ev(`(async () => {
            const { state } = window.testState;
            const container = document.getElementById('live-history-messages');
            window.testLiveChat.resetLiveHistory();

            window.testState.currentSession = {
                type: 'live',
                name: 'agent-alpha',
                session_id: 'agent-alpha-id',
                agent_type: 'codex',
                working_directory: '/tmp/alpha',
            };

            // Intercept fetch for agent-alpha/chat to return mock messages
            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                if (url.includes('/api/sessions/live/agent-alpha/chat')) {
                    return {
                        ok: true,
                        status: 200,
                        json: async () => ({
                            messages: [
                                { type: 'user', content: 'Hello from Alpha' },
                                { type: 'assistant', text: 'Alpha response' },
                            ],
                            total: 2,
                            has_more: false,
                        }),
                    };
                }
                return origFetch(url, opts);
            };

            await window.testLiveChat.refreshLiveHistory();
            window.fetch = origFetch;
        })()`);

        const alphaResults = await ev(`(() => {
            const container = document.getElementById('live-history-messages');
            return {
                hasLoading: !!container.querySelector('.loading-indicator'),
                hasError: !!container.querySelector('.chat-load-error'),
                text: container.innerText,
                childCount: container.children.length,
            };
        })()`);

        assert.equal(alphaResults.hasLoading, false, 'Loading indicator should be removed upon success');
        assert.equal(alphaResults.hasError, false, 'No error state should be present');
        assert.ok(alphaResults.text.includes('Hello from Alpha'), 'User message should be rendered');
        assert.ok(alphaResults.text.includes('Alpha response'), 'Assistant response should be rendered');
        console.log('   ✓ Normal chat load success verified');

        console.log('3. Test Case: Failed chat request leaves coherent error state, NOT stuck loading indicator...');
        await ev(`(async () => {
            const container = document.getElementById('live-history-messages');
            window.testLiveChat.resetLiveHistory();

            window.testState.currentSession = {
                type: 'live',
                name: 'agent-beta',
                session_id: 'agent-beta-id',
                agent_type: 'agy',
                working_directory: '/tmp/beta',
            };

            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                if (url.includes('/api/sessions/live/agent-beta/chat')) {
                    return {
                        ok: false,
                        status: 500,
                        json: async () => ({ error: 'Transcript corrupted or backend lock timeout' }),
                    };
                }
                return origFetch(url, opts);
            };

            await window.testLiveChat.refreshLiveHistory();
            window.fetch = origFetch;
        })()`);

        const betaResults = await ev(`(() => {
            const container = document.getElementById('live-history-messages');
            return {
                hasLoading: !!container.querySelector('.loading-indicator'),
                errorEl: !!container.querySelector('.chat-load-error'),
                errorText: container.querySelector('.chat-error-message')?.innerText || '',
                retryBtn: !!container.querySelector('.chat-retry-btn'),
            };
        })()`);

        assert.equal(betaResults.hasLoading, false, 'Loading indicator MUST NOT remain stuck on fetch error');
        assert.equal(betaResults.errorEl, true, 'Error state must be rendered on failure');
        assert.ok(betaResults.errorText.includes('Transcript corrupted or backend lock timeout'), 'Specific error message should be displayed');
        assert.equal(betaResults.retryBtn, true, 'Retry button should be available');
        console.log('   ✓ Failed request settled error state and retry button verified');

        console.log('4. Test Case: Rapid switching with slow prior agent responses and AbortController cancellation...');
        await ev(`(async () => {
            const container = document.getElementById('live-history-messages');
            window.__fetchEvents = [];
            window.__abortedRequests = [];

            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                const signal = opts?.signal;
                if (url.includes('/api/sessions/live/')) {
                    const agentName = url.split('/live/')[1].split('/')[0];
                    const entry = { agent: agentName, startedAt: Date.now(), aborted: false };
                    window.__fetchEvents.push(entry);

                    if (signal) {
                        signal.addEventListener('abort', () => {
                            entry.aborted = true;
                            window.__abortedRequests.push(agentName);
                        });
                    }

                    if (agentName === 'agent-alpha') {
                        // Simulate SLOW prior agent (takes 400ms)
                        return new Promise((resolve, reject) => {
                            const timer = setTimeout(() => {
                                resolve({
                                    ok: true,
                                    status: 200,
                                    json: async () => ({
                                        messages: [{ type: 'assistant', text: 'STALE ALPHA MESSAGE' }],
                                        total: 1,
                                    }),
                                });
                            }, 400);
                            if (signal) {
                                signal.addEventListener('abort', () => {
                                    clearTimeout(timer);
                                    const err = new Error('The user aborted a request.');
                                    err.name = 'AbortError';
                                    reject(err);
                                });
                            }
                        });
                    }

                    if (agentName === 'agent-beta') {
                        // Simulate MEDIUM prior agent (takes 200ms)
                        return new Promise((resolve, reject) => {
                            const timer = setTimeout(() => {
                                resolve({
                                    ok: true,
                                    status: 200,
                                    json: async () => ({
                                        messages: [{ type: 'assistant', text: 'STALE BETA MESSAGE' }],
                                        total: 1,
                                    }),
                                });
                            }, 200);
                            if (signal) {
                                signal.addEventListener('abort', () => {
                                    clearTimeout(timer);
                                    const err = new Error('The user aborted a request.');
                                    err.name = 'AbortError';
                                    reject(err);
                                });
                            }
                        });
                    }

                    if (agentName === 'agent-gamma') {
                        // FAST destination agent (takes 20ms)
                        return new Promise((resolve) => {
                            setTimeout(() => {
                                resolve({
                                    ok: true,
                                    status: 200,
                                    json: async () => ({
                                        messages: [{ type: 'assistant', text: 'FRESH GAMMA MESSAGE' }],
                                        total: 1,
                                    }),
                                });
                            }, 20);
                        });
                    }
                }
                return origFetch(url, opts);
            };

            // Rapidly select Alpha -> Beta -> Gamma
            window.testSessions.selectLiveSession('agent-alpha', 'codex', 'agent-alpha-id');
            // Immediate rapid switch to Beta after 10ms
            await new Promise(r => setTimeout(r, 10));
            window.testSessions.selectLiveSession('agent-beta', 'agy', 'agent-beta-id');
            // Immediate rapid switch to Gamma after 10ms
            await new Promise(r => setTimeout(r, 10));
            window.testSessions.selectLiveSession('agent-gamma', 'claude', 'agent-gamma-id');

            // Wait for all promises/timers to settle
            await new Promise(r => setTimeout(r, 500));
            window.fetch = origFetch;
        })()`);

        const rapidSwitchResults = await ev(`(() => {
            const container = document.getElementById('live-history-messages');
            return {
                aborted: window.__abortedRequests,
                containerText: container.innerText,
                currentSessionId: window.testState.currentSession?.session_id,
            };
        })()`);

        assert.equal(rapidSwitchResults.currentSessionId, 'agent-gamma-id', 'Active session must be Agent Gamma');
        assert.ok(rapidSwitchResults.aborted.includes('agent-alpha'), 'Agent Alpha in-flight fetch must be cancelled via AbortController');
        assert.ok(rapidSwitchResults.aborted.includes('agent-beta'), 'Agent Beta in-flight fetch must be cancelled via AbortController');
        assert.ok(rapidSwitchResults.containerText.includes('FRESH GAMMA MESSAGE'), 'Container must render Gamma messages');
        assert.equal(rapidSwitchResults.containerText.includes('STALE ALPHA MESSAGE'), false, 'Obsolete Alpha response must never overwrite current session');
        assert.equal(rapidSwitchResults.containerText.includes('STALE BETA MESSAGE'), false, 'Obsolete Beta response must never overwrite current session');
        console.log('   ✓ Rapid switching with slow prior responses and AbortController cancellation verified');

        console.log('5. Test Case: Native WebKit environment with document.hidden and retry interaction...');
        await ev(`(async () => {
            window.testPlatform.isNative = true;
            const container = document.getElementById('live-history-messages');
            window.testLiveChat.resetLiveHistory();

            window.testState.currentSession = {
                type: 'live',
                name: 'agent-beta',
                session_id: 'agent-beta-id',
                agent_type: 'agy',
                working_directory: '/tmp/beta',
            };

            let attempts = 0;
            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                if (url.includes('/api/sessions/live/agent-beta/chat')) {
                    attempts++;
                    if (attempts === 1) {
                        return {
                            ok: false,
                            status: 502,
                            json: async () => ({ error: 'Bad Gateway' }),
                        };
                    } else {
                        return {
                            ok: true,
                            status: 200,
                            json: async () => ({
                                messages: [{ type: 'assistant', text: 'RECOVERED BETA MESSAGE' }],
                                total: 1,
                            }),
                        };
                    }
                }
                return origFetch(url, opts);
            };

            // First call fails
            await window.testLiveChat.refreshLiveHistory();
            const errorRendered = !!container.querySelector('.chat-load-error');
            const retryBtn = container.querySelector('.chat-retry-btn');

            if (!errorRendered || !retryBtn) {
                window.fetch = origFetch;
                throw new Error('Retry button not rendered');
            }

            // Click retry button to trigger recovery
            retryBtn.click();
            await new Promise(r => setTimeout(r, 100));

            window.fetch = origFetch;
        })()`);

        const nativeRetryResults = await ev(`(() => {
            const container = document.getElementById('live-history-messages');
            return {
                hasLoading: !!container.querySelector('.loading-indicator'),
                hasError: !!container.querySelector('.chat-load-error'),
                text: container.innerText,
            };
        })()`);

        assert.equal(nativeRetryResults.hasLoading, false, 'Loading indicator should be gone after retry');
        assert.equal(nativeRetryResults.hasError, false, 'Error indicator should be cleared after recovery');
        assert.ok(nativeRetryResults.text.includes('RECOVERED BETA MESSAGE'), 'Recovered messages should be rendered');
        console.log('   ✓ Native WebKit retry recovery verified');

        console.log('6. Test Case: Rapid same-session switching (A -> B -> A) with out-of-order resolution...');
        await ev(`(async () => {
            const container = document.getElementById('live-history-messages');
            window.testLiveChat.resetLiveHistory();
            window.__eventsABA = [];

            let alphaCallCount = 0;
            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                if (url.includes('/api/sessions/live/agent-alpha/chat')) {
                    alphaCallCount++;
                    const callId = alphaCallCount;
                    window.__eventsABA.push('alpha_call_' + callId);
                    if (callId === 1) {
                        // Slow initial Alpha call (resolves after 250ms)
                        return new Promise((resolve) => {
                            setTimeout(() => {
                                resolve({
                                    ok: true,
                                    status: 200,
                                    json: async () => ({
                                        messages: [{ type: 'assistant', text: 'OBSOLETE ALPHA 1 MESSAGE' }],
                                        total: 1,
                                    }),
                                });
                            }, 250);
                        });
                    } else {
                        // Fast subsequent Alpha call (resolves in 30ms)
                        return new Promise((resolve) => {
                            setTimeout(() => {
                                resolve({
                                    ok: true,
                                    status: 200,
                                    json: async () => ({
                                        messages: [{ type: 'assistant', text: 'FRESH ALPHA 2 MESSAGE' }],
                                        total: 1,
                                    }),
                                });
                            }, 30);
                        });
                    }
                }
                if (url.includes('/api/sessions/live/agent-beta/chat')) {
                    window.__eventsABA.push('beta_call');
                    return new Promise((resolve) => {
                        setTimeout(() => {
                            resolve({
                                ok: true,
                                status: 200,
                                json: async () => ({
                                    messages: [{ type: 'assistant', text: 'INTERMEDIATE BETA MESSAGE' }],
                                    total: 1,
                                }),
                            });
                        }, 50);
                    });
                }
                return origFetch(url, opts);
            };

            // Switch Alpha -> Beta -> Alpha rapidly
            window.testSessions.selectLiveSession('agent-alpha', 'codex', 'agent-alpha-id');
            await new Promise(r => setTimeout(r, 10));
            window.testSessions.selectLiveSession('agent-beta', 'agy', 'agent-beta-id');
            await new Promise(r => setTimeout(r, 10));
            window.testSessions.selectLiveSession('agent-alpha', 'codex', 'agent-alpha-id');

            // Wait 400ms for all delayed promises to settle
            await new Promise(r => setTimeout(r, 400));
            window.fetch = origFetch;
        })()`);

        const abaResults = await ev(`(() => {
            const container = document.getElementById('live-history-messages');
            return {
                text: container.innerText,
                hasLoading: !!container.querySelector('.loading-indicator'),
                currentSessionId: window.testState.currentSession?.session_id,
            };
        })()`);

        assert.equal(abaResults.currentSessionId, 'agent-alpha-id', 'Active session must be Agent Alpha');
        assert.equal(abaResults.hasLoading, false, 'Loading indicator must be settled');
        assert.ok(abaResults.text.includes('FRESH ALPHA 2 MESSAGE'), 'Container must render newest Alpha 2 response');
        assert.equal(abaResults.text.includes('OBSOLETE ALPHA 1 MESSAGE'), false, 'Obsolete Alpha 1 response must be ignored by generation guard');
        assert.equal(abaResults.text.includes('INTERMEDIATE BETA MESSAGE'), false, 'Intermediate Beta response must never leak into Alpha');
        console.log('   ✓ Rapid A -> B -> A switching with generation guard verified');

        console.log('7. Test Case: Load-more pagination cancellation on session switch...');
        await ev(`(async () => {
            const container = document.getElementById('live-history-messages');
            window.testLiveChat.resetLiveHistory();
            window.__loadMoreAborted = false;

            const origFetch = window.fetch;
            window.fetch = async (url, opts) => {
                const signal = opts?.signal;
                if (url.includes('/api/sessions/live/agent-alpha/chat')) {
                    if (url.includes('offset=0') || url.includes('after=')) {
                        // Initial chat load for Alpha
                        return {
                            ok: true,
                            status: 200,
                            json: async () => ({
                                messages: [{ type: 'assistant', text: 'ALPHA RECENT MESSAGE' }],
                                total: 10,
                                has_more: true,
                            }),
                        };
                    }
                    if (url.includes('offset=1')) {
                        // Slow load-more for older messages
                        return new Promise((resolve, reject) => {
                            const timer = setTimeout(() => {
                                resolve({
                                    ok: true,
                                    status: 200,
                                    json: async () => ({
                                        messages: [{ type: 'assistant', text: 'ALPHA OLDER MESSAGE' }],
                                        total: 10,
                                        has_more: false,
                                    }),
                                });
                            }, 300);
                            if (signal) {
                                signal.addEventListener('abort', () => {
                                    clearTimeout(timer);
                                    window.__loadMoreAborted = true;
                                    const err = new Error('The user aborted a request.');
                                    err.name = 'AbortError';
                                    reject(err);
                                });
                            }
                        });
                    }
                }
                if (url.includes('/api/sessions/live/agent-beta/chat')) {
                    return {
                        ok: true,
                        status: 200,
                        json: async () => ({
                            messages: [{ type: 'assistant', text: 'BETA INITIAL MESSAGE' }],
                            total: 1,
                            has_more: false,
                        }),
                    };
                }
                return origFetch(url, opts);
            };

            // 1. Load Alpha
            window.testSessions.selectLiveSession('agent-alpha', 'codex', 'agent-alpha-id');
            await new Promise(r => setTimeout(r, 50));

            // 2. Trigger load-more (which takes 300ms)
            window.testLiveChat.loadMoreHistory();
            await new Promise(r => setTimeout(r, 20));

            // 3. Switch to Beta while load-more is in flight
            window.testSessions.selectLiveSession('agent-beta', 'agy', 'agent-beta-id');
            await new Promise(r => setTimeout(r, 400));

            window.fetch = origFetch;
        })()`);

        const paginationResults = await ev(`(() => {
            const container = document.getElementById('live-history-messages');
            return {
                aborted: window.__loadMoreAborted,
                text: container.innerText,
                currentSessionId: window.testState.currentSession?.session_id,
            };
        })()`);

        assert.equal(paginationResults.currentSessionId, 'agent-beta-id', 'Active session must be Agent Beta');
        assert.equal(paginationResults.aborted, true, 'In-flight load-more request must be aborted on session switch');
        assert.ok(paginationResults.text.includes('BETA INITIAL MESSAGE'), 'Container must render Beta message');
        assert.equal(paginationResults.text.includes('ALPHA OLDER MESSAGE'), false, 'Older Alpha messages must never be injected into Beta session');
        console.log('   ✓ Load-more pagination cancellation on switch verified');

        console.log('\nAll live agent switching loading performance tests PASSED successfully!');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error('Test FAILED:', err);
    process.exit(1);
});
