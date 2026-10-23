// CSP stage 2, slice 5 (#3645): the live agent view and its command pane on delegated data-on-*
// actions, against a real terminal agent (launched, then killed). Each moved action still does what
// its inline handler did, seen in the request it sends or the view it changes:
//  - keys: Esc, ↑, ↓, Enter and Undo send exactly their keys; a key outside the allowlist sends nothing;
//  - Bash (!), a macro (run by its index in the saved list; a bad index sends nothing), the Mode
//    toggle (BTab), Send, and the send menu (it opens; Send sends and closes it);
//  - the Chat/Terminal toggle, the side panel's tabs (an unknown tab does nothing) and its toggle;
//  - Restart opens its confirmation (and the shared list marks it destructive for a popout);
//  - no inline handler is left in the live view or its command pane.
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
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    let agent = null;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__sent = [];
            const inner = window.fetch;
            window.fetch = (url, opts = {}) => {
                const u = String(url && url.url || url);
                const m = u.match(/\\/api\\/sessions\\/live\\/[^/]+\\/(keys|send)(\\?|$)/);
                if (m && (opts.method || 'GET').toUpperCase() === 'POST') { let b = {}; try { b = JSON.parse(opts.body || '{}'); } catch {} window.__sent.push({ kind: m[1], keys: b.keys, command: b.command }); }
                return inner(url, opts);
            };
            // Keys and input for an attached agent go over its terminal socket (fetch is the fallback).
            const wsSend = WebSocket.prototype.send;
            WebSocket.prototype.send = function (data) { if (typeof data === 'string') window.__sent.push({ kind: 'ws', data }); return wsSend.call(this, data); };
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        agent = await ev(`fetch('/api/sessions/launch', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ working_dir: ${JSON.stringify(require('node:os').tmpdir())}, agent_type: 'terminal', display_name: 'Live QA' }) }).then(r => r.json())`);
        await until(`import(${MOD} + 'state.js').then(({ state }) => (state.liveSessions || []).some(s => s.session_id === ${JSON.stringify(agent.session_id)}))`, 'the agent in the live list');
        await ev(`window.selectLiveSession(${JSON.stringify(agent.session_name)}, 'terminal', ${JSON.stringify(agent.session_id)}); true`);
        await until(`!!document.querySelector('#command-toolbar [data-on-click="cmd-raw-keys"]')`, 'the command toolbar');
        const sent = () => ev(`window.__sent.slice()`);
        const clear = () => ev(`window.__sent.length = 0; true`);
        const click = sel => ev(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (!el) return false; el.click(); return true; })()`);
        // Wait for input to reach the agent (a resize frame can come first; it isn't input).
        const settle = what => until(`window.__sent.some(s => s.kind !== 'ws' || /"type":"terminal_input"/.test(s.data))`, what, 4000);
        // What reached the agent: keys and commands sent by fetch, and text written to its terminal
        // socket (a JSON frame's string fields, or the raw frame).
        const SEQ = { Escape: '\x1b', Up: '\x1b[A', Down: '\x1b[B', Enter: '\r', BTab: '\x1b[Z' };
        const wsTexts = list => list.filter(s => s.kind === 'ws').flatMap(s => { try { const j = JSON.parse(s.data); return Object.values(j).filter(v => typeof v === 'string'); } catch { return [s.data]; } });
        const gotKeys = (list, keys) => list.some(s => s.kind === 'keys' && (s.keys || []).join(',') === keys.join(',')) || wsTexts(list).some(t => t === keys.map(k => SEQ[k]).join(''));
        // Input that reached the agent (a resize frame isn't input).
        const inputs = list => list.filter(s => s.kind !== 'ws' || /"type":"terminal_input"/.test(s.data));
        // Wait until nothing more is sent (a send types its Enter a moment after its text), so a
        // "sends nothing" check starts clean.
        const quiet = async () => { let n = -1, still = 0; for (let i = 0; i < 40 && still < 6; i++) { const c = await ev(`window.__sent.length`); still = c === n ? still + 1 : 0; n = c; await new Promise(r => setTimeout(r, 250)); /* sleep-ok: polls for 1.5 s of quiet */ } };
        const gotText = (list, text) => list.some(s => s.kind === 'send' && (s.command || '').includes(text)) || wsTexts(list).some(t => t.includes(text));

        // ── Keys ───────────────────────────────────────────────────────────
        const keys = {};
        for (const [label, sel] of [['Esc', '.btn-esc'], ['Up', '[data-keys="Up"]'], ['Down', '[data-keys="Down"]'], ['Enter', '.btn-enter'], ['Undo', '[data-keys="Escape,Escape"]']]) {
            await clear();
            await click(`#command-toolbar ${sel}[data-on-click="cmd-raw-keys"]`);
            await settle(label);
            const want = { Esc: ['Escape'], Up: ['Up'], Down: ['Down'], Enter: ['Enter'], Undo: ['Escape', 'Escape'] }[label];
            keys[label] = gotKeys(await sent(), want);
        }
        check('Esc, ↑, ↓, Enter and Undo send exactly their keys', Object.values(keys).every(Boolean), JSON.stringify(keys));
        await quiet(); await clear();
        await ev(`(() => { const b = document.createElement('button'); b.setAttribute('data-on-click', 'cmd-raw-keys'); b.dataset.keys = 'Escape,F1'; document.body.appendChild(b); b.click(); b.remove(); return true; })()`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: shows that nothing is sent (no condition to wait for)
        check('a key outside the allowlist sends nothing (Ctrl-C is on it since #6676, F1 is not)', inputs(await sent()).length === 0, JSON.stringify(await sent()));

        // ── Bash, a macro, Mode, Send, the send menu ───────────────────────
        await clear();
        await click('#command-toolbar [data-on-click="cmd-quick"]');
        await settle('Bash');
        check('Bash sends "!"', gotText(await sent(), '!'), JSON.stringify(await sent()));
        const macro = await ev(`import(${MOD} + 'controls.js').then(c => c.getMacros()[0] || null)`);
        await clear();
        await click('#command-toolbar [data-on-click="cmd-macro"][data-index="0"]');
        await settle('the macro');
        check('a macro sends its saved command (by index; no command in the markup)', !!macro && gotText(await sent(), macro.command)
            && !(await ev(`[...document.querySelectorAll('[data-on-click="cmd-macro"]')].some(b => b.getAttribute('data-on-click') !== 'cmd-macro' || 'command' in b.dataset)`)), JSON.stringify({ macro, sent: await sent() }));
        await quiet(); await clear();
        await ev(`(() => { const b = document.createElement('button'); b.setAttribute('data-on-click', 'cmd-macro'); b.dataset.index = '999'; document.body.appendChild(b); b.click(); b.dataset.index = '-1'; b.click(); b.remove(); return true; })()`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: shows that nothing is sent (no condition to wait for)
        check('a macro index that isn\'t in the list sends nothing', inputs(await sent()).length === 0, JSON.stringify(await sent()));
        await clear();
        await click('#command-toolbar [data-on-click="cmd-mode-toggle"]');
        await settle('Mode');
        check('Mode sends Shift+Tab (BTab)', gotKeys(await sent(), ['BTab']), JSON.stringify(await sent()));
        await clear();
        await ev(`document.getElementById('command-input').value = 'echo live-qa-1'; true`);
        await click('#command-toolbar [data-on-click="cmd-send"]');
        await settle('Send');
        check('Send sends the input', gotText(await sent(), 'live-qa-1'), JSON.stringify(await sent()));
        await click('#command-toolbar [data-on-click="cmd-send-menu"]');
        const menuOpen = await ev(`getComputedStyle(document.querySelector('#command-toolbar .send-btn-menu')).display !== 'none'`);
        await clear();
        await ev(`document.getElementById('command-input').value = 'echo live-qa-2'; true`);
        await click('#command-toolbar [data-on-click="send-menu-send"]');
        await settle('the menu Send');
        const menuClosed = await ev(`getComputedStyle(document.querySelector('#command-toolbar .send-btn-menu')).display === 'none'`);
        check('the send menu opens; its Send sends and closes it', menuOpen && menuClosed && gotText(await sent(), 'live-qa-2'), JSON.stringify({ menuOpen, menuClosed, sent: await sent() }));

        // ── View toggle, side panel ────────────────────────────────────────
        // The mode is saved per viewer (a terminal agent has no chat to show, so the button state alone can't tell).
        const mode = await ev(`(() => { const out = {}; for (const m of ['terminal', 'chat', 'terminal']) { document.querySelector('[data-on-click="live-view-mode"][data-mode="' + m + '"]').click();
            out[m + Object.keys(out).length] = localStorage.getItem('coral-live-view-mode'); } return out; })()`);
        check('the Chat/Terminal toggle switches the view', JSON.stringify(Object.values(mode)) === JSON.stringify(['terminal', 'chat', 'terminal']), JSON.stringify(mode));
        // The side panel may start collapsed on a small runner (QA's run): open it first.
        await ev(`window.toggleAgenticPanel(true); true`);
        await until(`[...document.querySelectorAll('#agentic-state [data-on-click="agentic-tab"]')].some(b => b.offsetParent !== null)`, 'the side panel open', 3000);
        const tabs = await ev(`(() => { const out = {}; for (const b of document.querySelectorAll('#agentic-state [data-on-click="agentic-tab"]')) {
            if (b.offsetParent === null) continue; b.click(); out[b.dataset.tab] = b.classList.contains('active') || b.getAttribute('aria-selected') === 'true'; } return out; })()`);
        check('each visible side panel tab opens and is marked active', Object.keys(tabs).length >= 3 && Object.values(tabs).every(Boolean), JSON.stringify(tabs));
        const bogus = await ev(`(() => { const active = () => [...document.querySelectorAll('#agentic-state [data-on-click="agentic-tab"]')].filter(b => b.classList.contains('active')).map(b => b.dataset.tab).join(',');
            const before = active(); const b = document.createElement('button'); b.setAttribute('data-on-click', 'agentic-tab'); b.dataset.tab = 'nope'; document.body.appendChild(b); b.click(); b.remove(); return { before, after: active() }; })()`);
        check('an unknown side panel tab does nothing', bogus.before === bogus.after, JSON.stringify(bogus));

        // ── Restart asks first; it is on the destructive list ──────────────
        const listed = await ev(`import(${MOD} + 'actions.js').then(a => a.DESTRUCTIVE_ACTIONS.has('session-restart') && a.DESTRUCTIVE_ACTIONS.has('mb-msg-delete') && a.DESTRUCTIVE_ACTIONS.has('wf-kill-run') && a.SEND_ACTIONS.has('cmd-send') && a.SEND_ACTIONS.has('cmd-raw-keys'))`);
        check('restart is on the shared destructive list, Send and keys on the send list', listed);
        await quiet(); await clear();
        await ev(`(() => { const b = document.querySelector('#live-session-view [data-on-click="session-restart"]'); if (b) b.click(); return true; })()`);
        // The dialog loads the dialogs module first.
        await until(`(() => { const m = document.getElementById('restart-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'the restart dialog', 5000);
        const r = await ev(`(() => { const m = document.getElementById('restart-modal'); return m ? getComputedStyle(m).display : 'no modal'; })()`);
        check('Restart opens its confirmation and sends nothing yet', r !== 'none' && r !== 'no button' && r !== 'no modal' && inputs(await sent()).length === 0, r);
        await ev(`window.hideRestartModal ? window.hideRestartModal() : (document.getElementById('restart-modal').style.display = 'none'); true`);

        const inline = await ev(`[...document.querySelectorAll('#live-session-view *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + '.' + el.className + ' ' + a.name + '=' + a.value.slice(0, 30)))`);
        check('no inline handler left in the live view or its command pane', inline.length === 0, JSON.stringify(inline.slice(0, 5)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        if (agent && agent.session_name) {
            try { await ev(`fetch('/api/sessions/live/' + encodeURIComponent(${JSON.stringify(agent.session_name)}) + '/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: ${JSON.stringify(agent.session_id)} }) }).then(() => true)`); } catch { /* best effort */ }
        }
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
