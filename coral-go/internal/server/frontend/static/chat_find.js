// Find-in-conversation for the live chat view (Cmd/Ctrl+F). Matches are marked
// with the CSS Custom Highlight API, so the transcript DOM is never modified
// and the live poll re-rendering messages cannot strip the marks.
import { chatHasOlder, loadMoreHistory } from './live_chat.js';

const MAX_MATCHES = 2000;
const MAX_OLDER_PAGES = 100;

let bar = null;
let ranges = [];
let current = -1;
let observer = null;
let refreshTimer = null;
let loadingOlder = false;

function container() { return document.getElementById('live-history-messages'); }

function chatVisible() {
    const c = container();
    return !!c && c.offsetParent !== null;
}

function ensureBar() {
    if (bar) return bar;
    bar = document.createElement('div');
    bar.className = 'chat-find-bar';
    bar.hidden = true;
    bar.setAttribute('role', 'search');
    bar.innerHTML = `
        <input type="text" class="chat-find-input" placeholder="Find in conversation" aria-label="Find in conversation" spellcheck="false">
        <span class="chat-find-count" aria-live="polite"></span>
        <button type="button" class="chat-find-older" hidden title="Load the rest of the conversation so older messages are searched too">Search older</button>
        <button type="button" class="chat-find-prev" title="Previous match (Shift+Enter)" aria-label="Previous match">&#x2191;</button>
        <button type="button" class="chat-find-next" title="Next match (Enter)" aria-label="Next match">&#x2193;</button>
        <button type="button" class="chat-find-close" title="Close (Esc)" aria-label="Close find">&times;</button>`;
    document.body.appendChild(bar);
    const input = bar.querySelector('.chat-find-input');
    input.addEventListener('input', () => runSearch(true));
    input.addEventListener('keydown', e => {
        if (e.key === 'Enter') { e.preventDefault(); step(e.shiftKey ? -1 : 1); }
        else if (e.key === 'Escape') { e.preventDefault(); closeFind(); }
    });
    bar.querySelector('.chat-find-prev').onclick = () => step(-1);
    bar.querySelector('.chat-find-next').onclick = () => step(1);
    bar.querySelector('.chat-find-close').onclick = closeFind;
    bar.querySelector('.chat-find-older').onclick = loadAllOlder;
    return bar;
}

function setFindBtn(on) {
    document.getElementById('chat-find-btn')?.setAttribute('aria-pressed', String(on));
}

function placeBar() {
    const c = container();
    if (!c || !bar) return;
    const r = c.getBoundingClientRect();
    bar.style.top = `${Math.max(r.top + 8, 8)}px`;
    bar.style.right = `${Math.max(window.innerWidth - r.right + 20, 8)}px`;
}

function collectRanges(query) {
    const c = container();
    const out = [];
    if (!c || !query) return out;
    const needle = query.toLowerCase();
    const walker = document.createTreeWalker(c, NodeFilter.SHOW_TEXT, {
        acceptNode(n) {
            if (!n.nodeValue || !n.nodeValue.trim()) return NodeFilter.FILTER_REJECT;
            const p = n.parentElement;
            if (p && p.closest('.load-more-btn, script, style')) return NodeFilter.FILTER_REJECT;
            return NodeFilter.FILTER_ACCEPT;
        },
    });
    for (let n = walker.nextNode(); n && out.length < MAX_MATCHES; n = walker.nextNode()) {
        const hay = n.nodeValue.toLowerCase();
        for (let i = hay.indexOf(needle); i !== -1 && out.length < MAX_MATCHES; i = hay.indexOf(needle, i + needle.length)) {
            const r = document.createRange();
            r.setStart(n, i);
            r.setEnd(n, i + needle.length);
            out.push(r);
        }
    }
    return out;
}

function paint() {
    if (typeof CSS === 'undefined' || !CSS.highlights || typeof Highlight === 'undefined') return;
    CSS.highlights.delete('chat-find');
    CSS.highlights.delete('chat-find-current');
    if (!ranges.length) return;
    CSS.highlights.set('chat-find', new Highlight(...ranges));
    if (current >= 0) CSS.highlights.set('chat-find-current', new Highlight(ranges[current]));
}

function updateCount(query) {
    const count = bar.querySelector('.chat-find-count');
    const older = bar.querySelector('.chat-find-older');
    if (!query) count.textContent = '';
    else if (!ranges.length) count.textContent = 'No results';
    else count.textContent = `${current + 1} of ${ranges.length}${ranges.length >= MAX_MATCHES ? '+' : ''}`;
    older.hidden = !query || !chatHasOlder();
    older.disabled = loadingOlder;
    older.textContent = loadingOlder ? 'Loading…' : 'Search older';
    bar.classList.toggle('no-results', !!query && !ranges.length);
}

function reveal(range) {
    const el = range.startContainer.parentElement;
    if (!el) return;
    for (let d = el.closest('details'); d; d = d.parentElement && d.parentElement.closest('details')) d.open = true;
    el.scrollIntoView({ block: 'center' });
}

function runSearch(jump) {
    if (!bar || bar.hidden) return;
    const query = bar.querySelector('.chat-find-input').value;
    const prev = current >= 0 ? ranges[current] : null;
    ranges = collectRanges(query);
    if (!ranges.length) current = -1;
    else if (jump || !prev) current = 0;
    else {
        // Keep the same match selected across a re-render when it still exists.
        const idx = ranges.findIndex(r => r.startContainer === prev.startContainer && r.startOffset === prev.startOffset);
        current = idx >= 0 ? idx : Math.min(current, ranges.length - 1);
    }
    paint();
    updateCount(query);
    if (jump && current >= 0) reveal(ranges[current]);
}

function step(dir) {
    if (!ranges.length) return;
    current = (current + dir + ranges.length) % ranges.length;
    paint();
    updateCount(bar.querySelector('.chat-find-input').value);
    reveal(ranges[current]);
}

async function loadAllOlder() {
    if (loadingOlder) return;
    loadingOlder = true;
    updateCount(bar.querySelector('.chat-find-input').value);
    try {
        for (let i = 0; i < MAX_OLDER_PAGES && chatHasOlder(); i++) await loadMoreHistory();
    } finally {
        loadingOlder = false;
        runSearch(false);
    }
}

export function openFind() {
    if (!chatVisible()) return false;
    ensureBar();
    bar.hidden = false;
    setFindBtn(true);
    placeBar();
    const input = bar.querySelector('.chat-find-input');
    const sel = String(window.getSelection() || '').trim();
    if (sel && sel.length < 200 && !sel.includes('\n')) input.value = sel;
    input.focus();
    input.select();
    if (!observer) {
        observer = new MutationObserver(() => {
            clearTimeout(refreshTimer);
            refreshTimer = setTimeout(() => runSearch(false), 250);
        });
    }
    observer.observe(container(), { childList: true, subtree: true, characterData: true });
    runSearch(true);
    return true;
}

export function closeFind() {
    if (!bar) return;
    bar.hidden = true;
    setFindBtn(false);
    ranges = [];
    current = -1;
    if (observer) observer.disconnect();
    if (typeof CSS !== 'undefined' && CSS.highlights) {
        CSS.highlights.delete('chat-find');
        CSS.highlights.delete('chat-find-current');
    }
}

export function toggleChatFind() {
    if (bar && !bar.hidden) closeFind();
    else openFind();
}

export function initChatFind() {
    window.toggleChatFind = toggleChatFind;
    window.closeChatFind = closeFind;
    document.addEventListener('keydown', e => {
        if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'f' && chatVisible()) {
            // Leave the terminal's own shortcuts alone.
            if (e.target.closest && e.target.closest('#xterm-container')) return;
            if (openFind()) e.preventDefault();
        }
    });
    window.addEventListener('resize', placeBar);
    // Switching agents or leaving the chat view drops the search.
    window.addEventListener('hashchange', closeFind);
}
