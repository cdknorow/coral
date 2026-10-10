/* Folder-level tags: load, render, dropdown picker, add/remove */

import { state } from './state.js';
import { escapeHtml, escapeAttr, showToast } from './utils.js';
import { renderLiveSessions } from './render.js';
import { loadAllTags } from './tags.js';

let allFolderTags = {};

export async function loadAllFolderTags() {
    try {
        const resp = await fetch("/api/folder-tags");
        allFolderTags = await resp.json();
    } catch (e) {
        console.error("Failed to load folder tags:", e);
        allFolderTags = {};
    }
}

export function getFolderTags(folderName) {
    return allFolderTags[folderName] || [];
}

export function renderFolderTagPills(tags) {
    if (!tags || tags.length === 0) return "";
    if (tags.length <= 2) {
        return tags.map(t =>
            `<span class="folder-tag-full" style="background:${escapeHtml(t.color)}">${escapeHtml(t.name)}</span>`
        ).join("");
    }
    return '<span class="sidebar-tag-pills">' +
        tags.map(t => `<span class="sidebar-tag-pill" style="background:${escapeHtml(t.color)}" title="${escapeHtml(t.name)}"></span>`).join("") +
        '</span>';
}

export async function showFolderTagDropdown(folderName, anchorEl) {
    hideFolderTagDropdown();
    await loadAllTags();

    const rect = anchorEl.getBoundingClientRect();
    const dropdown = document.createElement("div");
    dropdown.className = "folder-tag-dropdown";
    dropdown.id = "folder-tag-dropdown";
    dropdown.style.left = (rect.right + 8) + "px";
    dropdown.style.top = rect.top + "px";

    const tags = getFolderTags(folderName);
    dropdown.innerHTML = _buildDropdownHtml(folderName, tags);
    document.body.appendChild(dropdown);

    // Reposition if overflows viewport bottom
    const ddRect = dropdown.getBoundingClientRect();
    if (ddRect.bottom > window.innerHeight - 8) {
        dropdown.style.top = Math.max(8, window.innerHeight - ddRect.height - 8) + "px";
    }

    // Close on outside click
    setTimeout(() => {
        document.addEventListener("click", _onOutsideClick);
    }, 0);
}

function _onOutsideClick(e) {
    const dd = document.getElementById("folder-tag-dropdown");
    if (dd && !dd.contains(e.target)) {
        hideFolderTagDropdown();
    }
}

export function hideFolderTagDropdown() {
    const dd = document.getElementById("folder-tag-dropdown");
    if (dd) dd.remove();
    document.removeEventListener("click", _onOutsideClick);
}

function _buildDropdownHtml(folderName, currentTags) {
    const escapedFolder = escapeAttr(folderName);

    let currentHtml = "";
    if (currentTags.length > 0) {
        currentHtml = `<div class="folder-tag-current">${currentTags.map(t =>
            `<span class="tag-pill" style="background:${escapeHtml(t.color)}">
                ${escapeHtml(t.name)}
                <span class="tag-remove" onclick="event.stopPropagation(); removeFolderTag('${escapedFolder}', ${t.id})">&times;</span>
            </span>`
        ).join("")}</div>`;
    }

    // Get all available tags from the tags module cache
    const allTags = _getAllTagsFromCache();
    const currentIds = new Set(currentTags.map(t => t.id));
    const available = allTags.filter(t => !currentIds.has(t.id));

    let availableHtml = "";
    if (available.length > 0) {
        availableHtml = `<div class="tag-dropdown-list">${available.map(t =>
            `<span class="tag-dropdown-item" style="background:${escapeHtml(t.color)}"
                  onclick="event.stopPropagation(); addFolderTag('${escapedFolder}', ${t.id})">
                ${escapeHtml(t.name)}
            </span>`
        ).join("")}</div>`;
    } else {
        availableHtml = '<div class="tag-dropdown-list"><span style="font-size:12px;color:var(--text-muted)">No tags available</span></div>';
    }

    const createHtml = `<div class="tag-dropdown-create">
        <input type="text" id="folder-tag-new-name" placeholder="New tag name" onkeydown="if(event.key==='Enter'){event.stopPropagation(); createAndAddFolderTag('${escapedFolder}')}" />
        <input type="color" id="folder-tag-new-color" value="#58a6ff" />
        <button class="btn btn-small" onclick="event.stopPropagation(); createAndAddFolderTag('${escapedFolder}')">Add</button>
    </div>`;

    return `<div class="tag-dropdown-header">Folder Tags</div>
        ${currentHtml}
        ${availableHtml}
        ${createHtml}`;
}

function _getAllTagsFromCache() {
    // Access the allTags array from the tags module via a fetch-if-needed approach
    // Since loadAllTags was called in showFolderTagDropdown, we rely on the DOM-based
    // approach or re-fetch. For simplicity, we use a synchronous fetch from the module.
    return window._allTagsCache || [];
}

export async function addFolderTag(folderName, tagId) {
    try {
        const resp = await fetch(`/api/folder-tags/${encodeURIComponent(folderName)}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ tag_id: tagId }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }
        await loadAllFolderTags();
        renderLiveSessions(state.liveSessions);
        // Re-open dropdown to show updated state
        const header = _findGroupHeader(folderName);
        if (header) await showFolderTagDropdown(folderName, header);
        showToast("Tag added");
    } catch (e) {
        showToast("Failed to add tag", true);
    }
}

export async function removeFolderTag(folderName, tagId) {
    try {
        const resp = await fetch(`/api/folder-tags/${encodeURIComponent(folderName)}/${tagId}`, {
            method: "DELETE",
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }
        await loadAllFolderTags();
        renderLiveSessions(state.liveSessions);
        // Re-open dropdown to show updated state
        const header = _findGroupHeader(folderName);
        if (header) await showFolderTagDropdown(folderName, header);
        showToast("Tag removed");
    } catch (e) {
        showToast("Failed to remove tag", true);
    }
}

export async function createAndAddFolderTag(folderName) {
    const nameInput = document.getElementById("folder-tag-new-name");
    const colorInput = document.getElementById("folder-tag-new-color");
    const name = nameInput ? nameInput.value.trim() : "";
    const color = colorInput ? colorInput.value : "#58a6ff";

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
        // Now add the new tag to this folder
        await addFolderTag(folderName, result.id);
    } catch (e) {
        showToast("Failed to create tag", true);
    }
}

function _findGroupHeader(folderName) {
    const headers = document.querySelectorAll(".session-group-header");
    for (const h of headers) {
        const nameEl = h.querySelector(".session-group-name");
        if (nameEl && nameEl.textContent.trim().startsWith(folderName)) return h;
    }
    return null;
}
