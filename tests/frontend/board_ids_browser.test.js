// Board ids in the dashboard (#3538, with #3536): a launch that names a board that exists asks
// whether to join it or start another (no default; Cancel and Escape launch nothing); joining
// lands on that board, another starts a second board with the same name; the agent list and the
// launch picker show labels, with a short id when two boards share one; Team settings renames a
// board (its label only), with the name rule checked inline.
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
    const until = (expr, what, timeout = 8000) => waitFor(expr, what, { timeout, soft: true });
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)},
        headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} })
        .then(async r => ({ status: r.status, data: await r.json().catch(() => null) }))`);
    const NAME = 'ids-qa-3538';
    const sessions = [];
    const boards = new Set();
    // A terminal launched through the dashboard's own launch path (_launchOn), left running: the
    // promise resolves after any choice dialog is answered.
    const launch = () => ev(`(async () => {
        const m = await import((window.__coralStatic || '/static/') + 'modals.js');
        window.__launchDone = false;
        window.__launch = m._launchOn('local', '/api/sessions/launch', { working_dir: ${JSON.stringify(require('node:os').tmpdir())}, agent_type: 'terminal', display_name: 'Ids QA', board_name: ${JSON.stringify(NAME)} })
            .then(r => { window.__launchResult = r; window.__launchDone = true; return r; });
        return true;
    })()`);
    const result = async () => { await until('window.__launchDone === true', 'the launch'); return ev('window.__launchResult'); };
    const live = async sid => {
        const r = await api('GET', '/api/sessions/live');
        const list = Array.isArray(r.data) ? r.data : (r.data && r.data.sessions) || [];
        return list.find(s => s.session_id === sid) || null;
    };
    const record = async res => {
        if (res && res.session_id) {
            sessions.push({ sid: res.session_id, name: res.session_name });
            const s = await live(res.session_id);
            if (s && s.board_project) boards.add(s.board_project);
            return s;
        }
        return null;
    };

    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        console.log('1. A new name starts a board with an id, shown by its label');
        await launch();
        const first = await record(await result());
        check('the first launch starts a board with a b- id and the label', !!first && /^b-/.test(first.board_project) && first.board_label === NAME, JSON.stringify(first && { board: first.board_project, label: first.board_label }));
        const idA = first && first.board_project;

        console.log('2. The same name again: the choice, with no default');
        await launch();
        await until(`!!document.getElementById('board-choice-modal')`, 'the choice dialog');
        const dlg = await ev(`(() => { const d = document.getElementById('board-choice-modal');
            return d && { title: d.querySelector('h3').textContent, rows: [...d.querySelectorAll('.board-choice-row')].map(r => r.textContent),
                focus: document.activeElement && document.activeElement.dataset.action, joins: [...d.querySelectorAll('[data-board-id]')].map(b => b.dataset.boardId),
                role: d.querySelector('[role=dialog]') && d.querySelector('[role=dialog]').getAttribute('aria-modal') }; })()`);
        check('it names the board and lists it to join', !!dlg && dlg.title.includes(NAME) && dlg.joins.length === 1 && dlg.joins[0] === idA && /members?/.test(dlg.rows[0] || '') && dlg.role === 'true', JSON.stringify(dlg));
        check('no choice is preselected (focus on Cancel)', dlg && dlg.focus === 'cancel', dlg && dlg.focus);
        check('nothing launched while it waits', (await ev('window.__launchDone')) === false);
        await ev(`document.querySelector('#board-choice-modal [data-board-id]').click(); true`);
        const joined = await record(await result());
        check('"Join it" launches on that board', !!joined && joined.board_project === idA, JSON.stringify(joined && joined.board_project));

        console.log('3. Create another: a second board with the same name; labels get a short id');
        await launch();
        await until(`!!document.getElementById('board-choice-modal')`, 'the choice dialog');
        await ev(`document.querySelector('#board-choice-modal [data-action="new"]').click(); true`);
        const another = await record(await result());
        const idB = another && another.board_project;
        check('"Create another" starts a new board with that label', !!another && /^b-/.test(idB) && idB !== idA && another.board_label === NAME, JSON.stringify(another && { board: idB, label: another.board_label }));
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'api.js'); await m.loadLiveSessions(); return true; })()`);
        const headerText = id => `[...document.querySelectorAll('#live-sessions-list .session-group-header')].filter(h => h.dataset.groupName === ${JSON.stringify('')} + ${JSON.stringify(id)}).map(h => h.querySelector('.group-name-line') ? h.querySelector('.group-name-line').textContent.trim() : h.textContent.trim())[0] || null`;
        await until(`!!(${headerText(idA)}) && !!(${headerText(idB)})`, 'both team groups');
        const hA = await ev(headerText(idA)), hB = await ev(headerText(idB));
        check('the agent list shows each board by its label, told apart by a short id',
            !!hA && !!hB && hA.startsWith(`${NAME} (${idA.slice(0, 6)})`) && hB.startsWith(`${NAME} (${idB.slice(0, 6)})`) && !hA.includes(idA) && !hB.includes(idB), JSON.stringify({ hA, hB }));

        console.log('3a. A board page opened directly shows its label in the header, not its id');
        for (const id of [idA, idB]) {
            const l2 = Page.loadEventFired();
            await Page.navigate({ url: `${BASE}/board/${id}` });
            await l2;
            await coralReady();
            await until(`((document.getElementById('messageboard-project-badge') || {}).textContent || '').startsWith(${JSON.stringify(NAME)})`, 'the header label');
            const badge = await ev(`(document.getElementById('messageboard-project-badge') || {}).textContent || ''`);
            check(`/board/<${id.slice(0, 6)}…>: the header shows the label (with its short id), never the bare id`, badge.startsWith(NAME) && badge !== id && badge.includes(`(${id.slice(0, 6)})`), badge);
        }

        console.log("3b. The operator's case: a second board next to an older one whose id is its name");
        const LEGACY = 'ids-legacy-3538';
        boards.add(LEGACY);
        await api('POST', `/api/board/${LEGACY}/subscribe`, { subscriber_id: 'Ids QA', job_title: 'QA' }); // a board keyed by its name
        const legacyLaunch = await ev(`(async () => {
            const m = await import((window.__coralStatic || '/static/') + 'modals.js');
            window.__launchDone = false;
            window.__launch = m._launchOn('local', '/api/sessions/launch', { working_dir: ${JSON.stringify(require('node:os').tmpdir())}, agent_type: 'terminal', display_name: 'Ids QA', board_name: ${JSON.stringify(LEGACY)} })
                .then(r => { window.__launchResult = r; window.__launchDone = true; return r; });
            return true; })()`);
        await until(`!!document.getElementById('board-choice-modal')`, 'the choice for the older board');
        const offers = await ev(`({ join: [...document.querySelectorAll('#board-choice-modal [data-board-id]')].map(b => b.dataset.boardId), another: !!document.querySelector('#board-choice-modal [data-action="new"]') })`);
        check('the older board is offered to join, and another can be made', legacyLaunch && offers.join[0] === LEGACY && offers.another, JSON.stringify(offers));
        await ev(`document.querySelector('#board-choice-modal [data-action="new"]').click(); true`);
        const second = await record(await result());
        check('"Create another" next to the older board starts a b- board with that label', !!second && /^b-/.test(second.board_project) && second.board_label === LEGACY, JSON.stringify(second && { board: second.board_project, label: second.board_label }));
        if (second) {
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'api.js'); await m.loadLiveSessions(); return true; })()`);
            await until(`(${headerText(second.board_project)} || '').length > 0`, 'the new group');
            const label2 = await ev(headerText(second.board_project));
            check("the new board shows its label with a short id (the older one has none)", (label2 || '').startsWith(`${LEGACY} (${second.board_project.slice(0, 6)})`), label2);
        }

        console.log('4. Cancel and Escape launch nothing');
        const before = sessions.length;
        await launch();
        await until(`!!document.getElementById('board-choice-modal')`, 'the choice dialog');
        await ev(`document.querySelector('#board-choice-modal [data-action="cancel"]').click(); true`);
        check('Cancel: no launch', (await result()) === null && !(await ev(`!!document.getElementById('board-choice-modal')`)));
        await launch();
        await until(`!!document.getElementById('board-choice-modal')`, 'the choice dialog');
        await ev(`document.querySelector('#board-choice-modal [data-action="cancel"]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
        check('Escape: no launch', (await result()) === null && !(await ev(`!!document.getElementById('board-choice-modal')`)));
        check('and no agent was started', sessions.length === before);

        console.log('5. The launch picker lists boards by label');
        const opts = await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js');
            m.showLaunchModal && m.showLaunchModal(); window._selectLaunchType('agent'); // the agent step fills the board picker
            const sel = document.getElementById('launch-board-select'); if (!sel) return null;
            for (let i = 0; i < 100 && ![...sel.options].some(o => o.value === ${JSON.stringify(idB)}); i++) await new Promise(r => setTimeout(r, 50));
            const o = [...sel.options].filter(o => o.value === ${JSON.stringify(idA)} || o.value === ${JSON.stringify(idB)}).map(o => o.textContent);
            m.hideLaunchModal && m.hideLaunchModal(); return o; })()`);
        check('both boards are offered by label, with a short id', Array.isArray(opts) && opts.length === 2 && opts.every(t => t.startsWith(NAME + ' (b-')), JSON.stringify(opts));

        console.log('6. Rename in Team settings: the label only');
        await ev(`window.enterTeamContext(${JSON.stringify(idB)}, { tab: 'team-settings' }); true`);
        await until(`!!document.querySelector('#team-settings-workspace .board-rename-toggle')`, 'the rename control');
        await ev(`document.querySelector('#team-settings-workspace .board-rename-toggle').click(); true`);
        const prefilled = await ev(`document.querySelector('#team-settings-workspace .board-rename-form input').value`);
        check('the field holds the label (no short id)', prefilled === NAME, prefilled);
        let posted = 0;
        await ev(`(() => { const f = window.fetch; window.__patches = 0; window.fetch = (u, o) => { if (o && o.method === 'PATCH') window.__patches++; return f(u, o); }; return true; })()`);
        await ev(`(() => { const i = document.querySelector('#team-settings-workspace .board-rename-form input'); i.value = 'bad name!'; i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); return true; })()`);
        const bad = await ev(`({ err: document.querySelector('#team-settings-workspace .board-rename-error').textContent, invalid: document.querySelector('#team-settings-workspace .board-rename-form input').getAttribute('aria-invalid'), patches: window.__patches })`);
        check('a bad name is refused inline, with no request', /letters, digits/.test(bad.err) && bad.invalid === 'true' && bad.patches === 0, JSON.stringify(bad));
        await ev(`(() => { const i = document.querySelector('#team-settings-workspace .board-rename-form input'); i.value = 'ids-renamed'; i.closest('.board-rename-form').querySelector('.btn-primary').click(); return true; })()`);
        await until(`document.querySelector('#team-settings-workspace .working-mode-team').textContent === 'ids-renamed'`, 'the renamed heading');
        const desc = await api('GET', `/api/board/${encodeURIComponent(idB)}`);
        check('the board is renamed; its id is the same', desc.status === 200 && desc.data && desc.data.board_label === 'ids-renamed' && desc.data.board_id === idB, JSON.stringify(desc));
        await until(`(${headerText(idB)} || '').startsWith('ids-renamed')`, 'the renamed group');
        const after = { hA: await ev(headerText(idA)), hB: await ev(headerText(idB)) };
        check('the agent list follows, and the short id goes with the clash', (after.hB || '').startsWith('ids-renamed') && !(after.hB || '').includes('(b-') && (after.hA || '').startsWith(NAME) && !(after.hA || '').includes('(b-'), JSON.stringify(after));
        const still = await live(another.session_id);
        check("its agent's board key is unchanged", !!still && still.board_project === idB, JSON.stringify(still && still.board_project));
        posted = await ev('window.__patches');
        check('one rename request', posted === 1, String(posted));
        await ev(`window.exitTeamContext && window.exitTeamContext({ goBack: false, force: true }); true`);
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        try {
            for (const s of sessions) await api('POST', `/api/sessions/live/${encodeURIComponent(s.name)}/kill`, { agent_type: 'terminal', session_id: s.sid });
            for (const b of boards) await api('DELETE', `/api/board/${encodeURIComponent(b)}`);
        } catch { /* best effort */ }
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
