// CSP stage 2, slice 4a (#3645): the message board on delegated data-on-* actions, against a real
// board (a terminal agent launched on a team, killed at the end). Each moved action still does
// what its inline handler did: open a board from the list, post (the Send button, Enter; never
// Shift+Enter), select mode (a message's checkbox, All, None, Export Markdown, Cancel), export
// (its agent checkboxes All/None, Export, Cancel), the subscribers sheet (open, a subscriber's
// history link, backdrop, Close), pause, deleting a message; deleting the board still asks
// first (refused here, so the board stays); a bad message id does nothing; no inline handler
// is left in the board view.
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
const { removeBoards, removeTeams } = require('./helpers/board_cleanup');
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    let agent = null;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__downloads = [];
            const real = HTMLAnchorElement.prototype.click;
            HTMLAnchorElement.prototype.click = function () { if (this.download) { window.__downloads.push(this.download); return; } return real.call(this); };
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        agent = await ev(`fetch('/api/sessions/launch-team', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ board_name: 'board-3645', working_dir: ${JSON.stringify(require('node:os').tmpdir())}, agent_type: 'terminal', agents: [{ name: 'Board QA', prompt: 'x' }] }) })
            .then(r => r.json()).then(j => ({ ...j.agents[0], board: j.board, label: j.board_label }))`);

        // ── Open the board from the list ───────────────────────────────────
        await ev(`window.switchNavTab('board'); window.showMessageBoardProjects && window.showMessageBoardProjects(); true`);
        await until(`!!document.querySelector('#mb-projects-ul [data-on-click="mb-project"][data-project="${agent.board}"]')`, 'the board in the list');
        await ev(`document.querySelector('#mb-projects-ul [data-on-click="mb-project"][data-project="${agent.board}"]').click(); true`);
        await until(`document.getElementById('mb-board') && getComputedStyle(document.getElementById('mb-board')).display !== 'none'`, 'the board');
        check('a board in the list opens it', await ev(`getComputedStyle(document.getElementById('mb-project-list')).display === 'none'`));

        // ── Post ───────────────────────────────────────────────────────────
        const post = async (text, how) => ev(`(() => { const t = document.getElementById('mb-post-input'); t.value = ${JSON.stringify(text)};
            ${how === 'button' ? `document.querySelector('[data-on-click="mb-post"]').click();`
              : `t.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', shiftKey: ${how === 'shift'}, bubbles: true, cancelable: true }));`}
            return true; })()`);
        await post('first by button', 'button');
        await until(`document.getElementById('mb-messages').textContent.includes('first by button')`, 'the posted message');
        check('Send posts the message', await ev(`document.getElementById('mb-messages').textContent.includes('first by button')`));
        await post('second by enter', 'enter');
        await until(`document.getElementById('mb-messages').textContent.includes('second by enter')`, 'the Enter post');
        check('Enter posts it', await ev(`document.getElementById('mb-messages').textContent.includes('second by enter')`));
        await post('third not sent', 'shift');
        await new Promise(r => setTimeout(r, 800)); // sleep-ok: shows that Shift+Enter sends nothing (no condition to wait for)
        check('Shift+Enter does not post', !(await ev(`document.getElementById('mb-messages').textContent.includes('third not sent')`)));
        await ev(`document.getElementById('mb-post-input').value = ''; true`);

        // ── Select mode ────────────────────────────────────────────────────
        await ev(`(() => { const b = document.getElementById('mb-select-btn'); b.style.display = ''; b.click(); return true; })()`);
        await until(`document.querySelectorAll('#mb-messages [data-on-click="mb-msg-select"]').length >= 2`, 'the select checkboxes');
        const sel = await ev(`(() => {
            const boxes = [...document.querySelectorAll('#mb-messages [data-on-click="mb-msg-select"]')];
            boxes[0].click();
            const one = boxes.filter(b => b.checked).length;
            document.querySelector('[data-on-click="mb-select-all"]').click();
            const all = [...document.querySelectorAll('#mb-messages [data-on-click="mb-msg-select"]')].filter(b => b.checked).length;
            document.querySelector('[data-on-click="mb-select-none"]').click();
            const none = [...document.querySelectorAll('#mb-messages [data-on-click="mb-msg-select"]')].filter(b => b.checked).length;
            document.querySelector('[data-on-click="mb-select-all"]').click();
            document.querySelector('[data-on-click="mb-export-selected"]').click();
            return { boxes: boxes.length, one, all, none };
        })()`);
        check('a message\'s checkbox selects it; All selects all; None clears them', sel.one === 1 && sel.all === sel.boxes && sel.none === 0, JSON.stringify(sel));
        await until(`window.__downloads.length > 0`, 'the selected export', 3000);
        check('Export Markdown downloads the selection', (await ev(`window.__downloads`)).some(d => /selected-export\.md$/.test(d)), JSON.stringify(await ev(`window.__downloads`)));
        await ev(`document.querySelector('[data-on-click="mb-select-cancel"]').click(); true`);
        check('Cancel leaves select mode', await ev(`document.querySelectorAll('#mb-messages [data-on-click="mb-msg-select"]').length === 0`));

        // ── Export ─────────────────────────────────────────────────────────
        await ev(`(() => { const b = document.getElementById('mb-export-btn'); b.style.display = ''; b.click(); return true; })()`);
        await until(`!!document.querySelector('[data-on-click="mb-export-do"]')`, 'the export dialog');
        const cbs = await ev(`(() => { const all = document.querySelector('[data-on-click="mb-export-agents"][data-checked="1"]'), none = document.querySelector('[data-on-click="mb-export-agents"][data-checked="0"]');
            const boxes = () => [...document.querySelectorAll('.export-agent-cb')]; if (!boxes().length) return { n: 0 };
            none.click(); const off = boxes().every(c => !c.checked); all.click(); const on = boxes().every(c => c.checked); return { n: boxes().length, off, on }; })()`);
        const cbWidth = await ev(`Math.max(...[...document.querySelectorAll('.export-agent-cb')].map(c => c.getBoundingClientRect().width))`);
        check('each agent checkbox is checkbox-sized, not stretched across its row (#3702)', cbWidth > 0 && cbWidth <= 24, String(cbWidth));
        check('the export dialog\'s All/None set every agent checkbox', cbs.n >= 1 && cbs.off && cbs.on, JSON.stringify(cbs));
        await ev(`window.__downloads.length = 0; document.querySelector('[data-on-click="mb-export-do"]').click(); true`);
        await until(`window.__downloads.length > 0`, 'the board export', 5000);
        check('Export downloads the board', (await ev(`window.__downloads.length`)) > 0, JSON.stringify(await ev(`window.__downloads`)));
        await ev(`(() => { const b = document.getElementById('mb-export-btn'); b.click(); return true; })()`);
        await until(`!!document.querySelector('[data-on-click="mb-export-do"]')`, 'the export dialog again');
        const exportOpen = () => ev(`getComputedStyle(document.getElementById('mb-export-modal')).display !== 'none'`);
        check('Export has its own dialog, not the shared confirm dialog', (await exportOpen()) && await ev(`document.getElementById('mb-export-modal').contains(document.querySelector('[data-on-click="mb-export-do"]')) && getComputedStyle(document.getElementById('confirm-modal')).display === 'none'`));
        await ev(`document.querySelector('[data-on-click="mb-export-close"]').click(); true`);
        check('its Cancel closes it', !(await exportOpen()));
        await ev(`document.getElementById('mb-export-btn').click(); true`);
        await until(`getComputedStyle(document.getElementById('mb-export-modal')).display !== 'none'`, 'the export dialog a third time');
        await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
        check('Escape closes it', !(await exportOpen()));
        await ev(`document.getElementById('mb-export-btn').click(); true`);
        await until(`getComputedStyle(document.getElementById('mb-export-modal')).display !== 'none'`, 'the export dialog a fourth time');
        await ev(`document.getElementById('mb-export-modal').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`);
        check('a click on its backdrop closes it', !(await exportOpen()));
        // The shared confirm dialog is untouched by all that: it still has its message and its Yes (#3701).
        check('after Export, the shared confirm dialog still has its message and Yes button', await ev(`!!document.getElementById('confirm-modal-message') && !!document.getElementById('confirm-modal-yes') && !!document.getElementById('confirm-modal-title')`));

        // ── Subscribers sheet ──────────────────────────────────────────────
        const sheetOpen = () => ev(`getComputedStyle(document.getElementById('mb-subscribers-sheet')).display !== 'none'`);
        await ev(`document.querySelector('[data-on-click="mb-subscribers"]').click(); true`);
        await until(`getComputedStyle(document.getElementById('mb-subscribers-sheet')).display !== 'none'`, 'the sheet');
        const opened = await sheetOpen();
        await ev(`document.querySelector('#mb-subscribers-sheet .modal-content, #mb-subscribers-sheet > div').click(); true`);
        const insideKept = await sheetOpen();
        await ev(`document.getElementById('mb-subscribers-sheet').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`);
        const backdropClosed = !(await sheetOpen());
        await ev(`document.querySelector('[data-on-click="mb-subscribers"]').click(); true`);
        await until(`getComputedStyle(document.getElementById('mb-subscribers-sheet')).display !== 'none'`, 'the sheet again');
        await ev(`document.querySelector('[data-on-click="mb-subscribers-close"]').click(); true`);
        check('Subscribers opens the sheet; a click inside keeps it; its backdrop and Close close it', opened && insideKept && backdropClosed && !(await sheetOpen()), `${opened}/${insideKept}/${backdropClosed}`);
        const hist = await ev(`(() => { const l = document.querySelector('[data-on-click="mb-subscriber-history"]'); return l ? { s: l.dataset.session, n: l.dataset.name } : null; })()`);
        check('a subscriber\'s history link carries its session as data, not code', !!hist && hist.s === agent.session_name, JSON.stringify(hist));

        // ── Pause, delete a message, delete the board (refused) ────────────
        const pauseText = () => ev(`document.getElementById('mb-pause-btn').textContent.trim()`);
        const p0 = await pauseText();
        await ev(`(() => { const b = document.getElementById('mb-pause-btn'); b.style.display = ''; b.click(); return true; })()`);
        await until(`document.getElementById('mb-pause-btn').textContent.trim() !== ${JSON.stringify(p0)}`, 'paused');
        const paused = await ev(`fetch('/api/board/${agent.board}/paused').then(r => r.json())`);
        check('Pause pauses the board', paused.paused === true, JSON.stringify(paused));
        await ev(`document.getElementById('mb-pause-btn').click(); true`);
        await until(`document.getElementById('mb-pause-btn').textContent.trim() === ${JSON.stringify(p0)}`, 'resumed');

        const before = await ev(`document.querySelectorAll('#mb-messages [data-on-click="mb-msg-delete"]').length`);
        await ev(`(() => { const b = document.createElement('button'); b.setAttribute('data-on-click', 'mb-msg-delete'); b.dataset.msgId = '1; DROP'; document.body.appendChild(b); b.click(); b.remove(); return true; })()`);
        await new Promise(r => setTimeout(r, 500)); // sleep-ok: shows that a bad id deletes nothing (no condition to wait for)
        check('a delete with a bad id does nothing', (await ev(`document.querySelectorAll('#mb-messages [data-on-click="mb-msg-delete"]').length`)) === before);
        // A message's × asks first (#3696): the dialog names the sender and the text, Cancel deletes
        // nothing, and only Yes deletes. The real confirm dialog is used, not a stub.
        const modalState = () => ev(`(() => { const m = document.getElementById('confirm-modal'); if (!m) return { open: false, title: '', message: '' }; return { open: getComputedStyle(m).display !== 'none', title: document.getElementById('confirm-modal-title').textContent, message: document.getElementById('confirm-modal-message').textContent }; })()`);
        const countMsgs = () => ev(`document.querySelectorAll('#mb-messages [data-on-click="mb-msg-delete"]').length`);
        await ev(`document.querySelector('#mb-messages [data-on-click="mb-msg-delete"]').click(); true`);
        await until(`!!document.getElementById('confirm-modal') && getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirm dialog');
        const asked = await modalState();
        check('a message\'s × asks first, naming what is deleted', asked.open && asked.title === 'Delete Message' && /Delete the message from .+\? This cannot be undone\./.test(asked.message), JSON.stringify(asked));
        check('...and nothing is deleted while it is open', (await countMsgs()) === before);
        await ev(`document.querySelector('#confirm-modal .btn:not(#confirm-modal-yes)').click(); true`);
        await new Promise(r => setTimeout(r, 500)); // sleep-ok: shows that Cancel deletes nothing (no condition to wait for)
        const cancelled = await modalState();
        check('Cancel closes the dialog and deletes nothing', !cancelled.open && (await countMsgs()) === before, JSON.stringify({ cancelled, n: await countMsgs(), before }));
        await ev(`document.querySelector('#mb-messages [data-on-click="mb-msg-delete"]').click(); true`);
        await until(`!!document.getElementById('confirm-modal') && getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirm dialog again');
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await until(`document.querySelectorAll('#mb-messages [data-on-click="mb-msg-delete"]').length === ${before - 1}`, 'one message deleted');
        check('Yes deletes the message', (await countMsgs()) === before - 1, `${before} -> ${await countMsgs()}`);

        // With the board's agent still running the Delete says so instead of asking (#5043); once it is stopped, the delete asks first and, without a Yes, leaves the board.
        const boardListed = () => ev(`fetch('/api/board/projects').then(r => r.json()).then(p => (Array.isArray(p) ? p : p.projects || []).some(x => x.project === ${JSON.stringify(agent.board)}))`);
        await ev(`window.__confirmModal = null; window.showConfirmModal = t => { window.__confirmModal = t; }; (() => { const b = document.getElementById('mb-delete-btn'); b.style.display = ''; b.click(); })(); true`);
        await until(`[...document.querySelectorAll('.toast')].some(t => /still on this board.*kill (it|them) or move (it|them) first/.test(t.textContent))`, 'the refusal while the agent runs');
        check('a board with its agent running is not deleted: Delete says so instead of asking (#5043)', (await ev(`window.__confirmModal`)) === null && (await boardListed()), JSON.stringify({ modal: await ev(`window.__confirmModal`) }));
        await ev(`fetch('/api/sessions/live/' + encodeURIComponent(${JSON.stringify(agent.session_name)}) + '/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: ${JSON.stringify(agent.session_id)} }) }).then(() => true)`);
        await until(`!document.querySelector('#live-sessions-list [data-session-id="${agent.session_id}"]')`, 'the stopped agent gone from the list');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__confirmModal = null; document.getElementById('mb-delete-btn').click(); true`);
        await new Promise(r => setTimeout(r, 300)); // sleep-ok: shows that nothing is deleted without a Yes
        const still = await boardListed();
        check('with nothing running, deleting the board asks first; without a Yes it stays', (await ev(`window.__confirmModal`)) === 'Delete Board' && still, JSON.stringify({ modal: await ev(`window.__confirmModal`), still }));

        const inline = await ev(`[...document.querySelectorAll('#messageboard-view *, #mb-subscribers-sheet *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + '.' + el.className + ' ' + a.name))`);
        check('no inline handler left in the board view or its sheet', inline.length === 0, JSON.stringify(inline.slice(0, 5)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        if (agent) {
            try { await ev(`fetch('/api/sessions/live/' + encodeURIComponent(${JSON.stringify(agent.session_name)}) + '/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: ${JSON.stringify(agent.session_id)} }) }).then(() => true)`); } catch { /* best effort */ }
        }
        await removeBoards(ev, [agent && agent.board]); await removeTeams(ev, [agent && agent.board]); // the team record launch-team made (#5343)
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
