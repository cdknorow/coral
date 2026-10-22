// #4853 (Security's sweep of how the dashboard renders labels a client or an agent controls): every string an API stores and
// the dashboard later shows is planted through the real API with markup in it, the pages that show it are opened, and
//  - a hook on every HTML sink (innerHTML, outerHTML, insertAdjacentHTML, createContextualFragment, DOMParser, document.write)
//    records any string that carries the planted markup UNESCAPED (a raw "<i data-pwn=..." tag, or a raw quote before
//    " data-pwn="), with the caller's stack: the sanitizer's own parse (vendor/purify) is not a finding, what it returns is;
//  - the page must hold no element carrying a data-pwn attribute at the end (an injected element: the dashboard's own
//    controls are found by their data-on-* attributes, so an injected one could press a button for the Operator).
// The payload closes the usual surrounding contexts first (textarea, title, style, select, a, button), then plants a tag, a
// double-quote attribute break, a single-quote attribute break and an image. A field the API refuses is a field that never
// shows (listed). A positive control proves the hook sees a raw sink: a string put through innerHTML on purpose is found.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { launchStandIn } = require('./helpers/stand_in_agent');
const { removeBoards } = require('./helpers/board_cleanup');

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const MOD = `(window.__coralStatic || '/static/')`;

const S = f => `"><i data-pwn="${f}"></i>" data-pwn="${f}" x="'`;
const P = f => `</textarea></title></style></select></a></button><i data-pwn="${f}"></i>" data-pwn="${f}" x="' data-pwn='${f}' x='<img data-pwn="${f}" src=x>`;

async function httpJson(method, path, body, headers) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json', ...(headers || {}) }, body: body === undefined ? undefined : (typeof body === 'string' ? body : JSON.stringify(body)) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

const HOOK = `(() => {
    if (window.__pwnHooked) return; window.__pwnHooked = true;
    const log = window.__pwnSinks = [];
    const realSet = Object.getOwnPropertyDescriptor(Element.prototype, 'innerHTML').set;
    // The string is parsed the way the sink will parse it, in a template of its own (inert: no script, no image loads): a data-pwn
    // attribute on a parsed element means the markup was not escaped. (A quote in a text node is not one.)
    const injected = s => { const t = document.createElement('template'); realSet.call(t, s); const e = t.content.querySelector('[data-pwn]'); return e ? { f: e.getAttribute('data-pwn'), tag: e.tagName } : null; };
    const note = (kind, v) => {
        try {
            const s = typeof v === 'string' ? v : String(v);
            if (s.indexOf('data-pwn') < 0) return;
            const hit = injected(s); if (!hit) return;
            log.push({ kind, field: hit.f, tag: hit.tag, stack: new Error().stack.split('\\n').slice(2, 6).map(l => l.trim().replace(/^at /, '')).join(' | ').slice(0, 420) });
        } catch (e) { /* the hook must never break the page */ }
    };
    // Phase 2 (localStorage.__pwnTaint): every text field an API answer or a live-feed message carries, under a key that names something shown,
    // gets the payload appended, tagged with its own key, so what the dashboard does with ANY server string is tried, not only what a suite
    // could plant. Routing keys (ids, names used in paths, urls, enums) are left alone, or the page could not ask for anything.
    let taintOn = false; try { taintOn = localStorage.getItem('__pwnTaint') === '1'; } catch (e) { /* no storage */ }
    if (taintOn) {
        const SHOWN = /^(title|summary|content|description|label|text|message|body|branch|subject|author|display_name|snippet|prompt|topic|role|job_title|detail|note|reason|error|waiting_summary|goal|commit_subject|excerpt|preview|output|stdout|stderr|first_message|last_message|tool_name|repo_name|agent_name|sender|assignee|assigned_to|created_by|from|hint|warning|status_text|outcome|completion_message|instructions|notes_md|auto_summary|working_mode|team_name|board_label|first_user_message)$/;
        const Q = String.fromCharCode(39);
        const pay = f => '</textarea></title></style></select></a></button><i data-pwn="' + f + '"></i>" data-pwn="' + f + '" x="' + Q + ' data-pwn=' + Q + f + Q + ' x=' + Q + '<img data-pwn="' + f + '" src=x>';
        const taint = (v, key) => {
            if (typeof v === 'string' && key === 'color') return 'red;position:fixed;inset:0;z-index:99999;width:100vw;height:100vh;background:red'; // rows stored before the server checked a colour (#4875)
            if (typeof v === 'string') return SHOWN.test(key || '') && v.length < 6000 ? v + pay('T-' + key) : v;
            if (Array.isArray(v)) return v.map(x => taint(x, key));
            if (v && typeof v === 'object') { const o = {}; for (const k of Object.keys(v)) o[k] = taint(v[k], k); return o; }
            return v;
        };
        const rj = Response.prototype.json; Response.prototype.json = function () { return rj.call(this).then(v => taint(v, '')); };
        const md = Object.getOwnPropertyDescriptor(MessageEvent.prototype, 'data');
        Object.defineProperty(MessageEvent.prototype, 'data', { ...md, get() { const d = md.get.call(this); if (typeof d === 'string' && d.charCodeAt(0) === 123) { try { return JSON.stringify(taint(JSON.parse(d), '')); } catch (e) { return d; } } return d; } });
    }
    for (const [proto, prop] of [[Element.prototype, 'innerHTML'], [Element.prototype, 'outerHTML'], [ShadowRoot.prototype, 'innerHTML']]) {
        const d = Object.getOwnPropertyDescriptor(proto, prop);
        if (!d || !d.set) continue;
        Object.defineProperty(proto, prop, { ...d, set(v) { note(prop, v); return d.set.call(this, v); } });
    }
    const iah = Element.prototype.insertAdjacentHTML; Element.prototype.insertAdjacentHTML = function (p, h) { note('insertAdjacentHTML', h); return iah.call(this, p, h); };
    const ccf = Range.prototype.createContextualFragment; Range.prototype.createContextualFragment = function (h) { note('createContextualFragment', h); return ccf.call(this, h); };
    const pfs = DOMParser.prototype.parseFromString; DOMParser.prototype.parseFromString = function (h, t) { note('parseFromString', h); return pfs.call(this, h, t); };
    const dw = document.write; document.write = function (h) { note('document.write', h); return dw.apply(this, arguments); };
})();`;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, waitStable, coralReady } = require('./helpers/wait').bind(ev);
    const settle = () => waitStable(`document.getElementsByTagName('*').length`, 'the page to settle', 120, { timeout: 4000 }).catch(() => {});
    const planted = [], refused = [];
    // What the suite creates in tables that outlive it (saved messages, tags, jobs, views, workflows) is taken off again in its finally (#4916): by id, through the page's fetch.
    const made = [];
    const track = (path, id) => { if (id && /^\/api\/(saved|tags|scheduled\/jobs|views|workflows)$/.test(path)) made.push(`${path}/${id}`); };
    const plant = async (field, method, path, body, headers) => {
        const r = await httpJson(method, path, body, headers);
        track(path, r.data && r.data.id);
        (r.status >= 200 && r.status < 300 ? planted : refused).push(`${field} (${r.status}${r.status >= 300 ? ' ' + JSON.stringify(r.data).slice(0, 160) : ''})`);
        return r;
    };
    let standIn = null;
    const BOARD = 'pwn-' + Date.now().toString(36);
    const fs = require('fs'), path = require('path'), os = require('os'), cp = require('./helpers/child_process.js');
    // A working folder an agent could have made: a file, a folder and a branch named with markup (a name may hold < > " ' and =, not a slash or a
    // space: the tag is written as <img/data-pwn=.../src=x>), a commit message and a markdown file with the payload in it.
    // Windows cannot hold < > " in a file, folder or branch name (a branch is a file under .git), so there the names carry the same payload as far as
    // the platform allows: a single-quoted attribute written into the name, which is what breaks out of a '-quoted attribute.
    const WIN = process.platform === 'win32';
    const T = WIN ? f => `x' data-pwn='${f}' y='z` : f => `<img data-pwn="${f}" src=x>`;     // a file or folder name may hold a space
    const TB = WIN ? f => `x'/data-pwn='${f}'/y='z` : f => `<img/data-pwn="${f}"/src=x>`;   // a branch name may not: its slashes are ref levels, and HTML reads / like a space
    const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'pwn-repo-'));
    const git = (...a) => cp.execFileSync('git', ['-C', repo, '-c', 'user.name=t', '-c', 'user.email=t@example.invalid', ...a], { stdio: 'pipe' });
    cp.execFileSync('git', ['init', '-q', repo]);
    fs.writeFileSync(path.join(repo, 'notes.md'), P('file-md') + '\n\n# heading\n');
    fs.writeFileSync(path.join(repo, 'tracked' + T('file-name-tracked') + '.txt'), 'one\n');
    fs.mkdirSync(path.join(repo, 'd' + T('dir-name')));
    fs.writeFileSync(path.join(repo, 'd' + T('dir-name'), 'inner.txt'), 'x\n');
    git('add', '-A'); git('commit', '-q', '-m', 'first ' + P('commit-msg'));
    git('checkout', '-q', '-b', (WIN ? "feat'" : 'feat"') + TB('branch-name'));
    fs.writeFileSync(path.join(repo, 'tracked' + T('file-name-tracked') + '.txt'), 'two\n');          // modified
    fs.writeFileSync(path.join(repo, 'untracked' + T('file-name-untracked') + '.txt'), 'new\n');       // untracked
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Injection QA', workingDir: repo });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        const sid = standIn.sid, name = standIn.name, tok = { 'X-Coral-Session-Token': standIn.token };

        // ── plant ──────────────────────────────────────────────────────────────
        await plant('board-job-title', 'POST', `/api/board/${BOARD}/subscribe`, { subscriber_id: 'Lead', job_title: 'Orchestrator ' + S('board-job-title') });
        await plant('board-subscriber-id', 'POST', `/api/board/${BOARD}/subscribe`, { subscriber_id: S('board-subscriber-id'), job_title: 'Dev' });
        await plant('board-message', 'POST', `/api/board/${BOARD}/messages`, { subscriber_id: 'Lead', content: P('board-message') + '\n\n**bold** ' + P('board-message-md') });
        await plant('board-message-by-name', 'POST', `/api/board/${BOARD}/messages`, { subscriber_id: S('board-subscriber-id'), content: 'hello' });
        await plant('board-task', 'POST', `/api/board/${BOARD}/tasks`, { subscriber_id: 'Operator', title: P('board-task-title'), body: P('board-task-body'), assigned_to: S('board-task-assignee'), priority: 'high' });
        await plant('board-task-2', 'POST', `/api/board/${BOARD}/tasks`, { subscriber_id: 'Operator', title: 'second', body: 'x', assigned_to: 'Lead' });
        await plant('agent-display-name', 'PUT', `/api/sessions/live/${encodeURIComponent(name)}/display-name`, { session_id: sid, display_name: P('agent-display-name') });
        await plant('agent-icon', 'PUT', `/api/sessions/live/${encodeURIComponent(name)}/icon`, { session_id: sid, icon: P('agent-icon') });
        await plant('agent-event', 'POST', `/api/sessions/live/${encodeURIComponent(name)}/events`, { session_id: sid, event_type: 'tool_use', tool_name: P('event-tool'), summary: P('event-summary'), detail_json: { x: P('event-detail') } }, tok);
        await plant('agent-event-type', 'POST', `/api/sessions/live/${encodeURIComponent(name)}/events`, { session_id: sid, event_type: P('event-type'), summary: 's' }, tok);
        await plant('agent-note', 'POST', `/api/sessions/live/${encodeURIComponent(name)}/notes`, { session_id: sid, content: P('agent-note') }, tok);
        await plant('personal-task', 'POST', '/api/agent/tasks', { title: P('personal-task-title'), body: P('personal-task-body') }, tok);
        const tag = await plant('tag', 'POST', '/api/tags', { name: P('tag-name'), color: '#336699' });
        if (tag.data && tag.data.id) await plant('folder-tag', 'POST', `/api/folder-tags/${encodeURIComponent(path.basename(repo))}`, { tag_id: tag.data.id });
        // #4875: a colour is stored only as #rgb, #rrggbb or #rrggbbaa (it goes into a style attribute); a CSS payload is a 400.
        const cssRefused = (await plant('tag-colour-css', 'POST', '/api/tags', { name: 'css-colour', color: 'red;position:fixed;inset:0;z-index:99999' })).status;
        const colourOk = (await plant('tag-colour-8hex', 'POST', '/api/tags', { name: 'ok-colour', color: '#12345678' })).status;
        await plant('artifact-name', 'POST', `/api/agent/artifacts?session_id=${sid}`, 'x', { 'X-Artifact-Name': 'a-' + P('artifact-name') + '.txt', ...tok, 'Content-Type': 'text/plain' }).catch(e => refused.push('artifact-name (client refused: ' + e.message.slice(0, 80) + ')'));
        // a markdown task and message through the personal list's other body

        // The Operator-only writes go through the dashboard itself (the page's own fetch: its cookie and origin), as the Operator makes them.
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: HOOK });
        { const loaded0 = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await loaded0; await coralReady().catch(() => {}); }
        const plantPage = async (field, method, path, body) => {
            const r = await ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${JSON.stringify(JSON.stringify(body))} }).then(async r => ({ status: r.status, text: (await r.text()).slice(0, 4000) }))`); // enough to hold the new row's id
            (r.status >= 200 && r.status < 300 ? planted : refused).push(`${field} (${r.status}${r.status >= 300 ? ' ' + r.text.slice(0, 160) : ''})`);
            try { r.id = (JSON.parse(r.text || 'null') || {}).id; } catch { const m = /"id"\s*:\s*"?([0-9A-Za-z_-]+)"?/.exec(r.text || ''); r.id = m && m[1]; } // the text is cut: its first id
            track(path, r.id);
            return r;
        };
        await plantPage('saved-board', 'POST', '/api/saved', { kind: 'board', ref: BOARD, key: '1', agent_type: 'claude', title: P('saved-board-title'), snippet: P('saved-board-snippet'), said_at: P('saved-board-said-at'), author: P('saved-board-author') });
        await plantPage('saved-agent', 'POST', '/api/saved', { kind: 'agent', ref: sid, key: 'a1b2c3d4e5f6a7b8c9d0', agent_type: P('saved-agent-type'), title: P('saved-agent-title'), snippet: P('saved-agent-snippet'), said_at: P('saved-agent-said-at'), team: P('saved-agent-team') });
        await plantPage('scheduled-job', 'POST', '/api/scheduled/jobs', { name: P('job-name'), description: P('job-desc'), cron_expr: '0 3 * * *', timezone: 'UTC', agent_type: 'claude', repo_path: repo, prompt: P('job-prompt'), enabled: 0 });
        await plantPage('custom-view', 'POST', '/api/views', { name: P('view-name'), prompt: P('view-prompt'), html: '<p>x</p>', scope: 'global' });
        await plantPage('workflow', 'POST', '/api/workflows', { name: 'wf-' + Date.now().toString(36), description: P('workflow-desc'), steps: [{ name: 'step one', type: 'shell', command: 'true' }], enabled: 0 });

        // A notification shows as a toast when it arrives, so it is sent with the page open (and again after each page below).
        const notify = () => plant('notification', 'POST', '/api/notifications', { title: P('notif-title'), message: P('notif-message'), level: 'info', link: { label: P('notif-link-label'), url: 'docs://workflow-quickstart' } }, tok);
        await notify();
        await waitFor(`!!document.querySelector('.notification-toast, .alert-notification')`, 'the notification toast', { timeout: 15000, soft: true });
        // ── open the pages ─────────────────────────────────────────────────────
        const wfId = (await ev(`fetch('/api/workflows').then(r => r.json()).then(d => ((d.workflows || d)[0] || {}).id)`).catch(() => null));
        const visits = ['/', `/agents/${sid}`, ...['board', 'tasks', 'notes', 'files', 'browse', 'artifacts', 'activity'].map(s => `/agents/${sid}/${s}`),
            `/board/${BOARD}`, `/team/${BOARD}`, '/jobs', '/workflows', ...(wfId ? [`/workflows/${wfId}`] : []), '/analytics'];

        // The safe controls the dashboard has for looking at an item (opening, expanding, editing without saving, dialogs that only ask), clicked once each:
        // never a delete, kill, sleep, send, run, trigger, publish or "do" action.
        const SAFE = /^(agentic-tab|agentic-panel-toggle|history-tab|jobs-subtab|live-view-mode|goal-edit|chat-find-toggle|task-open|task-edit|task-complete|task-cancel|agent-info|agent-rename|agent-icon|wf-select|wf-run-open|wf-edit|wf-output-toggle|job-select|job-edit|team-details-close|team-enter|team-agent-open|team-agent-edit|team-share|team-tokens|team-working-mode|team-accent|team-availability|history-open|session-open|file-preview|file-open|board-open|board-chat-open|mb-subscriber-history|mb-project|filter-dropdown|kebab-toggle|groups-toggle-all|display-panel-toggle|group-collapse|group-sleeping-expand|cost-open-live|cost-open-history|cost-sort|cost-task|doc-open|custom-view-tab|custom-view-edit|macro-show|launch-show|perms-toggle|perm-chip-toggle|task-subagent-open|task-artifact-open|activity-more|task-load-more|mb-earlier|board-chat-earlier|board-chat-all|history-load-.*|task-sort|folder-tag|session-tag-add|template-browser-close|m-nav-more|m-launch-session|m-launch-team|job-workflow|info-copy|browser-recent|browser-nav|mb-export-agents|board-chat-export|folder-add-agent|team-add-agent|dep-picker-toggle|send-menu-protocol|cmd-mode-toggle|goal-request)$/;
        const crawl = async path => {
            let clicks = 0;
            for (let round = 0; round < 4; round++) {
                const n = await ev(`(() => { const done = window.__crawled || (window.__crawled = new Set()); let n = 0;
                    for (const el of [...document.querySelectorAll('[data-on-click]')]) {
                        const a = el.getAttribute('data-on-click'); if (!${SAFE}.test(a)) continue;
                        const key = location.pathname + '|' + a + '|' + el.id + '|' + [...el.attributes].filter(x => x.name.startsWith('data-') && x.name !== 'data-on-click' && !x.name.startsWith('data-pwn')).map(x => x.name + '=' + x.value).join('&');
                        if (done.has(key)) continue; done.add(key); n++;
                        try { el.click(); } catch (e) { /* a control that throws is not a finding here */ }
                        if (n >= 20) break;
                    } return n; })()`);
                if (!n) break;
                clicks += n;
                if (process.env.PWN_DEBUG) console.log('  crawl round ' + round + ' on ' + path + ': ' + n);
                await settle();
            }
            return clicks;
        };
        const findings = [];
        const seen = new Set();
        const seenT = new Set();
        let pillCount = 0, fallbackPills = 0;
        for (const path of visits) {
            const loaded = Page.loadEventFired();
            await Page.navigate({ url: BASE + path });
            await loaded;
            await coralReady().catch(() => {});
            await settle();
            if (path === `/agents/${sid}/files`) await waitFor(`document.documentElement.outerHTML.includes('file-name-')`, 'the changed files', { timeout: 8000, soft: true });
            if (path === '/') { await ev(`document.getElementById('saved-btn') && document.getElementById('saved-btn').click()`); await settle(); await notify(); await waitFor(`!!document.querySelector('.notification-toast, .alert-notification')`, 'the notification toast', { timeout: 15000, soft: true }); }
            const snap = async () => { for (const f of await ev(`[...document.documentElement.outerHTML.matchAll(/data-pwn=(?:&quot;|[\"'])([A-Za-z0-9_.-]+)/g)].map(m => m[1])`)) seen.add(f); };
            await snap();
            const clicked = await crawl(path);
            await snap();
            const got = await ev(`({ sinks: (window.__pwnSinks || []).filter(s => !/\\/vendor\\//.test(s.stack)), dom: [...document.querySelectorAll('[data-pwn]')].map(e => ({ f: e.getAttribute('data-pwn'), tag: e.tagName, in: (e.parentElement && (e.parentElement.id || e.parentElement.className || e.parentElement.tagName)) + '' })) })`);
            for (const f of await ev(`[...document.documentElement.outerHTML.matchAll(/data-pwn=(?:&quot;|[\"'])([A-Za-z0-9_.-]+)/g)].map(m => m[1])`)) seen.add(f);
            for (const s of got.sinks) findings.push(`${path}: raw ${s.field} in ${s.kind} <- ${s.stack}`);
            for (const d of got.dom) findings.push(`${path}: element [data-pwn=${d.f}] <${d.tag}> in ${String(d.in).slice(0, 60)}`);
            console.log(`visited ${path}: ${clicked} click(s), ${got.sinks.length} raw sink(s), ${got.dom.length} injected element(s)`);
        }

        // The fields an agent controls the most, by what it leaves in its folder, must have been on screen for the check above to mean anything.
        check('positive control: a hostile file and folder name were shown (escaped)', ['dir-name', 'file-name-tracked', 'file-name-untracked'].some(f => seen.has(f)), [...seen].filter(f => /name/.test(f)).join(', '));
        // Phase 2: the same pages with every shown text field of every API answer and feed message tainted.
        await ev(`localStorage.setItem('__pwnTaint', '1'); true`);
        for (const path of visits) {
            const loaded = Page.loadEventFired();
            await Page.navigate({ url: BASE + path });
            await loaded;
            await coralReady().catch(() => {});
            await settle();
            await ev(`document.getElementById('saved-btn') && document.getElementById('saved-btn').click(); true`).catch(() => {});
            const snapT = async () => { for (const f of await ev(`[...document.documentElement.outerHTML.matchAll(/data-pwn=(?:&quot;|[\"'])(T-[A-Za-z0-9_.-]+)/g)].map(m => m[1])`)) seenT.add(f); };
            await snapT();
            const clicked = await crawl('T' + path);
            await snapT();
            const got = await ev(`({ sinks: (window.__pwnSinks || []).filter(s => !/\\/vendor\\//.test(s.stack)), dom: [...document.querySelectorAll('[data-pwn]')].map(e => ({ f: e.getAttribute('data-pwn'), tag: e.tagName, in: (e.parentElement && (e.parentElement.id || e.parentElement.className || e.parentElement.tagName)) + '' })) })`);
            const pills = await ev(`[...document.querySelectorAll('.tag-pill, .sidebar-tag-pill, .folder-tag-full, .tag-dropdown-item')].map(e => { const r = e.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), pos: getComputedStyle(e).position, bg: getComputedStyle(e).backgroundColor }; })`);
            pillCount += pills.length;
            for (const q of pills) {
                if (q.bg === 'rgb(255, 0, 0)' || q.pos === 'fixed') findings.push(`[tainted] ${path}: a tag pill took the CSS from its colour: ${q.w}x${q.h} position ${q.pos}, background ${q.bg}`);
                if (q.bg === 'rgb(88, 166, 255)') fallbackPills++; // the default colour: what a colour that is not a hex colour shows as
            }
            for (const s of got.sinks) findings.push(`[tainted] ${path}: raw ${s.field} in ${s.kind} <- ${s.stack}`);
            for (const d of got.dom) findings.push(`[tainted] ${path}: element [data-pwn=${d.f}] <${d.tag}> in ${String(d.in).slice(0, 60)}`);
            console.log(`tainted ${path}: ${clicked} click(s), ${got.sinks.length} raw sink(s), ${got.dom.length} injected element(s)`);
        }
        await ev(`localStorage.removeItem('__pwnTaint'); true`);

        check('the server refuses a tag colour that is CSS (400) and keeps a #rrggbbaa one (200)', cssRefused === 400 && colourOk === 200, `${cssRefused} and ${colourOk}`);
        check('phase 2 positive control: tag pills were on screen with the tainted (CSS) colour and showed the default colour instead', pillCount >= 3 && fallbackPills >= 3, pillCount + ' pill(s) measured, ' + fallbackPills + ' in the default colour');
        check('phase 2 positive control: tainted fields of many kinds reached the page (as text)', seenT.size >= 12, seenT.size + ' keys: ' + [...seenT].sort().join(', '));
        // positive control: the hook sees a raw sink
        await ev(`(() => { const d = document.createElement('div'); d.innerHTML = '<i data-pwn="control"></i>'; return true; })()`);
        const ctrl = await ev(`(window.__pwnSinks || []).some(s => s.field === 'control')`);
        check('positive control: the hook sees a raw string put through innerHTML', ctrl);
        console.log(`planted ${planted.length}: ${planted.join('; ')}`);
        console.log(`refused by the API (never shown) ${refused.length}: ${refused.join('; ')}`);
        const unseen = planted.map(x => x.split(' ')[0]).filter(f => ![...seen].some(sf => sf === f || sf.startsWith(f + '-') || f.startsWith(sf)));
        console.log('never shown on any visited page (coverage gap or not displayed): ' + unseen.join(', '));
        if (process.env.PWN_REPORT) require('fs').writeFileSync(process.env.PWN_REPORT, ['tainted keys seen: ' + [...seenT].sort().join(', '), 'seen fields: ' + [...seen].sort().join(', '), 'never shown: ' + unseen.join(', '), 'planted ' + planted.length + ': ' + planted.join('; '), 'refused ' + refused.length + ': ' + refused.join('; '), ''].join('\n'));
        const uniq = [...new Set(findings)];
        for (const f of uniq) console.log('FINDING ' + f);
        check('no planted markup reached a sink raw or the page as an element', uniq.length === 0, uniq.length + ' finding(s)');
    } catch (e) {
        check('the suite ran', false, e.message);
    } finally {
        if (standIn) await standIn.stop().catch(() => {});
        for (const u of made) await ev(`fetch(${JSON.stringify(u)}, { method: 'DELETE' }).then(r => r.status)`).catch(() => {});
        try { if (/\/pwn-repo-/.test(repo)) fs.rmSync(repo, { recursive: true, force: true }); } catch { /* best effort */ }
        await removeBoards(ev, [BOARD]);
        await client.close();
    }
    process.exit(results.every(Boolean) ? 0 : 1);
})();
