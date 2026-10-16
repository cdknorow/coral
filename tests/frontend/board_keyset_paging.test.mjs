// Board message paging: the message board and the board chat panel page by
// message id (before_id / after_id). Run: node board_keyset_paging.test.mjs
import assert from 'node:assert/strict';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom();
const settle = () => new Promise(r => setTimeout(r, 0));

// A board of 120 messages with ids 1..120, served like the Go handler.
let board = globalThis.__board = Array.from({ length: 120 }, (_, i) => ({ id: i + 1, content: `m${i + 1}`, job_title: 'Dev', created_at: '2026-10-01T00:00:00Z' }));
const requests = [];
const otherBoard = Array.from({ length: 5 }, (_, i) => ({ id: 1000 + i, content: `o${i}`, job_title: 'QA', created_at: '2026-10-01T00:00:00Z' }));
function serve(url) {
    const u = new URL(url, 'https://hub.example/');
    const q = u.searchParams;
    const board = u.pathname.includes('/other/') ? otherBoard : globalThis.__board;
    const limit = Number(q.get('limit') || 200), offset = Number(q.get('offset') || 0);
    const before = Number(q.get('before_id') || 0);
    const after = q.has('after_id');
    let msgs;
    if (after) msgs = board.filter(m => m.id > Number(q.get('after_id'))).slice(0, limit);
    else if (before > 0) msgs = board.filter(m => m.id < before).slice(-limit);
    else if (offset > 0) msgs = board.slice(offset, offset + limit);
    else msgs = board.slice(-limit);
    const body = { messages: msgs, total: board.length, limit, offset };
    if (offset === 0) {
        body.paging = 'keyset';
        const anchor = msgs.length ? msgs[0].id : after ? Number(q.get('after_id')) + 1 : before;
        body.before_count = board.filter(m => m.id < anchor).length;
    }
    return body;
}
// While holdPolls is set, after_id requests wait for it (a slow server).
let holdPolls = null;
let holdEarlier = null;
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
globalThis.fetch = window.fetch = async (url) => {
    const s = String(url);
    if (s.includes('/messages/all')) requests.push(s);
    if (holdPolls && s.includes('after_id=')) await holdPolls.promise;
    if (holdEarlier && s.includes('before_id=')) await holdEarlier.promise;
    const body = s.includes('/messages/all') ? serve(s) : s.includes('/subscribers') ? [] : s.includes('/projects') ? [] : {};
    return { ok: true, status: 200, json: async () => structuredClone(body), text: async () => '' };
};
let pollTick = null, chatTick = null;
const realSetInterval = globalThis.setInterval;
globalThis.setInterval = window.setInterval = (fn, ms) => {
    if (ms === 10000) { pollTick = fn; return 1; }
    if (ms === 3000) { chatTick = fn; return 2; } // the board chat panel's poll
    return realSetInterval(fn, ms);
};

const mb = await importUi('message_board');
const renderedIds = () => [...document.querySelectorAll('#mb-messages [data-message-id]')].map(e => Number(e.dataset.messageId));
const query = s => new URL(s, 'https://hub.example/').searchParams;

// ── Message board on a keyset server ──
mb.selectBoardProject('team');
for (let i = 0; i < 5; i++) await settle();
assert.equal(requests.length, 1, 'one request loads the latest page (no separate count call)');
assert.equal(query(requests[0]).get('offset'), '0');
assert.deepEqual(renderedIds(), board.slice(-50).map(m => m.id));
assert.match(document.getElementById('mb-messages').textContent, /70 remaining/);

window.__coral.loadEarlierMessages();
for (let i = 0; i < 3; i++) await settle();
assert.equal(query(requests.at(-1)).get('before_id'), '71', 'load earlier pages by the oldest loaded id');
assert.equal(query(requests.at(-1)).get('offset'), '0');
assert.deepEqual(renderedIds(), board.slice(-100).map(m => m.id));
assert.match(document.getElementById('mb-messages').textContent, /20 remaining/);

board.push({ id: 121, content: 'new', job_title: 'QA', created_at: '2026-10-01T00:01:00Z' });
pollTick();
for (let i = 0; i < 3; i++) await settle();
assert.equal(query(requests.at(-1)).get('after_id'), '120', 'the poll asks for what follows the newest id');
assert.equal(renderedIds().at(-1), 121);
assert.ok(requests.every(r => Number(query(r).get('offset')) === 0), 'no OFFSET scans on a keyset server');

// ── Poll races: overlap, board switch, poll before the initial load ──
const unique = ids => new Set(ids).size === ids.length && ids.every((id, i) => i === 0 || ids[i - 1] < id);
// A poll before the initial page has loaded must not send after_id=0.
requests.length = 0;
const realFetch = window.fetch;
let releaseLoad;
const attempted = [];
globalThis.fetch = window.fetch = async (url) => {
    if (!String(url).includes('/messages/all')) return realFetch(url);
    attempted.push(String(url));
    if (attempted.length === 1) await new Promise(r => { releaseLoad = r; }); // hold the initial load
    return realFetch(url);
};
mb.selectBoardProject('team');
await settle();
pollTick();
await settle();
assert.equal(attempted.length, 1, 'no poll before the initial page is loaded (only the load request)');
globalThis.fetch = window.fetch = realFetch;
releaseLoad();
for (let i = 0; i < 5; i++) await settle();
assert.ok(unique(renderedIds()));
assert.equal(renderedIds().at(-1), board.at(-1).id);

// Overlapping polls on a slow server: one request, each message once.
holdPolls = deferred();
requests.length = 0;
pollTick(); pollTick(); pollTick();
await settle();
assert.equal(requests.filter(r => query(r).has('after_id')).length, 1, 'a poll does not start while one is in flight');
board.push({ id: board.at(-1).id + 1, content: 'late', job_title: 'Dev', created_at: '2026-10-01T00:02:00Z' });
holdPolls.resolve(); holdPolls = null;
for (let i = 0; i < 3; i++) await settle();
pollTick();
for (let i = 0; i < 3; i++) await settle();
assert.ok(unique(renderedIds()), 'no duplicate rows');
assert.equal(renderedIds().filter(id => id === board.at(-1).id).length, 1);

// A poll in flight across a board switch is dropped.
holdPolls = deferred();
pollTick();
await settle();
board.push({ id: board.at(-1).id + 1, content: 'team only', job_title: 'Dev', created_at: '2026-10-01T00:03:00Z' });
mb.selectBoardProject('other');
for (let i = 0; i < 5; i++) await settle();
holdPolls.resolve(); holdPolls = null;
for (let i = 0; i < 3; i++) await settle();
assert.deepEqual(renderedIds(), otherBoard.map(m => m.id), "board A's poll result never lands on board B");
mb.showMessageBoardProjects();

// ── Board chat panel ──
requests.length = 0;
// The panel is part of the agent view, and its poll keeps a slow beat while that view is off
// screen (#3719): the board view above hid it, so show it, as when the panel is in use.
{ const agentView = document.getElementById('live-session-view'); if (agentView) agentView.style.display = ''; }
const render = await importUi('render');
render.showBoardChatTab('team');
for (let i = 0; i < 6; i++) await settle();
const chatPages = requests.filter(r => query(r).get('limit') !== '1');
assert.equal(chatPages.length, 1);
assert.equal(query(chatPages[0]).get('offset'), '0', 'the chat panel loads its latest page without an OFFSET scan');
window.__coral._loadEarlierBoardChat();
for (let i = 0; i < 3; i++) await settle();
assert.equal(query(requests.at(-1)).get('before_id'), String(board.at(-50).id), 'chat panel loads earlier by id');
assert.match(document.getElementById('board-panel-msgs').textContent, new RegExp(`${board.length - 100} remaining`));

// The panel's poll asks only for what follows its newest message (#3722).
const panelIds = () => [...document.querySelectorAll('#board-panel-msgs .mb-message')].length;
const tick = async () => { requests.length = 0; chatTick(); for (let i = 0; i < 6; i++) await settle(); return requests.map(query); };
const shownBefore = panelIds();
let q = await tick();
assert.equal(q.length, 1, 'a quiet poll is one request');
assert.equal(q[0].get('after_id'), String(board.at(-1).id), 'by the newest id shown');
assert.equal(panelIds(), shownBefore, 'and changes nothing');
board.push({ id: board.at(-1).id + 1, content: 'chat new 1', job_title: 'QA', created_at: '2026-10-01T00:04:00Z' },
    { id: board.at(-1).id + 2, content: 'chat new 2', job_title: 'QA', created_at: '2026-10-01T00:04:00Z' });
q = await tick();
assert.equal(q.length, 1, 'new messages come in the same one request');
assert.equal(panelIds(), shownBefore + 2, 'and are added, older pages kept');
assert.match(document.getElementById('board-panel-msgs').textContent, /chat new 2/);
q = await tick();
assert.equal(q[0].get('after_id'), String(board.at(-1).id), 'the next poll starts after them');
// Overlapping ticks on a slow server: one request, each message once.
holdPolls = deferred();
requests.length = 0;
chatTick(); chatTick();
await settle();
assert.equal(requests.length, 1, 'a panel poll does not start while one is in flight');
holdPolls.resolve(); holdPolls = null;
for (let i = 0; i < 6; i++) await settle();
// A deleted message (the total falls short): the latest page is loaded again.
globalThis.__board = board = board.filter(m => m.content !== 'chat new 1');
q = await tick();
assert.deepEqual(q.map(x => x.has('after_id') ? 'after' : 'offset=' + x.get('offset')), ['after', 'offset=0'], 'a deletion reloads the latest page');
assert.doesNotMatch(document.getElementById('board-panel-msgs').textContent, /chat new 1/);
// A deletion and a new message in the same tick (the total unchanged): reloaded too, so the
// deleted one is gone within a poll.
const secret = board.at(-1);
globalThis.__board = board = board.filter(m => m !== secret);
board.push({ id: secret.id + 1, content: 'after the secret', job_title: 'QA', created_at: '2026-10-01T00:04:30Z' });
q = await tick();
assert.deepEqual(q.map(x => x.has('after_id') ? 'after' : 'offset=' + x.get('offset')), ['after', 'offset=0'], 'a deletion hidden by a new message reloads the latest page');
assert.doesNotMatch(document.getElementById('board-panel-msgs').textContent, new RegExp(secret.content));
assert.match(document.getElementById('board-panel-msgs').textContent, /after the secret/);
// A page or more at once: the latest page, not the oldest of the new ones.
const base = board.at(-1).id;
for (let i = 1; i <= 60; i++) board.push({ id: base + i, content: `burst ${i}`, job_title: 'Dev', created_at: '2026-10-01T00:05:00Z' });
q = await tick();
assert.equal(q.at(-1).get('offset'), '0', 'a burst of a page or more reloads the latest page');
assert.match(document.getElementById('board-panel-msgs').textContent, /burst 60/);
// Switching to another agent of the same board shows what it had, and fetches only what's new.
requests.length = 0;
render.showBoardChatTab('team');
for (let i = 0; i < 6; i++) await settle();
assert.deepEqual(requests.map(r => query(r).has('after_id')), [true], 'a same-board switch does not load the page again');
assert.match(document.getElementById('board-panel-msgs').textContent, /burst 60/);

// A chat-panel "load earlier" in flight across a board switch is dropped.
holdEarlier = deferred();
window.__coral._loadEarlierBoardChat();
await settle();
render.showBoardChatTab('other');
for (let i = 0; i < 6; i++) await settle();
holdEarlier.resolve(); holdEarlier = null;
for (let i = 0; i < 3; i++) await settle();
window.__coral._toggleBoardChatSelect(); // re-renders the panel from its message state
const chatText = document.getElementById('board-panel-msgs').textContent;
assert.match(chatText, /o4/);
assert.doesNotMatch(chatText, /\bm1\b|m10\b/, "team's earlier page never lands in another board's chat");

console.log('board_keyset_paging: ok');
process.exit(0);
