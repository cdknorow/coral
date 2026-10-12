// Board message paging: the message board and the board chat panel page by
// message id (before_id / after_id) when the server supports it, and keep
// offset paging for older servers. Run: node board_keyset_paging.test.mjs
import assert from 'node:assert/strict';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom();
const settle = () => new Promise(r => setTimeout(r, 0));

// A board of 120 messages with ids 1..120, served like the Go handler.
let keysetServer = true;
let board = globalThis.__board = Array.from({ length: 120 }, (_, i) => ({ id: i + 1, content: `m${i + 1}`, job_title: 'Dev', created_at: '2026-10-01T00:00:00Z' }));
const requests = [];
const otherBoard = Array.from({ length: 5 }, (_, i) => ({ id: 1000 + i, content: `o${i}`, job_title: 'QA', created_at: '2026-10-01T00:00:00Z' }));
function serve(url) {
    const u = new URL(url, 'https://hub.example/');
    const q = u.searchParams;
    const board = u.pathname.includes('/other/') ? otherBoard : globalThis.__board;
    const limit = Number(q.get('limit') || 200), offset = Number(q.get('offset') || 0);
    const before = Number(q.get('before_id') || 0);
    const after = q.has('after_id') && keysetServer;
    let msgs;
    if (after) msgs = board.filter(m => m.id > Number(q.get('after_id'))).slice(0, limit);
    else if (before > 0 && keysetServer) msgs = board.filter(m => m.id < before).slice(-limit);
    else if (offset > 0) msgs = board.slice(offset, offset + limit);
    else msgs = board.slice(-limit);
    const body = { messages: msgs, total: board.length, limit, offset };
    if (keysetServer && offset === 0) {
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
let pollTick = null;
const realSetInterval = globalThis.setInterval;
globalThis.setInterval = window.setInterval = (fn, ms) => { if (ms === 10000) { pollTick = fn; return 1; } return realSetInterval(fn, ms); };

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

window.loadEarlierMessages();
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

// ── Message board against an older server (no paging field): offsets as before ──
keysetServer = false;
requests.length = 0;
mb.selectBoardProject('legacy');
for (let i = 0; i < 5; i++) await settle();
assert.deepEqual(renderedIds(), board.slice(-50).map(m => m.id));
window.loadEarlierMessages();
for (let i = 0; i < 3; i++) await settle();
assert.equal(query(requests.at(-1)).get('offset'), '21', 'older servers page by offset');
assert.ok(!query(requests.at(-1)).has('before_id'));
assert.equal(renderedIds().length, 100);
pollTick();
for (let i = 0; i < 3; i++) await settle();
assert.equal(query(requests.at(-1)).get('offset'), '121', 'older servers poll by offset');
assert.ok(!requests.some(r => query(r).has('after_id')));
mb.showMessageBoardProjects();

// ── Poll races: overlap, board switch, poll before the initial load ──
keysetServer = true;
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
keysetServer = true;
requests.length = 0;
const render = await importUi('render');
render.showBoardChatTab('team');
for (let i = 0; i < 6; i++) await settle();
const chatPages = requests.filter(r => query(r).get('limit') !== '1');
assert.equal(chatPages.length, 1);
assert.equal(query(chatPages[0]).get('offset'), '0', 'the chat panel loads its latest page without an OFFSET scan');
window._loadEarlierBoardChat();
for (let i = 0; i < 3; i++) await settle();
assert.equal(query(requests.at(-1)).get('before_id'), String(board.at(-50).id), 'chat panel loads earlier by id');
assert.match(document.getElementById('board-panel-msgs').textContent, new RegExp(`${board.length - 100} remaining`));

// A chat-panel "load earlier" in flight across a board switch is dropped.
holdEarlier = deferred();
window._loadEarlierBoardChat();
await settle();
render.showBoardChatTab('other');
for (let i = 0; i < 6; i++) await settle();
holdEarlier.resolve(); holdEarlier = null;
for (let i = 0; i < 3; i++) await settle();
window._toggleBoardChatSelect(); // re-renders the panel from its message state
const chatText = document.getElementById('board-panel-msgs').textContent;
assert.match(chatText, /o4/);
assert.doesNotMatch(chatText, /\bm1\b|m10\b/, "team's earlier page never lands in another board's chat");

console.log('board_keyset_paging: ok');
process.exit(0);
