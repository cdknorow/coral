import { historyFetch } from './history_source.js';
/* Session tags: CRUD, tag pills, dropdown picker */

import { state } from './state.js';
import { escapeHtml, escapeAttr, tagColor } from './utils.js';
import { registerActions, idOf, textOf } from './actions.js';
import { showToast } from './utils.js';

let allTags = [];
let currentSessionTags = [];

export async function loadAllTags(forHistory = false) {
    try {
        const resp = await (forHistory ? historyFetch("/api/tags") : fetch("/api/tags"));
        allTags = await resp.json();
    } catch (e) {
        if (e.name === 'AbortError') return null;
        console.error("Failed to load tags:", e);
        allTags = [];
    }
    // Expose cache for cross-module access (folder_tags.js)
    if (!forHistory) window._allTagsCache = allTags;
    return allTags;
}

export async function loadSessionTags(sessionId) {
    try {
        const resp = await historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tags`);
        currentSessionTags = await resp.json();
        renderSessionTags(currentSessionTags);
    } catch (e) {
        if (e.name === 'AbortError') return null;
        console.error("Failed to load session tags:", e);
        currentSessionTags = [];
        renderSessionTags([]);
    }
}

export function renderSessionTags(tags) {
    const container = document.getElementById("history-session-tags");
    if (!container) return;

    if (!tags || tags.length === 0) {
        container.innerHTML = "";
        return;
    }

    container.innerHTML = tags.map(tag =>
        `<span class="tag-pill" style="background:${escapeAttr(tagColor(tag.color))}">
            ${escapeHtml(tag.name)}
            <span class="tag-remove" data-history-local-only data-on-click="session-tag-remove" data-name="${escapeAttr(state.currentSession.name)}" data-tag-id="${Number(tag.id)}">&times;</span>
        </span>`
    ).join("");
}

export async function showTagDropdown() {
    await loadAllTags();
    const dropdown = document.getElementById("tag-dropdown");
    dropdown.style.display = "block";
    renderTagDropdownList();
}

export function hideTagDropdown() {
    document.getElementById("tag-dropdown").style.display = "none";
}

function renderTagDropdownList() {
    const container = document.getElementById("tag-dropdown-list");
    const sessionTagIds = new Set(currentSessionTags.map(t => t.id));

    // Show tags not already on this session
    const available = allTags.filter(t => !sessionTagIds.has(t.id));

    if (available.length === 0) {
        container.innerHTML = '<span style="font-size:0.75rem;color:var(--text-muted)">No tags available — create one below</span>';
        return;
    }

    container.innerHTML = available.map(tag =>
        `<span class="tag-dropdown-item" style="background:${escapeAttr(tagColor(tag.color))}"
              data-on-click="session-tag-add" data-name="${escapeAttr(state.currentSession.name)}" data-tag-id="${Number(tag.id)}">
            ${escapeHtml(tag.name)}
        </span>`
    ).join("");
}

export async function addTagToSession(sessionId, tagId) {
    try {
        const resp = await historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tags`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ tag_id: tagId }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }
        // Reload tags
        await loadSessionTags(sessionId);
        renderTagDropdownList();
        showToast("Tag added");
    } catch (e) {
        if (e.name === 'AbortError') return null;
        showToast("Failed to add tag", true);
    }
}

export async function removeTagFromSession(sessionId, tagId) {
    try {
        const resp = await historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tags/${tagId}`, {
            method: "DELETE",
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }
        // Reload tags
        await loadSessionTags(sessionId);
        showToast("Tag removed");
    } catch (e) {
        if (e.name === 'AbortError') return null;
        showToast("Failed to remove tag", true);
    }
}

export async function createTag() {
    const nameInput = document.getElementById("new-tag-name");
    const colorInput = document.getElementById("new-tag-color");
    const name = nameInput.value.trim();
    const color = colorInput.value;

    if (!name) {
        showToast("Tag name is required", true);
        return;
    }

    try {
        const resp = await fetch("/api/tags", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ name, color }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }

        nameInput.value = "";
        await loadAllTags();
        renderTagDropdownList();
        showToast(`Tag "${name}" created`);
    } catch (e) {
        if (e.name === 'AbortError') return null;
        showToast("Failed to create tag", true);
    }
}

/**
 * Render small tag dot indicators in sidebar session items.
 * Called from render.js when tags data is available.
 */
export function renderSidebarTagDots(tags) {
    if (!tags || tags.length === 0) return "";
    return '<span class="sidebar-tag-pills">' +
        tags.map(t => `<span class="sidebar-tag-pill" style="background:${escapeAttr(tagColor(t.color))}" title="${escapeHtml(t.name)}"></span>`).join("") +
        '</span>';
}

// A session's tag chip (remove) and the tag dropdown (add) (#3645): the session name arrives intact
// as one argument (data-name), the tag id is checked.
registerActions({
    'session-tag-remove': el => { const n = textOf(el.dataset.name), id = idOf(el.dataset.tagId); if (n && id) window.removeTagFromSession(n, id); },
    'session-tag-add': el => { const n = textOf(el.dataset.name), id = idOf(el.dataset.tagId); if (n && id) window.addTagToSession(n, id); },
});
