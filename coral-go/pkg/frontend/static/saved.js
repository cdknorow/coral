/* Save for Later (#4510), the list and the jump back: the Saved tab of the right sidebar (Open and Done, newest saved first), which stays open beside the main
 * view while you go back to a saved message. The buttons
 * on the messages and the top bar's count are in saved_marks.js. An agent still running opens in its Chat view and pages up to the message; one that has ended
 * opens in Chats (History) at the message; a board opens and jumps to the message as the board's search does. A message that is gone (the transcript no longer has
 * it, the board message was deleted) is marked Gone on its item, which keeps its snippet and can only be removed. */
import { state } from './state.js';
import { showToast } from './utils.js';
import { sessionServer, normServer } from './server_base.js';
import { savedChanged, setSavedCount } from './saved_marks.js';
import { effectiveLiveViewMode, setLiveViewMode, jumpToLiveMessage } from './live_chat.js';

const $ = id => document.getElementById(id);
let tab = 'open';
let items = [];
const checks = new Map(); // item id -> { state: 'live' | 'ended' | 'board' | 'gone', why }

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

async function load() {
    const r = await fetch('/api/saved');
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const d = await r.json();
    items = d.items || [];
    $('saved-count-open').textContent = d.open ? `(${d.open})` : '';
    $('saved-count-done').textContent = d.done ? `(${d.done})` : '';
    setSavedCount(d.open || 0);
}

// Is the agent one this server has running now? (a live agent opens in Chat; an ended one in Chats)
function liveAgent(ref) {
    return (state.liveSessions || []).find(s => s.session_id === ref && normServer(sessionServer(s)) === 'local') || null;
}

function srcLabel(it) { return it.title || it.ref; }

function chipFor(it) {
    const c = checks.get(it.id);
    if (c && c.state === 'gone') return { text: 'Gone', cls: 'gone' };
    if (it.kind === 'board') return { text: 'Board', cls: '' };
    if (c ? c.state === 'ended' : !liveAgent(it.ref)) return { text: 'Ended agent', cls: 'ended' };
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
    const chk = checks.get(it.id);
    const gone = chk && chk.state === 'gone';
    const chip = chipFor(it);
    const li = document.createElement('li');
    li.className = `saved-item${gone ? ' gone' : ''}${done ? ' done' : ''}`;
    li.dataset.id = String(it.id);
    const top = document.createElement('div'); top.className = 'top';
    const ic = document.createElement('span'); ic.className = 'material-icons'; ic.setAttribute('aria-hidden', 'true');
    ic.textContent = it.kind === 'board' ? ICON.board : (chip.cls === 'ended' ? ICON.ended : ICON.agent);
    const src = document.createElement('span'); src.className = 'src'; src.textContent = srcLabel(it); src.title = srcLabel(it);
    const kind = document.createElement('span'); kind.className = `kind ${chip.cls}`.trim(); kind.textContent = chip.text;
    top.append(ic, src, kind);
    const snip = document.createElement('p'); snip.className = 'snip'; snip.textContent = it.snippet || '(no text)';
    li.append(top, snip);
    const why = document.createElement('p'); why.className = 'why'; why.setAttribute('role', 'status'); why.hidden = !gone;
    if (gone) {
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
        const o = openLabel(it);
        acts.append(btn('btn-primary saved-open', o.text, o.name, 'launch', () => openItem(it, li)));
        acts.append(btn('saved-done', done ? 'Reopen' : 'Done', done ? 'Move back to Open' : 'Mark done', done ? 'replay' : 'done', () => setDone(it, !done)));
    }
    acts.append(btn('saved-remove', 'Remove', 'Remove from saved', 'close', () => remove(it)));
    li.append(acts);
    return li;
}

function render() {
    const list = $('saved-list');
    const shown = items.filter(i => (tab === 'done') === !!i.done_at);
    list.replaceChildren(...shown.map(itemNode));
    if (!shown.length) {
        const li = document.createElement('li');
        li.className = 'saved-empty';
        li.textContent = tab === 'done' ? 'Nothing marked done.' : 'Nothing saved yet. Use the bookmark on a message to save it for later.';
        list.append(li);
    }
    for (const t of ['open', 'done']) {
        const b = $(`saved-tab-${t}`);
        b.setAttribute('aria-selected', String(t === tab));
        b.tabIndex = t === tab ? 0 : -1;
    }
    $('saved-panel').setAttribute('aria-labelledby', `saved-tab-${tab}`);
    verify(shown);
}

// What each shown item points at, found out when the list opens: running, ended (opens in Chats) or gone. A few at a time; what is found replaces the chip.
let verifyRun = 0;
async function verify(shown) {
    const run = ++verifyRun;
    const todo = shown.filter(i => !checks.has(i.id)).slice(0, 60);
    let next = 0;
    const worker = async () => {
        while (next < todo.length) {
            const it = todo[next++];
            const found = await probe(it).catch(() => null);
            if (run !== verifyRun || !found) continue;
            checks.set(it.id, found);
            swap(it);
        }
    };
    await Promise.all([worker(), worker(), worker()]);
}

/** An item drawn again (its chip, its actions): the focus stays on the same button when it was inside, or moves to the item's first. */
function swap(it) {
    const li = $('saved-list').querySelector(`[data-id="${it.id}"]`);
    if (!li) return;
    const had = li.contains(document.activeElement) ? [...li.querySelectorAll('.btn')].indexOf(document.activeElement) : -2;
    const next = itemNode(it);
    li.replaceWith(next);
    if (had !== -2) (next.querySelectorAll('.btn')[Math.max(had, 0)] || next.querySelector('.btn'))?.focus();
}

async function probe(it) {
    if (it.kind === 'board') {
        const r = await fetch(`/api/board/${encodeURIComponent(it.ref)}/messages/all?id=${Number(it.key)}`);
        if (r.status === 404) return { state: 'gone', why: 'This message was deleted from the board, or the board was.' };
        return r.ok ? { state: 'board' } : null;
    }
    const live = liveAgent(it.ref);
    const url = live ? `/api/sessions/live/${encodeURIComponent(live.name)}/chat?session_id=${encodeURIComponent(it.ref)}&agent_type=${encodeURIComponent(live.agent_type || it.agent_type || '')}&find_key=${encodeURIComponent(it.key)}`
        : `/api/sessions/history/${encodeURIComponent(it.ref)}?find_key=${encodeURIComponent(it.key)}`;
    const r = await fetch(url);
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
    const li = $('saved-list').querySelector(`[data-id="${it.id}"]`);
    return li?.nextElementSibling?.querySelector('.btn') || li?.previousElementSibling?.querySelector('.btn');
}

async function setDone(it, done) {
    const next = neighbour(it);
    const r = await fetch(`/api/saved/${it.id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ done }) });
    if (!r.ok) { showToast('Could not change the saved message', true); return; }
    await refresh();
    status(done ? 'Marked done' : 'Moved back to Open');
    (next && next.isConnected ? next : $(`saved-tab-${tab}`)).focus();
    savedChanged();
}

async function remove(it) {
    const next = neighbour(it);
    const r = await fetch(`/api/saved/${it.id}`, { method: 'DELETE' });
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
    wire();
    checks.clear();
    status('');
    showing = refresh().finally(() => { showing = null; });
    return showing;
}

/** The top bar's Saved button: the right sidebar open on its Saved tab (or the tab focused if it already is), the focus inside it. The sidebar belongs to the agent's
 *  page: from History, a board or the home screen an agent is selected first, so that there is a sidebar to show. */
export async function openSaved() {
    const inAgent = () => document.getElementById('live-session-view') && getComputedStyle($('live-session-view')).display !== 'none';
    if (!inAgent()) {
        const live = (state.liveSessions || []).filter(s => normServer(sessionServer(s)) === 'local');
        const pick = live.find(s => !s.sleeping && !s.archived) || live[0];
        if (!pick) { showToast('Saved opens beside an agent: start or select one first.', true); return; }
        await window.selectLiveSession(pick.name, pick.agent_type, pick.session_id, normServer(sessionServer(pick)));
    }
    dropJumpBar();
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
    back.addEventListener('click', () => { dropJumpBar(); openSaved(); });
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

async function openItem(it, li) {
    try {
        const found = await probe(it);
        if (found && found.state === 'gone') { checks.set(it.id, found); swap(it); $('saved-list').querySelector(`[data-id="${it.id}"] .saved-remove`)?.focus(); return; }
        if (it.kind === 'board') {
            closeOverlay();
            window.selectBoardProject(it.ref, Number(it.key));
            // the board marks and focuses the message when it is drawn; the tag and the bar follow
            for (let i = 0; i < 100; i++) {
                const t = document.querySelector(`#mb-messages [data-message-id="${CSS.escape(it.key)}"].chat-search-target`);
                if (t) { tagTarget(t); showJumpBar($('mb-messages')); break; }
                await new Promise(r => setTimeout(r, 100));
            }
            return;
        }
        const live = liveAgent(it.ref);
        const index = found ? found.index : 0;
        closeOverlay();
        if (live) {
            await window.selectLiveSession(live.name, live.agent_type, live.session_id, normServer(sessionServer(live)));
            if (effectiveLiveViewMode() !== 'chat') setLiveViewMode('chat');
            if (!(await jumpToLiveMessage(it.key, index))) { showToast('That message could not be found any more', true); return; }
            const t = document.querySelector(`#live-history-messages [data-msg-key="${CSS.escape(it.key)}"]`);
            if (t) tagTarget(t);
            showJumpBar($('live-history-messages'));
        } else {
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
