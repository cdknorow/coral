/* Session tags: CRUD, tag pills, dropdown picker */

import { state } from './state.js';
import { escapeHtml } from './utils.js';
import { showToast } from './utils.js';

let allTags = [];
let currentSessionTags = [];

export async function loadAllTags() {
    try {
        const resp = await fetch("/api/tags");
        allTags = await resp.json();
    } catch (e) {
        console.error("Failed to load tags:", e);
        allTags = [];
    }
    // Expose cache for cross-module access (folder_tags.js)
    window._allTagsCache = allTags;
    return allTags;
}

export async function loadSessionTags(sessionId) {
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tags`);
        currentSessionTags = await resp.json();
        renderSessionTags(currentSessionTags);
    } catch (e) {
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
        `<span class="tag-pill" style="background:${escapeHtml(tag.color)}">
            ${escapeHtml(tag.name)}
            <span class="tag-remove" onclick="removeTagFromSession('${escapeHtml(state.currentSession.name)}', ${tag.id})">&times;</span>
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
        container.innerHTML = '<span style="font-size:12px;color:var(--text-muted)">No tags available — create one below</span>';
        return;
    }

    container.innerHTML = available.map(tag =>
        `<span class="tag-dropdown-item" style="background:${escapeHtml(tag.color)}"
              onclick="addTagToSession('${escapeHtml(state.currentSession.name)}', ${tag.id})">
            ${escapeHtml(tag.name)}
        </span>`
    ).join("");
}

export async function addTagToSession(sessionId, tagId) {
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tags`, {
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
        showToast("Failed to add tag", true);
    }
}

export async function removeTagFromSession(sessionId, tagId) {
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tags/${tagId}`, {
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
        tags.map(t => `<span class="sidebar-tag-pill" style="background:${escapeHtml(t.color)}" title="${escapeHtml(t.name)}"></span>`).join("") +
        '</span>';
}
