/* Directory browser for session launch modal */

import { escapeHtml, escapeAttr } from './utils.js';

let browserCurrentPath = "~";
// Track which dir-input + browser pair is active
let _activeDirInputId = "launch-dir";
let _activeBrowserId = "dir-browser";
let _activeListId = "browser-list";
let _activePathId = "browser-current-path";

function getCoralRoot() {
    const input = document.getElementById(_activeDirInputId);
    return (input && input.dataset.coralRoot) || "~";
}

export function toggleBrowser(dirInputId, browserId, listId, pathId) {
    // If called with arguments, use those; otherwise default to agent form
    if (dirInputId) {
        _activeDirInputId = dirInputId;
        _activeBrowserId = browserId;
        _activeListId = listId;
        _activePathId = pathId;
    } else {
        _activeDirInputId = "launch-dir";
        _activeBrowserId = "dir-browser";
        _activeListId = "browser-list";
        _activePathId = "browser-current-path";
    }

    const browser = document.getElementById(_activeBrowserId);
    const isVisible = browser.style.display !== "none";
    browser.style.display = isVisible ? "none" : "";
    if (!isVisible) {
        const inputPath = document.getElementById(_activeDirInputId).value.trim();
        browserCurrentPath = inputPath || getCoralRoot();
        loadBrowserEntries(browserCurrentPath);
    }
}

async function loadBrowserEntries(path) {
    const list = document.getElementById(_activeListId);
    const pathDisplay = document.getElementById(_activePathId);
    list.innerHTML = '<li class="empty-state">Loading...</li>';

    try {
        const resp = await fetch(`/api/filesystem/list?path=${encodeURIComponent(path)}`);
        const data = await resp.json();

        if (data.error) {
            list.innerHTML = `<li class="empty-state">${escapeHtml(data.error)}</li>`;
            return;
        }

        browserCurrentPath = data.path;
        pathDisplay.textContent = data.path;
        document.getElementById(_activeDirInputId).value = data.path;

        if (!data.entries.length) {
            list.innerHTML = '<li class="empty-state">No subdirectories</li>';
            return;
        }

        list.innerHTML = data.entries.map(name =>
            `<li onclick="browserNavigateTo('${escapeAttr(name)}')" title="${escapeHtml(name)}">
                <span class="dir-icon">&#128193;</span>
                <span class="dir-name">${escapeHtml(name)}</span>
            </li>`
        ).join("");
    } catch (e) {
        list.innerHTML = '<li class="empty-state">Failed to load</li>';
        console.error("Browser load error:", e);
    }
}

export function browserNavigateTo(name) {
    const newPath = browserCurrentPath + "/" + name;
    loadBrowserEntries(newPath);
}

export function browserNavigateUp() {
    const parts = browserCurrentPath.split("/");
    if (parts.length > 1) {
        parts.pop();
        const parent = parts.join("/") || "/";
        loadBrowserEntries(parent);
    }
}
