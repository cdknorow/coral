import { filterState } from './search_filters.js';
import { escapeHtml } from './utils.js';

let requestGeneration = 0;

// The search endpoint uses complete/partial/unavailable. Keep this check
// shared and explicit so a successful response is never rendered as an error.
export function isSearchResponseUsable(status) {
    return status === 'complete' || status === 'partial';
}

function utf16Slice(text, start, end) {
    const chars = Array.from(String(text));
    let units = 0, out = '';
    for (const ch of chars) {
        const next = units + ch.length;
        if (next > start && units < end) out += ch;
        units = next;
        if (units >= end) break;
    }
    return out;
}

export function highlightExcerpt(text, offsets = []) {
    const source = String(text || '');
    const ranges = offsets.filter(r => Array.isArray(r) && r.length >= 2)
        .map(r => [Math.max(0, Number(r[0]) || 0), Math.max(0, Number(r[1]) || 0)])
        .filter(([a, b]) => b > a).sort((a, b) => a[0] - b[0]);
    if (!ranges.length) return escapeHtml(source);
    let html = '', cursor = 0;
    for (const [start, end] of ranges) {
        const a = Math.max(cursor, start), b = Math.max(a, end);
        if (a > cursor) html += escapeHtml(utf16Slice(source, cursor, a));
        html += `<mark>${escapeHtml(utf16Slice(source, a, b))}</mark>`;
        cursor = b;
    }
    return html + escapeHtml(utf16Slice(source, cursor, Number.MAX_SAFE_INTEGER));
}

function resultLabel(result) {
    return result.type === 'group'
        ? (result.project || result.session_id || 'Message board')
        : (result.session_id || 'Agent conversation');
}

function renderResult(result, index) {
    const hits = Array.isArray(result.hits) ? result.hits : [];
    const wrapper = document.createElement('section');
    wrapper.className = 'chat-search-result';
    wrapper.setAttribute('aria-labelledby', `chat-search-result-${index}`);
    wrapper.innerHTML = `<div class="chat-search-result-header"><strong id="chat-search-result-${index}">${escapeHtml(resultLabel(result))}</strong><span>${escapeHtml(result.type || 'agent')} · ${escapeHtml(result.last_timestamp || '')}</span></div>`;
    for (const hit of hits) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'chat-search-hit';
        button.dataset.sessionId = hit.locator?.session_id || result.session_id || '';
        button.dataset.project = hit.locator?.project || result.project || '';
        button.setAttribute('aria-label', `${hit.role || 'message'} ${hit.timestamp || ''}: ${hit.excerpt || ''}`);
        button.innerHTML = `<span class="chat-search-hit-meta">${escapeHtml(hit.role || 'message')} · ${escapeHtml(hit.timestamp || '')}</span><span class="chat-search-hit-excerpt">${highlightExcerpt(hit.excerpt, hit.match_offsets)}</span>`;
        button.addEventListener('click', () => openSearchHit(result, hit));
        button.addEventListener('keydown', e => {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); button.click(); }
        });
        wrapper.appendChild(button);
    }
    return wrapper;
}

async function openSearchHit(result, hit) {
    const locator = hit.locator || {};
    if (result.type === 'group' || locator.project) {
        if (window.selectBoardProject) window.selectBoardProject(locator.project || result.project, locator.message_id);
        return;
    }
    if (locator.session_id && window.selectHistorySession) {
        await window.selectHistorySession(locator.session_id, locator);
    }
}

export async function searchChats(query, page = 1) {
    const container = document.getElementById('chat-search-results');
    const list = document.getElementById('history-sessions-list');
    if (!container || !list) return;
    const q = String(query || '').trim();
    if (!q) { container.hidden = true; list.hidden = false; return; }
    container.hidden = false; list.hidden = true;
    container.replaceChildren(Object.assign(document.createElement('div'), { className: 'loading-indicator', textContent: 'Searching chats…' }));
    const generation = ++requestGeneration;
    try {
        const params = new URLSearchParams({ q, page: String(page), page_size: '20', type: filterState.chatType || 'all' });
        const response = await fetch(`/api/sessions/history/search?${params}`);
        if (!response.ok) throw new Error(`Search failed (${response.status})`);
        const data = await response.json();
        if (generation !== requestGeneration) return;
        container.replaceChildren();
        if (data.status && !isSearchResponseUsable(data.status)) {
            container.textContent = 'Search is temporarily unavailable.';
            return;
        }
        if (!data.results?.length) { container.textContent = 'No matching chats.'; return; }
        if (data.status === 'partial') {
            const note = document.createElement('div'); note.className = 'chat-search-status';
            note.textContent = 'Some chat sources are unavailable.'; container.appendChild(note);
        }
        data.results.forEach((result, i) => container.appendChild(renderResult(result, i)));
    } catch (error) {
        if (generation !== requestGeneration) return;
        container.textContent = error.message || 'Search failed.';
    }
}
