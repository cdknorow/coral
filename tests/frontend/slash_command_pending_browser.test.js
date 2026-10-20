// A slash command sent from the chat ("/model sonnet") that Claude Code ran by itself is not left pending (#5488): the transcript records it as a system entry, not a user turn, so the
// message waited in the chat queue for ever. The reader now gives it as a user turn flagged local_command with the text as typed; the chat shows a small note and the pending bubble goes.
// A message that was not consumed stays pending. Run against an isolated dev server.
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const check = (label, ok, detail) => { console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); if (!ok) process.exitCode = 1; };
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired();
        const S = `(window.__coralStatic || '/static/')`;
        await waitFor(`!!window.__coralReady`, 'the page');
        await ev(`(async () => { const pm = await import(${S} + 'pending_messages.js'); const lc = await import(${S} + 'live_chat.js');
            window.__pm = pm; window.__lc = lc; const box = document.createElement('div'); box.id = 'sc-box'; box.className = 'chat-messages chat-prose'; document.body.appendChild(box); return true; })()`);
        const pendingCount = () => ev(`(() => { const b = document.getElementById('sc-box'); window.__pm.syncPendingBubbles(b, 'sc-1'); return b.querySelectorAll('[class*="pending"] .pending-message, .pending-bubble, .chat-bubble.pending').length || b.querySelectorAll('[class*="pending-"]').length; })()`);
        const at = new Date().toISOString();
        // two messages pending: the command, and a question the agent has not taken yet
        await ev(`window.__pm.addPendingMessage('sc-1', '/model claude-sonnet-4-5', Date.now() - 1000); window.__pm.addPendingMessage('sc-1', 'and what does this mean?', Date.now() - 900); true`);
        const before = await pendingCount();
        check(`both messages are pending (${before})`, before >= 1);
        const text = () => ev(`document.getElementById('sc-box').textContent.replace(/\\s+/g, ' ').trim()`);
        check('both are listed as pending', /\/model claude-sonnet-4-5/.test(await text()) && /what does this mean/.test(await text()), await text());
        // the transcript brings the command back, as the reader gives it
        await ev(`(() => { const b = document.getElementById('sc-box'); const msg = { type: 'user', content: '/model claude-sonnet-4-5', local_command: true, timestamp: ${JSON.stringify(at)} };
            window.__lc.renderTranscript([msg], b, 'claude'); window.__pm.rememberUserMessage('sc-1', msg); window.__pm.syncPendingBubbles(b, 'sc-1'); return true; })()`);
        const t = await text();
        const note = await ev(`[...document.querySelectorAll('#sc-box .chat-local-command')].map(e => e.textContent.trim())`);
        check(`the command is shown as a small note, not a bubble of the Operator's (${JSON.stringify(note)})`, note.length === 1 && note[0] === '/model claude-sonnet-4-5' && (await ev(`[...document.querySelectorAll('#sc-box .chat-bubble.human')].filter(b => /^\\s*You\\s*\\/model/.test(b.textContent)).length`)) === 0);
        const pend = await ev(`[...document.querySelectorAll('#sc-box [class*="pending"]')].map(e => e.textContent.trim().replace(/\\s+/g, ' '))`);
        check(`its pending bubble is gone, the other message is still pending (${JSON.stringify(pend)})`, !pend.some(p => /\/model claude-sonnet-4-5/.test(p)) && pend.some(p => /what does this mean/.test(p)), t);
        // a different command's note does not clear it
        await ev(`(() => { const msg = { type: 'user', content: '/effort high', local_command: true, timestamp: ${JSON.stringify(at)} }; window.__pm.rememberUserMessage('sc-1', msg); window.__pm.syncPendingBubbles(document.getElementById('sc-box'), 'sc-1'); return true; })()`);
        check('another command does not clear a message it is not', /what does this mean/.test(await ev(`document.getElementById('sc-box').textContent`)));
    } finally { try { await client.close(); } catch (_) {} }
})().catch(e => { console.error(e); process.exit(1); });
