/* Save for Later (#4510), the list and the jump back: the Saved tab of the right sidebar (Open and Done, newest saved first), which stays open beside the main
 * view while you go back to a saved message. The buttons
 * on the messages and the top bar's count are in saved_marks.js. An agent still running opens in its Chat view and pages up to the message; one that has ended
 * opens in Chats (History) at the message; a board opens and jumps to the message as the board's search does. A message that is gone (the transcript no longer has
 * it, the board message was deleted) is marked Gone on its item, which keeps its snippet and can only be removed. */
import { state } from './state.js';
import { showToast } from './utils.js';
import { sessionServer, normServer, serverFetch, identityKey, LOCAL_SERVER } from './server_base.js';
import { savedChanged, setSavedCount, setScopedCount, wantAllScope, validSavedItem, savedShowing, savedViewShowing, syncSavedButton, currentScope } from './saved_marks.js';
import { withScope, scopeHeading, scopeEmpty, ALL } from './saved_scope.js';
import { effectiveLiveViewMode, setLiveViewMode, jumpToLiveMessage } from './live_chat.js';
import { buildListBar } from './list_bar.js';
import { importOnce } from './import_once.js';
const { savedExpandFor } = await importOnce('./inbox_coral.js'); // "Read the whole message" on a saved item: the shared Inbox component
import { selectInboxTab } from './inbox_tab.js';
// This module is loaded lazily, so its own parts are loaded the same way (a static import of a module not loaded at startup could not be retried: import_hygiene).
const { KINDS, KIND_LABEL, SORTS, SORT_LABEL, EMPTY: SAVED_EMPTY, makeModel: savedModel, clean: cleanSaved, isFiltered: savedFiltered, toQuery: savedQuery } = await importOnce('./saved_filters.js');

const $ = id => document.getElementById(id);
let tab = 'open';
let items = [];
let down = []; // the registered servers that did not answer the last read: {id, label}
let multi = false; // more than one server answered: each item names its server
const checks = new Map(); // item (uid) -> { state: 'live' | 'ended' | 'board' | 'gone' | 'noopen', why }
// The search and filters (#4778): the server matches (q, kind, conv, order), so a search covers every saved message, not just the ones shown.
let filters = { ...SAVED_EMPTY };
let bar = null;
let totals = { open: 0, done: 0 };
let scope = ALL; // where the panel was opened from (#4792): the agent or the team on screen, or everything; the server matches it, and the counts are its own
const convTitles = new Map(); // conversation reference -> its title, from every list read: the Conversation filter's options (they must outlast a filter that leaves one)
/** An item's identity across servers: ids are each server's own. */
const uid = it => `${it.server}:${it.id}`;

const ICON = { agent: 'smart_toy', board: 'forum', ended: 'history' };

function when(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
}

function ago(iso) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    const min = Math.round((Date.now() - d.getTime()) / 60000);
    if (min < 1) return 'just now';
    if (min < 60) return `${min} min ago`;
    const h = Math.round(min / 60);
    if (h < 24) return `${h} h ago`;
    const days = Math.round(h / 24);
    if (days === 1) return 'yesterday';
    if (days < 14) return `${days} days ago`;
    return when(iso);
}

/** This server's saved messages, and in hub mode every online registered server's (#4551), newest saved first. This server failing is an error; a registered server
 *  that does not answer is a line saying so, and its items are left out. Each item is tagged with the server that has it. */
async function load() {
    const here = currentScope();
    if (here.key !== scope.key) { // another agent or team: its own conversations, and a conversation filter of the last one means nothing
        scope = here;
        convTitles.clear();
        if (filters.conv) filters = { ...filters, conv: '' };
    }
    $('saved-scope').textContent = scopeHeading(scope);
    // A scope belongs to one server: only that one is asked (this computer when it is the hub's own).
    const remotes = scope.kind !== 'all' ? (normServer(scope.server) === LOCAL_SERVER ? [] : [{ id: scope.server, label: serverName(scope.server) }])
        : state.hub ? (state.servers || []).filter(s => s && s.id && s.id !== LOCAL_SERVER && s.status === 'online') : [];
    const read = async (server) => {
        const r = await serverFetch(server, '/api/saved' + withScope(savedQuery(filters), scope));
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        const d = await r.json();
        return { server, d };
    };
    const onlyRemote = scope.kind !== 'all' && normServer(scope.server) !== LOCAL_SERVER;
    const [local, ...others] = await Promise.all([onlyRemote ? Promise.resolve(null) : read(LOCAL_SERVER), ...remotes.map(s => read(s.id).catch(() => ({ server: s.id, failed: true, label: s.label || s.id })))]);
    const answered = [local, ...others.filter(o => !o.failed)].filter(Boolean);
    down = others.filter(o => o.failed).map(o => ({ id: o.server, label: o.label }));
    multi = scope.kind === 'all' && remotes.length > 0;
    // Only what a server sent in a usable shape is listed (an id goes into a URL: saved_marks.js validSavedItem).
    items = answered.flatMap(a => (Array.isArray(a.d.items) ? a.d.items : []).filter(validSavedItem).map(i => ({ ...i, server: a.server })))
        .sort((a, b) => (filters.sort === 'oldest' ? -1 : 1) * (b.saved_at || '').localeCompare(a.saved_at || ''));
    for (const i of items) if (i.ref && !convTitles.has(i.ref)) convTitles.set(i.ref, i.title || i.ref);
    const open = answered.reduce((n, a) => n + (a.d.open || 0), 0), done = answered.reduce((n, a) => n + (a.d.done || 0), 0);
    $('saved-count-open').textContent = open ? `(${open})` : '';
    $('saved-count-done').textContent = done ? `(${done})` : '';
    totals = { open, done };
    if (scope.kind === 'all') { setSavedCount(open); setScopedCount(open); } else setScopedCount(open); // the top bar's number is everything's: an agent's or team's list moves only its own tab's
}

/** The name of a server for people: its label, else its id; "This computer" for the hub itself. */
function serverName(id) {
    if (normServer(id) === LOCAL_SERVER) return 'This computer';
    const s = (state.servers || []).find(x => x && x.id === id);
    return (s && s.label) || id;
}

// Is the agent one this server has running now? (a live agent opens in Chat; an ended one in Chats)
function liveAgent(it) {
    return (state.liveSessions || []).find(s => s.session_id === it.ref && normServer(sessionServer(s)) === normServer(it.server)) || null;
}

function srcLabel(it) { return it.title || it.ref; }
/** Which saved message a button is for: its source and the start of its text, so four items do not read as the same three buttons four times (#6541). */
const nameSuffix = new Map(), openSuffix = new Map(); // uid -> " (2 of 3)" for items shown together whose button names would otherwise match (same source and first words; same source for Open), so each reads apart
function baseName(it) { const t = String(it.snippet || '').replace(/\s+/g, ' ').trim(); const c = [...t]; return t ? `${srcLabel(it)}, ${c.length > 40 ? c.slice(0, 40).join('') + '…' : t}` : srcLabel(it); } // cut by characters, not UTF-16 units, so an emoji is never split
const whichOne = it => baseName(it) + (nameSuffix.get(uid(it)) || '');
/** Work out the suffixes for the items about to be drawn together. */
function nameApart(items) {
    const number = (map, keyOf) => {
        map.clear();
        const seen = new Map(), total = new Map();
        for (const it of items) total.set(keyOf(it), (total.get(keyOf(it)) || 0) + 1);
        for (const it of items) { const k = keyOf(it); if (total.get(k) > 1) { const n = (seen.get(k) || 0) + 1; seen.set(k, n); map.set(uid(it), ` (${n} of ${total.get(k)})`); } }
    };
    number(nameSuffix, baseName);
    number(openSuffix, it => openLabel(it).name);
}

function chipFor(it) {
    const c = checks.get(uid(it));
    if (c && c.state === 'gone') return { text: 'Gone', cls: 'gone' };
    if (it.kind === 'board') return { text: 'Board', cls: '' };
    if (c ? (c.state === 'ended' || c.state === 'noopen') : !liveAgent(it)) return { text: 'Ended agent', cls: 'ended' };
    return { text: 'Agent', cls: '' };
}

function openLabel(it) {
    const c = chipFor(it);
    if (it.kind === 'board') return { text: 'Open board', name: `Open board: ${srcLabel(it)}` };
    if (c.cls === 'ended') return { text: 'Open in Chats', name: `Open in Chats: ${srcLabel(it)}` };
    return { text: 'Open chat', name: `Open chat: ${srcLabel(it)}` };
}

function btn(cls, label, name, icon, onClick) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = `btn ${cls}`.trim();
    b.setAttribute('aria-label', name);
    const i = document.createElement('span'); i.className = 'material-icons'; i.setAttribute('aria-hidden', 'true'); i.textContent = icon;
    b.append(i, document.createTextNode(label));
    b.addEventListener('click', onClick);
    return b;
}

function itemNode(it) {
    const done = !!it.done_at;
    const chk = checks.get(uid(it));
    const gone = chk && chk.state === 'gone';
    const noopen = chk && chk.state === 'noopen'; // kept, but this hub cannot open it
    const chip = chipFor(it);
    const li = document.createElement('li');
    li.className = `saved-item${gone ? ' gone' : ''}${done ? ' done' : ''}`;
    li.dataset.id = uid(it);
    const top = document.createElement('div'); top.className = 'top';
    const ic = document.createElement('span'); ic.className = 'material-icons'; ic.setAttribute('aria-hidden', 'true');
    ic.textContent = it.kind === 'board' ? ICON.board : (chip.cls === 'ended' ? ICON.ended : ICON.agent);
    const src = document.createElement('span'); src.className = 'src'; src.textContent = srcLabel(it); src.title = srcLabel(it);
    const kind = document.createElement('span'); kind.className = `kind ${chip.cls}`.trim(); kind.textContent = chip.text;
    top.append(ic, src, kind);
    if (multi) { const sv = document.createElement('span'); sv.className = 'server'; sv.textContent = serverName(it.server); sv.title = sv.textContent; top.append(sv); }
    const snip = document.createElement('p'); snip.className = 'snip'; snip.textContent = it.snippet || '(no text)';
    li.append(top, snip);
    const why = document.createElement('p'); why.className = 'why'; why.setAttribute('role', 'status'); why.hidden = !(gone || noopen);
    if (gone || noopen) {
        const w = document.createElement('span'); w.className = 'material-icons'; w.setAttribute('aria-hidden', 'true'); w.textContent = 'error_outline';
        const t = document.createElement('span'); t.textContent = chk.why;
        why.append(w, t);
    }
    li.append(why);
    const meta = document.createElement('div'); meta.className = 'meta';
    const said = document.createElement('span'); said.textContent = it.said_at ? `Said ${when(it.said_at)}` : '';
    const saved = document.createElement('span'); saved.textContent = `Saved ${ago(it.saved_at)}`;
    if (it.said_at) meta.append(said, Object.assign(document.createElement('span'), { textContent: '·' }), saved); else meta.append(saved);
    meta.children[1]?.setAttribute('aria-hidden', 'true');
    li.append(meta);
    const acts = document.createElement('div'); acts.className = 'acts';
    if (!gone) {
        const o = openLabel(it); o.name += openSuffix.get(uid(it)) || '';
        if (!noopen) acts.append(btn('btn-primary saved-open', o.text, o.name, 'launch', () => openItem(it, li)));
        acts.append(btn('saved-done', done ? 'Reopen' : 'Done', `${done ? 'Reopen, move back to Open' : 'Mark done'}: ${whichOne(it)}`, done ? 'replay' : 'done', () => setDone(it, !done)));
    }
    acts.append(btn('saved-remove', 'Remove', `Remove from saved: ${whichOne(it)}`, 'close', () => remove(it)));
    li.append(acts);
    // "Read the whole message": the shared Inbox component's control (inbox_panel.js), inline under the snippet; not for an item whose source is gone.
    if (!gone) { const x = savedExpandFor(it, whichOne(it)); if (x && !x.root.hidden) li.insertBefore(x.root, why); }
    return li;
}

/** One line for a server that did not answer: its items are left out, not an error. */
function noticeNode(s) {
    const li = document.createElement('li');
    li.className = 'saved-note';
    li.setAttribute('role', 'status');
    li.textContent = `${serverName(s.id)} did not answer, so its saved messages are not shown.`;
    return li;
}

const convLabel = ref => convTitles.get(ref) || ref;
const savedSpec = () => ({
    idp: 'sv', noun: 'message', label: 'Search and filter saved messages', searchLabel: 'Search saved messages', placeholder: 'Search saved messages',
    sorts: { values: SORTS, labels: SORT_LABEL, aria: 'Sort saved messages' },
    fields: [
        { type: 'select', key: 'kind', id: 'sv-kind', label: 'Type', values: KINDS, labels: KIND_LABEL, any: 'Any type' },
        { type: 'select', key: 'conv', id: 'sv-conv', label: 'From', values: [], labels: {}, any: 'Any conversation' },
    ],
    model: savedModel({ convLabel }),
});

/** The bar above the list, told the filters, the conversations there are and how many of the tab's messages match (the tab's own total, which a search does not change). */
function syncBar(shownCount) {
    const host = $('saved-bar');
    if (!host) return;
    if (!bar) { bar = buildListBar(savedSpec(), next => { filters = cleanSaved(next); refresh(); }); host.append(bar.node); }
    const refs = [...convTitles.keys()].sort((a, b) => convLabel(a).localeCompare(convLabel(b)));
    if (filters.conv && !refs.includes(filters.conv)) refs.push(filters.conv);
    bar.setOptions('conv', refs, Object.fromEntries(refs.map(r => [r, convLabel(r)])));
    bar.showField('conv', refs.length > 1 || !!filters.conv);
    const all = tab === 'done' ? totals.done : totals.open;
    const counts = { matched: shownCount, all: Math.max(all, shownCount) };
    // The Inbox's empty state says what to do; a second zero-message label adds nothing. Keep filtered counts.
    if (scope.kind === 'all' && counts.all === 0 && !savedFiltered(filters)) counts.text = '';
    bar.set(filters, counts);
}

function render() {
    const list = $('saved-list');
    const shown = items.filter(i => (tab === 'done') === !!i.done_at);
    syncBar(shown.length);
    verify(shown); // before the items are drawn: their chips and actions read the marks
    nameApart(shown);
    list.replaceChildren(...down.map(noticeNode), ...shown.map(itemNode));
    if (!shown.length) {
        const li = document.createElement('li');
        li.className = 'saved-empty';
        li.textContent = savedFiltered(filters) ? 'No saved messages match this search.' : scopeEmpty(scope, tab);
        list.append(li);
    }
    for (const t of ['open', 'done']) {
        const b = $(`saved-tab-${t}`);
        b.setAttribute('aria-selected', String(t === tab));
        b.tabIndex = t === tab ? 0 : -1;
    }
    $('saved-panel').setAttribute('aria-labelledby', `saved-tab-${tab}`);
}

// What each shown item points at, read from the list's own answer (#5802): the server marks a bookmark whose message is deleted or whose transcript is not on its computer, so the
// tab asks nothing per item. Only a click (Open) asks the source, in probe().
const GONE_WHY = {
    board: 'This message was deleted from the board, or the board was.',
    gone: 'The transcript no longer has this message, so there is nothing to jump to. The saved text above is all that is left.',
    not_reachable: 'The transcript is gone (the agent was removed), so there is nothing to jump to. The saved text above is all that is left.',
};
function verify(shown) {
    for (const it of shown) {
        if (it.reason === 'gone' || it.reason === 'not_reachable') {
            checks.set(uid(it), { state: 'gone', why: GONE_WHY[it.kind === 'board' ? 'board' : it.reason] });
        } else if (it.kind !== 'board' && !liveAgent(it) && normServer(it.server) !== LOCAL_SERVER) {
            // An ended agent's transcript is in the History of the server that had it, which this hub cannot browse for another server: the item is kept, not opened.
            checks.set(uid(it), { state: 'noopen', why: `This agent has ended. Its history is on ${serverName(it.server)}, and Chats shows only this computer's, so the message cannot be opened from here. The saved text above is kept.` });
        }
    }
}

/** An item drawn again (its chip, its actions): the focus stays on the same button when it was inside, or moves to the item's first. */
function swap(it) {
    const li = $('saved-list').querySelector(`[data-id="${uid(it)}"]`);
    if (!li) return;
    const had = li.contains(document.activeElement) ? [...li.querySelectorAll('.btn')].indexOf(document.activeElement) : -2;
    const next = itemNode(it);
    li.replaceWith(next);
    if (had !== -2) (next.querySelectorAll('.btn')[Math.max(had, 0)] || next.querySelector('.btn'))?.focus();
}

async function probe(it) {
    if (it.kind === 'board') {
        const r = await serverFetch(it.server, `/api/board/${encodeURIComponent(it.ref)}/messages/all?id=${Number(it.key)}`);
        if (r.status === 404) return { state: 'gone', why: 'This message was deleted from the board, or the board was.' };
        return r.ok ? { state: 'board' } : null;
    }
    const live = liveAgent(it);
    // An ended agent's transcript is in the History of the server that had it, which this hub cannot browse for another server (Chats is this computer's): the item is kept, not opened.
    if (!live && normServer(it.server) !== LOCAL_SERVER) return { state: 'noopen', why: `This agent has ended. Its history is on ${serverName(it.server)}, and Chats shows only this computer's, so the message cannot be opened from here. The saved text above is kept.` };
    const url = live ? `/api/sessions/live/${encodeURIComponent(live.name)}/chat?session_id=${encodeURIComponent(it.ref)}&agent_type=${encodeURIComponent(live.agent_type || it.agent_type || '')}&find_key=${encodeURIComponent(it.key)}`
        : `/api/sessions/history/${encodeURIComponent(it.ref)}?find_key=${encodeURIComponent(it.key)}`;
    const r = await serverFetch(live ? it.server : LOCAL_SERVER, url);
    if (r.status === 404) return { state: 'gone', why: 'The transcript is gone (the agent was removed), so there is nothing to jump to. The saved text above is all that is left.' };
    if (!r.ok) return null;
    const d = await r.json();
    if (!d.found) return { state: 'gone', why: 'The transcript no longer has this message, so there is nothing to jump to. The saved text above is all that is left.' };
    return { state: live ? 'live' : 'ended', index: d.index };
}

async function refresh() {
    try { await load(); render(); } catch (e) { $('saved-status').textContent = `Could not read the saved messages (${e.message}).`; }
}

/** On a phone the side panel is an overlay over the chat: it gives way so the jump is seen (the tab is where it was when it is opened again). */
function closeOverlay() { if (window.innerWidth <= 767) window.toggleAgenticPanel?.(false); }

function status(text) { $('saved-status').textContent = ''; requestAnimationFrame(() => { $('saved-status').textContent = text; }); }

function neighbour(it) {
    const li = $('saved-list').querySelector(`[data-id="${uid(it)}"]`);
    return li?.nextElementSibling?.querySelector('.btn') || li?.previousElementSibling?.querySelector('.btn');
}

async function setDone(it, done) {
    const next = neighbour(it);
    const r = await serverFetch(it.server, `/api/saved/${it.id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ done }) });
    if (!r.ok) { showToast('Could not change the saved message', true); return; }
    await refresh();
    status(done ? 'Marked done' : 'Moved back to Open');
    (next && next.isConnected ? next : $(`saved-tab-${tab}`)).focus();
    savedChanged();
}

async function remove(it) {
    const next = neighbour(it);
    const r = await serverFetch(it.server, `/api/saved/${it.id}`, { method: 'DELETE' });
    if (!r.ok && r.status !== 404) { showToast('Could not remove the saved message', true); return; }
    await refresh();
    status('Removed from saved');
    (next && next.isConnected ? next : $(`saved-tab-${tab}`)).focus();
    savedChanged();
}

let wired = false;
function wire() {
    if (wired) return;
    wired = true;
    // Another agent or team on screen while the list is open: the list is theirs.
    document.addEventListener('coral:route-changed', () => { if (savedShowing() && currentScope().key !== scope.key) { checks.clear(); refresh(); } });
    for (const t of ['open', 'done']) $(`saved-tab-${t}`).addEventListener('click', () => { tab = t; render(); });
    // Arrow keys move between the two tabs.
    $('saved-tab-open').parentElement.addEventListener('keydown', (e) => {
        if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
        tab = tab === 'open' ? 'done' : 'open';
        render();
        $(`saved-tab-${tab}`).focus();
        e.preventDefault();
    });
}

/** The Saved tab was shown (by a click on it, a key, or the top bar's button): read the list again. */
let showing = null;
export function showSaved() {
    if (showing) return showing; // the tab's switch and the top bar's button can both ask at once
    placeUi('sidebar'); // the right sidebar's Saved tab: the one list is there (it may have been the full-screen page's)
    wire();
    checks.clear();
    status('');
    showing = refresh().finally(() => { showing = null; });
    return showing;
}

// ── Where the one list is shown (#4811) ──────────────────────────────
// The list (#saved-ui) is a single component: it lives in the right sidebar's Saved tab (an agent's or a team's page keeps that, scoped), and is moved into the full-screen
// Saved page (#saved-view) while that is shown, for the scope "all". It is never shown in both.
function placeUi(where) {
    const ui = $('saved-ui'), host = $(where === 'view' ? 'saved-view' : 'agentic-panel-saved');
    if (ui && host && ui.parentElement !== host) host.append(ui);
}

let _prevTab = 'agents'; // the nav tab the full-screen page was opened from: Escape and the top bar's button return there
const activeNavTab = () => document.querySelector('.top-nav-tab.active')?.dataset.arg || 'agents';

/** The dashboard's own Inbox was shown (switchNavTab('hub-inbox')): the Saved list is moved into it and read. */
let viewLoad = null;
export function showSavedView() {
    if (viewLoad) return viewLoad; // the nav tab's switch and the top bar's button can both ask at once
    placeUi('view');
    wire();
    checks.clear();
    status('');
    viewLoad = refresh().finally(() => { viewLoad = null; });
    return viewLoad;
}
/** Left the full-screen page: the list goes back to the right sidebar. */
export function leaveSavedView() { placeUi('sidebar'); }

/** The Inbox at its Saved part: from the top bar's button on any page where the scope is all. A press while it shows goes back to where it was opened from. */
async function openSavedView({ toggle = false } = {}) {
    if (savedViewShowing()) {
        const onSaved = !$('inbox-panel-saved')?.hidden;
        if (toggle && onSaved) { closeSaved(); return; }
        if (!onSaved) selectInboxTab('saved'); // the Inbox is open on Needs you: the button brings its Saved tab up
        $('saved-list').querySelector('.btn')?.focus();
        return;
    }
    dropJumpBar();
    _prevTab = activeNavTab() === 'hub-inbox' ? _prevTab : activeNavTab();
    wantAllScope(true); // the Inbox is everything's, whatever agent or team is behind it
    selectInboxTab('saved'); // before the switch: the Inbox opens on its Saved tab
    window.switchNavTab?.('hub-inbox');
    try { await showSavedView(); } finally { wantAllScope(false); }
    $('saved-view')?.scrollIntoView({ block: 'start' }); // the Inbox at its Saved part
    ($('saved-list').querySelector('.btn') || $('saved-tab-open')).focus();
}

let _before = null; // {open, tab}: the dock as it was when the Saved button opened it
function dockOpen() {
    const dock = $('agentic-state');
    return !!dock && (window.innerWidth <= 767 ? dock.classList.contains('mobile-panel-overlay') : !dock.classList.contains('collapsed'));
}
/** Close the sidebar the Saved button opened: the tab the agent's dock had is shown again if it was open, the sidebar is shut if it was not. The focus goes back to the button. */
export function closeSaved({ focus = true } = {}) {
    if (savedViewShowing()) { // the full-screen page: back to the page it was opened from
        window.switchNavTab?.(_prevTab && _prevTab !== 'hub-inbox' ? _prevTab : 'agents');
        if (focus) $('saved-btn')?.focus();
        return true;
    }
    if (!savedShowing()) return false;
    const before = _before; _before = null;
    if (before && before.open && before.tab && before.tab !== 'saved') window.switchAgenticTab?.(before.tab, 'top');
    else window.toggleAgenticPanel?.(false);
    syncSavedButton();
    if (focus) $('saved-btn')?.focus();
    return true;
}

/** The top bar's Saved button (#4905): always the Inbox at its Saved part, everything's list and number, on every page: an agent's, a team's, home. A second press (or Escape) goes back to the page it
 *  was opened from. The right sidebar's Saved tab, scoped to the page, is the dock's own. */
export function openSavedAll({ toggle = false } = {}) { return openSavedView({ toggle }); }

/** The right sidebar's Saved tab: the right sidebar of the page on screen open on its Saved tab (or the tab focused if it already is), the focus inside it. Every page has the
 *  sidebar and the tab (#4539), so nothing has to be selected first. */
export async function openSaved({ toggle = false } = {}) {
    if (currentScope().kind === 'all') return openSavedView({ toggle }); // never an all-scope list in the right sidebar (#4811)
    if (toggle && savedShowing()) { closeSaved(); return; } // the top bar's button is a toggle: pressed, it closes the sidebar (#4742); the jump bar's Back to Saved and the toast's link only open
    dropJumpBar();
    // What the sidebar was before, so closing leaves an agent's own dock as it was: open on its tab, or shut.
    if (!savedShowing()) _before = { open: dockOpen(), tab: document.querySelector('#agentic-block-top .agentic-tab.active')?.dataset.tab || '' }; // opening it again (the toast's link) keeps what the dock was before the first open
    const btn = $('agentic-tab-saved');
    btn?.classList.remove('tab-hidden'); // a tab the user hid is still what this button opens
    window.toggleAgenticPanel?.(true);
    window.switchAgenticTab?.('saved', 'top'); // refreshes through showSaved()
    btn?.scrollIntoView({ block: 'nearest', inline: 'nearest' }); // the tab row scrolls sideways on a phone: the tab shown is in view
    tab = 'open';
    wire();
    await showSaved();
    ($('saved-list').querySelector('.btn') || $('saved-tab-open')).focus();
}

// ── The jump ──────────────────────────────────────────────────────────

/** A slim bar above the conversation after a jump: where you are, and a way back to the list. */
let _jumpFrom = 'sidebar'; // where the last jump started: the full-screen page ('view') or the right sidebar's list
function dropJumpBar() { document.getElementById('saved-jumpbar')?.remove(); }
function showJumpBar(container) {
    dropJumpBar();
    if (!container || !container.parentElement) return;
    const bar = document.createElement('div');
    bar.id = 'saved-jumpbar';
    bar.className = 'jumpbar';
    bar.setAttribute('role', 'status');
    const t = document.createElement('span'); t.textContent = 'Jumped to a saved message.';
    const back = document.createElement('button'); back.type = 'button'; back.className = 'btn btn-small'; back.textContent = 'Back to Saved';
    back.addEventListener('click', () => { dropJumpBar(); if (_jumpFrom === 'view') openSavedView(); else openSaved(); }); // back to the list the jump started from
    const x = document.createElement('button'); x.type = 'button'; x.className = 'btn btn-small'; x.setAttribute('aria-label', 'Dismiss'); x.textContent = '×';
    x.addEventListener('click', dropJumpBar);
    bar.append(t, back, x);
    container.parentElement.insertBefore(bar, container);
    setTimeout(() => { if (bar.isConnected) bar.remove(); }, 60000);
}

/** The message the jump landed on carries the "Saved" tag (and the search-target outline the other jumps use). */
function tagTarget(el) {
    for (const old of document.querySelectorAll('.saved-jump')) old.classList.remove('saved-jump');
    el.classList.add('saved-jump');
}

/** The jump a link makes (#4596, saved_link.js): the same as the Saved tab's Open. A message that is gone says so in a toast, and the Saved tab is the place to see what is left of it. */
export async function openSavedLink(it) {
    try {
        const found = await probe(it);
        if (found && found.state === 'gone') { showToast(found.why, true); return false; }
        if (found && found.state === 'noopen') { showToast(found.why, true); return false; }
    } catch (e) {
        showToast(`Could not open the saved message: ${e.message}`, true);
        return false;
    }
    await openItem(it, null);
    return true;
}

async function openItem(it, li) {
    _jumpFrom = savedViewShowing() ? 'view' : 'sidebar';
    try {
        const found = await probe(it);
        if (found && found.state === 'gone') { checks.set(uid(it), found); swap(it); $('saved-list').querySelector(`[data-id="${uid(it)}"] .saved-remove`)?.focus(); return; }
        const leave = tab => { if (savedViewShowing()) window.switchNavTab?.(tab); }; // from the full-screen page: the tab of where the item is, so the page is left (and the list goes back to the sidebar)
        if (it.kind === 'board') {
            closeOverlay();
            leave('board');
            window.selectBoardProject(identityKey(it.server, it.ref), Number(it.key));
            // the board marks and focuses the message when it is drawn; the tag and the bar follow
            for (let i = 0; i < 100; i++) {
                const t = document.querySelector(`#mb-messages [data-message-id="${CSS.escape(it.key)}"].chat-search-target`);
                if (t) { tagTarget(t); showJumpBar($('mb-messages')); break; }
                await new Promise(r => setTimeout(r, 100));
            }
            return;
        }
        const live = liveAgent(it);
        const index = found ? found.index : 0;
        closeOverlay();
        if (live) {
            leave('agents');
            await window.selectLiveSession(live.name, live.agent_type, live.session_id, normServer(sessionServer(live)));
            if (effectiveLiveViewMode() !== 'chat') setLiveViewMode('chat');
            if (!(await jumpToLiveMessage(it.key, index))) { showToast('That message could not be found any more', true); return; }
            const t = document.querySelector(`#live-history-messages [data-msg-key="${CSS.escape(it.key)}"]`);
            if (t) tagTarget(t);
            showJumpBar($('live-history-messages'));
        } else {
            leave('history');
            await window.selectHistorySession(it.ref, { message_index: index });
            for (let i = 0; i < 100; i++) {
                const t = document.querySelector(`#history-messages [data-msg-key="${CSS.escape(it.key)}"]`);
                if (t && document.querySelector('#history-messages .chat-search-target')) { tagTarget(document.querySelector('#history-messages .chat-search-target')); showJumpBar($('history-messages')); break; }
                await new Promise(r => setTimeout(r, 100));
            }
        }
    } catch (e) {
        showToast(`Could not open the saved message: ${e.message}`, true);
    }
}
