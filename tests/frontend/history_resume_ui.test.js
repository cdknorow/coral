const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server; refusing production port');

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

        // Initialize test environment on the page
        await ev(`(async () => {
            const { state } = await import('/static/state.js');
            const modals = await import('/static/modals.js');
            const sessions = await import('/static/sessions.js');
            window.testState = state;
            window.testModals = modals;
            window.testSessions = sessions;

            // Session fixtures
            window.__codexMarker = '14c7245b-9fb4-67de-871c-a03f6186ed30';
            window.__codexNativeID = 'rollout-2026-09-30T12-00-00-native-codex-xyz';
            window.__agyMarker = '24c7245b-9fb4-67de-871c-a03f6186ed30';
            window.__agyNativeID = 'c1d2e3f4-native-conv-uuid-5678';
            window.__claudeID = '34c7245b-9fb4-67de-871c-a03f6186ed30';

            state.historySessionsList = [
                {
                    session_id: window.__codexMarker,
                    source_type: 'codex',
                    display_name: 'Historical Codex Agent',
                    summary: 'Fix history resume bug',
                },
                {
                    session_id: window.__agyMarker,
                    source_type: 'agy',
                    display_name: 'Historical Antigravity Agent',
                    summary: 'Antigravity session audit',
                },
                {
                    session_id: window.__claudeID,
                    source_type: 'claude',
                    display_name: 'Historical Claude Agent',
                    summary: 'Claude original workflow',
                },
            ];

            window.__launchRequests = [];

            // Intercept fetch: provide realistic mock responses for history endpoints
            // and capture launch payloads without spawning live CLI processes.
            const origFetch = window.fetch;
            window.fetch = async (url, opts = {}) => {
                const u = String(url);
                const method = (opts.method || 'GET').toUpperCase();
                const body = opts.body ? JSON.parse(opts.body) : null;

                const path = new URL(u, window.location.origin).pathname;

                if (path === '/api/sessions/launch' && method === 'POST') {
                    window.__launchRequests.push({ url: u, method, body });
                    return new Response(JSON.stringify({ ok: true, session_id: 'new-live-agent' }), {
                        status: 200,
                        headers: { 'Content-Type': 'application/json' },
                    });
                }

                if (path === '/api/sessions/history/' + window.__codexMarker + '/resume-info') {
                    return new Response(JSON.stringify({
                        session_id: window.__codexMarker,
                        agent_type: 'codex',
                        resume_session_id: window.__codexNativeID,
                        working_dir: '/repo/codex-project',
                        display_name: 'Historical Codex Agent',
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                if (path === '/api/sessions/history/' + window.__codexMarker) {
                    return new Response(JSON.stringify({
                        session_id: window.__codexMarker,
                        agent_type: 'codex',
                        messages: [
                            { type: 'user', content: 'Reproduce history resume bug' },
                            { type: 'assistant', text: 'Rollout identity resolved.' },
                        ],
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                if (path === '/api/sessions/history/' + window.__agyMarker + '/resume-info') {
                    return new Response(JSON.stringify({
                        session_id: window.__agyMarker,
                        agent_type: 'agy',
                        resume_session_id: window.__agyNativeID,
                        working_dir: '/repo/agy-project',
                        display_name: 'Historical Antigravity Agent',
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                if (path === '/api/sessions/history/' + window.__agyMarker) {
                    return new Response(JSON.stringify({
                        session_id: window.__agyMarker,
                        agent_type: 'agy',
                        messages: [
                            { type: 'user', content: 'Audit Antigravity transcript' },
                            { type: 'assistant', text: 'Conversation directory mapped.' },
                        ],
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                if (path === '/api/sessions/history/' + window.__claudeID + '/resume-info') {
                    return new Response(JSON.stringify({
                        session_id: window.__claudeID,
                        agent_type: 'claude',
                        resume_session_id: window.__claudeID,
                        working_dir: '/repo/claude-project',
                        display_name: 'Historical Claude Agent',
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                if (path === '/api/sessions/history/' + window.__claudeID) {
                    return new Response(JSON.stringify({
                        session_id: window.__claudeID,
                        agent_type: 'claude',
                        messages: [
                            { type: 'user', content: 'Claude unchanged check' },
                            { type: 'assistant', text: 'Claude identity unchanged.' },
                        ],
                    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }

                return origFetch(url, opts);
            };
        })()`);

        // ---------------------------------------------------------------------
        // Test 1: Codex Historical Session
        // ---------------------------------------------------------------------
        console.log('Testing Codex Historical Session...');
        await ev(`(async () => {
            await window.testSessions.selectHistorySession(window.__codexMarker);
        })()`);

        // Verify history messages rendered in the DOM
        const codexDomState = await ev(`({
            currentSession: window.testState.currentSession,
            historyText: document.getElementById('history-messages').innerText,
            resumeBtnVisible: document.getElementById('btn-resume-session').style.display !== 'none',
        })`);

        assert.equal(codexDomState.currentSession.type, 'history');
        assert.equal(codexDomState.currentSession.name, '14c7245b-9fb4-67de-871c-a03f6186ed30');
        assert.match(codexDomState.historyText, /Reproduce history resume bug/);
        assert.match(codexDomState.historyText, /Rollout identity resolved/);
        assert.equal(codexDomState.resumeBtnVisible, true, 'Resume button should be visible for codex');

        // Open Resume Modal for Codex
        await ev(`window.testModals.showResumeModal()`);
        await ev(`new Promise(r => setTimeout(r, 50))`);

        const codexModalData = await ev(`({
            modalDisplay: document.getElementById('resume-modal').style.display,
            agentType: document.getElementById('resume-modal').dataset.agentType,
            resumeSessionId: document.getElementById('resume-modal').dataset.resumeSessionId,
            displayName: document.getElementById('resume-modal').dataset.displayName,
            dirValue: document.getElementById('resume-dir').value,
            currentSessionName: window.testState.currentSession.name,
        })`);

        assert.equal(codexModalData.modalDisplay, 'flex');
        assert.equal(codexModalData.agentType, 'codex');
        assert.equal(codexModalData.resumeSessionId, 'rollout-2026-09-30T12-00-00-native-codex-xyz');
        assert.equal(codexModalData.displayName, 'Historical Codex Agent');
        assert.equal(codexModalData.dirValue, '/repo/codex-project');
        // Crucial requirement: Coral history session ID preserved in state.currentSession.name
        assert.equal(codexModalData.currentSessionName, '14c7245b-9fb4-67de-871c-a03f6186ed30');

        fs.writeFileSync('/tmp/history-resume-modal-codex.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));

        // Click Resume launch
        await ev(`window.testModals.resumeLaunchNew()`);
        await ev(`new Promise(r => setTimeout(r, 50))`);

        const codexLaunch = await ev(`window.__launchRequests[window.__launchRequests.length - 1]`);
        assert.ok(codexLaunch, 'Launch request must be sent');
        assert.equal(codexLaunch.body.agent_type, 'codex');
        // Crucial requirement: payload receives the provider-native rollout ID
        assert.equal(codexLaunch.body.resume_session_id, 'rollout-2026-09-30T12-00-00-native-codex-xyz');
        assert.equal(codexLaunch.body.working_dir, '/repo/codex-project');
        assert.equal(codexLaunch.body.display_name, 'Historical Codex Agent');
        assert.equal(await ev(`window.testState.currentSession.name`), '14c7245b-9fb4-67de-871c-a03f6186ed30');

        fs.writeFileSync('/tmp/history-resume-codex.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));

        // ---------------------------------------------------------------------
        // Test 2: Antigravity Historical Session
        // ---------------------------------------------------------------------
        console.log('Testing Antigravity Historical Session...');
        await ev(`(async () => {
            await window.testSessions.selectHistorySession(window.__agyMarker);
        })()`);

        const agyDomState = await ev(`({
            currentSession: window.testState.currentSession,
            historyText: document.getElementById('history-messages').innerText,
        })`);

        assert.equal(agyDomState.currentSession.name, '24c7245b-9fb4-67de-871c-a03f6186ed30');
        assert.match(agyDomState.historyText, /Audit Antigravity transcript/);
        assert.match(agyDomState.historyText, /Conversation directory mapped/);

        // Open Resume Modal for Antigravity
        await ev(`window.testModals.showResumeModal()`);
        await ev(`new Promise(r => setTimeout(r, 50))`);

        const agyModalData = await ev(`({
            modalDisplay: document.getElementById('resume-modal').style.display,
            agentType: document.getElementById('resume-modal').dataset.agentType,
            resumeSessionId: document.getElementById('resume-modal').dataset.resumeSessionId,
            dirValue: document.getElementById('resume-dir').value,
            currentSessionName: window.testState.currentSession.name,
        })`);

        assert.equal(agyModalData.modalDisplay, 'flex');
        assert.equal(agyModalData.agentType, 'agy');
        assert.equal(agyModalData.resumeSessionId, 'c1d2e3f4-native-conv-uuid-5678');
        assert.equal(agyModalData.dirValue, '/repo/agy-project');
        assert.equal(agyModalData.currentSessionName, '24c7245b-9fb4-67de-871c-a03f6186ed30');

        // Click Resume launch
        await ev(`window.testModals.resumeLaunchNew()`);
        await ev(`new Promise(r => setTimeout(r, 50))`);

        const agyLaunch = await ev(`window.__launchRequests[window.__launchRequests.length - 1]`);
        assert.ok(agyLaunch, 'Agy launch request must be sent');
        assert.equal(agyLaunch.body.agent_type, 'agy');
        assert.equal(agyLaunch.body.resume_session_id, 'c1d2e3f4-native-conv-uuid-5678');
        assert.equal(agyLaunch.body.working_dir, '/repo/agy-project');

        fs.writeFileSync('/tmp/history-resume-agy.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));

        // ---------------------------------------------------------------------
        // Test 3: Claude Historical Session (Unchanged behavior)
        // ---------------------------------------------------------------------
        console.log('Testing Claude Historical Session (Unchanged Behavior)...');
        await ev(`(async () => {
            await window.testSessions.selectHistorySession(window.__claudeID);
        })()`);

        const claudeDomState = await ev(`({
            currentSession: window.testState.currentSession,
            historyText: document.getElementById('history-messages').innerText,
        })`);

        assert.equal(claudeDomState.currentSession.name, '34c7245b-9fb4-67de-871c-a03f6186ed30');
        assert.match(claudeDomState.historyText, /Claude unchanged check/);

        // Open Resume Modal for Claude
        await ev(`window.testModals.showResumeModal()`);
        await ev(`new Promise(r => setTimeout(r, 50))`);

        const claudeModalData = await ev(`({
            agentType: document.getElementById('resume-modal').dataset.agentType,
            resumeSessionId: document.getElementById('resume-modal').dataset.resumeSessionId,
            currentSessionName: window.testState.currentSession.name,
        })`);

        assert.equal(claudeModalData.agentType, 'claude');
        // Unchanged: Claude uses the session ID directly as native resume ID
        assert.equal(claudeModalData.resumeSessionId, '34c7245b-9fb4-67de-871c-a03f6186ed30');
        assert.equal(claudeModalData.currentSessionName, '34c7245b-9fb4-67de-871c-a03f6186ed30');

        await ev(`window.testModals.resumeLaunchNew()`);
        await ev(`new Promise(r => setTimeout(r, 50))`);

        const claudeLaunch = await ev(`window.__launchRequests[window.__launchRequests.length - 1]`);
        assert.equal(claudeLaunch.body.agent_type, 'claude');
        assert.equal(claudeLaunch.body.resume_session_id, '34c7245b-9fb4-67de-871c-a03f6186ed30');

        console.log(JSON.stringify({
            historyResumeVerified: true,
            codexRolloutMapped: true,
            antigravityConvMapped: true,
            claudeUnchanged: true,
            stateCurrentSessionPreserved: true,
            launchPayloadValidated: true,
        }));
    } finally {
        await client.close();
    }
})();
