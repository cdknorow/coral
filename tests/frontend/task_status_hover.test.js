const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;

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
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`(async () => {
            const {state} = await import((window.__coralStatic||'/static/')+'state.js');
            const tasks = await import((window.__coralStatic||'/static/')+'tasks.js');
            state.currentSession = {name:'task-status-hover-fixture', type:'live'};
            state.currentAgentTasks = [];
            state.currentSubagents = [];
            document.getElementById('board-tasks-section')?.remove();
            document.getElementById('board-task-list')?.remove();
            document.body.insertAdjacentHTML('beforeend', '<section id="board-tasks-section" style="display:block"><div class="board-tasks-header"><span></span></div><div id="board-task-hide-toggle-container"></div><div id="board-task-list"></div></section>');
            if (!document.getElementById('task-bar-count')) document.body.insertAdjacentHTML('beforeend', '<span id="task-bar-count"></span>');
            state.currentBoardTasks = [
                {id:1497, title:'Assigned working fixture', status:'in_progress', assigned_to:'Frontend Dev'},
                {id:1498, title:'Blocked fixture', status:'blocked', assigned_to:'QA Engineer', blocked_by:[1496]},
                {id:1499, title:'Finished fixture', status:'completed', assigned_to:'Lead Developer', claimed_at:'2026-09-28T10:00:00Z'},
                {id:1500, title:'Claimed fixture', status:'in_progress', assigned_to:'Backend Dev', claimed_at:'2026-09-29T10:00:00Z'},
                {id:1501, title:'Assigned open fixture', status:'pending', assigned_to:'ML Prompt Engineer'},
                {id:1502, title:'Review fixture', status:'review_pending', assigned_to:'QA Engineer', claimed_at:'2026-09-28T09:00:00Z'}
            ];
            tasks.renderBoardTaskList();
        })()`);

        const status = await ev(`(() => {
            const rows = [...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')];
            return rows.map(row => {
                const wrapper = row.querySelector('.board-task-status-wrap');
                return {
                    id: row.querySelector('.board-task-id')?.textContent.trim(),
                    text: wrapper?.querySelector('.board-task-status-label')?.textContent.trim() || '',
                    title: wrapper?.getAttribute('title'),
                    aria: wrapper?.getAttribute('aria-label'),
                    tabIndex: wrapper?.tabIndex,
                    role: wrapper?.getAttribute('role'),
                    label: Boolean(wrapper?.querySelector('.board-task-status-label')),
                    spinner: Boolean(wrapper?.querySelector('.task-spinner')),
                    hourglass: wrapper?.querySelector('.material-icons')?.textContent.trim() || '',
                    desc: row.querySelector('.board-task-desc')?.textContent.trim() || '',
                    badge: Boolean(row.querySelector('.board-task-blocked-label'))
                };
            });
        })()`);
        const working = status.find(row => row.id === '#1497');
        const blocked = status.find(row => row.id === '#1498');
        assert.equal(working.text, 'Open');
        assert.equal(working.title, 'Open · Working');
        assert.equal(working.aria, 'Open · Working');
        assert.equal(working.tabIndex, -1, 'the status is not a tab stop (#5813): its words are in the Open button\'s name');
        assert.equal(working.role, 'img');
        assert.equal(working.label, true);
        assert.equal(working.spinner, true);
        assert.equal(blocked.text, 'Blocked', 'a blocked task reads Blocked (#3170)');
        assert.equal(blocked.title, 'Blocked: waiting for prerequisites');
        assert.equal(blocked.aria, 'Blocked: waiting for prerequisites');
        assert.equal(blocked.tabIndex, -1, 'the status is not a tab stop (#5813): its words are in the Open button\'s name');
        assert.equal(blocked.role, 'img');
        assert.equal(blocked.label, true);
        assert.equal(blocked.hourglass, 'hourglass_empty');
        assert.equal(blocked.badge, false);

        const finished = status.find(row => row.id === '#1499');
        assert.equal(finished.text, 'Finished');
        assert.equal(finished.title, 'Finished');
        assert.equal(finished.aria, 'Finished');
        assert.equal(finished.tabIndex, -1, 'the status is not a tab stop (#5813): its words are in the Open button\'s name');
        assert.equal(finished.label, true);

        const claimed = status.find(row => row.id === '#1500');
        assert.equal(claimed.text, 'Claimed');
        assert.equal(claimed.title, 'Claimed · Working');
        assert.equal(claimed.aria, 'Claimed · Working');
        assert.equal(claimed.tabIndex, -1, 'the status is not a tab stop (#5813): its words are in the Open button\'s name');
        assert.equal(claimed.label, true);

        const assignedOpen = status.find(row => row.id === '#1501');
        assert.equal(assignedOpen.text, 'Open');
        assert.equal(assignedOpen.title, 'Open');

        const review = status.find(row => row.id === '#1502');
        // The visible label is short so it fits the fixed status column; the full status stays in the tooltip.
        assert.equal(review.text, 'Review');
        assert.equal(review.title, 'Open · Review pending');

        // the row's one keyboard control says the task and its status (#5813): "Open task #N: title, Open · Working"
        const focus = await ev(`(() => { const row = document.querySelector('#board-task-list .board-task-item:not(.board-task-header)'); const el = row.querySelector('.board-task-status-wrap'); el.focus(); return { label: el.getAttribute('aria-label'), tabIndex: el.tabIndex, focused: document.activeElement === el, open: row.querySelector('.board-task-open').getAttribute('aria-label') }; })()`);
        assert.equal(focus.label, 'Open · Working');
        assert.equal(focus.tabIndex, -1);
        assert.equal(focus.focused, false, 'the status image takes no focus');
        assert.match(focus.open, /^Open task #\d+: .+, Open · Working$/, 'the Open button carries the status: ' + focus.open);
        await ev(`document.getElementById('board-tasks-section').scrollIntoView({block:'start'})`);
        require('node:fs').writeFileSync('/tmp/coral-1497-task-status-hover.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
        console.log(JSON.stringify({working, blocked, focus}));
    } finally {
        await client.close();
    }
})().catch(error => { console.error(error); process.exit(1); });
