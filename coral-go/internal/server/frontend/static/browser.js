/* Directory browser for session launch modal */

import { escapeHtml, escapeAttr } from './utils.js';

let browserCurrentPath = "~";
// Track which dir-input + browser pair is active
let _activeDirInputId = "launch-dir";
let _activeBrowserId = "dir-browser";
let _activeListId = "browser-list";
let _activePathId = "browser-current-path";

const RECENT_DIRS_KEY = "coral-recent-dirs";
const MAX_RECENT_DIRS = 6;

function getRecentDirs() {
    try {
        const dirs = JSON.parse(localStorage.getItem(RECENT_DIRS_KEY) || "[]");
        return Array.isArray(dirs) ? dirs.filter(d => typeof d === "string") : [];
    } catch (_) {
        return [];
    }
}

/* Remember a working directory an agent was launched into (most recent first) */
export function recordRecentDir(path) {
    path = (path || "").trim();
    if (path.length > 1) path = path.replace(/\/+$/, "");
    if (!path) return;
    const dirs = [path, ...getRecentDirs().filter(d => d !== path)].slice(0, MAX_RECENT_DIRS);
    try {
        localStorage.setItem(RECENT_DIRS_KEY, JSON.stringify(dirs));
    } catch (_) {}
}

function renderRecentDirs() {
    const browser = document.getElementById(_activeBrowserId);
    if (!browser) return;
    let section = browser.querySelector(".dir-browser-recents");
    const dirs = getRecentDirs();
    if (!dirs.length) {
        if (section) section.remove();
        return;
    }
    if (!section) {
        section = document.createElement("div");
        section.className = "dir-browser-recents";
        browser.querySelector(".dir-browser-header").after(section);
    }
    section.innerHTML = `<div class="dir-browser-recents-label">Recent</div>` +
        dirs.map(dir => {
            const name = dir.split("/").filter(Boolean).pop() || dir;
            return `<div class="dir-browser-recent" onclick="browserSelectRecent('${escapeAttr(dir)}')" title="${escapeHtml(dir)}">
                <span class="dir-icon">&#128338;</span>
                <span class="dir-name">${escapeHtml(name)}</span>
                <span class="dir-browser-recent-path">&lrm;${escapeHtml(dir)}</span>
            </div>`;
        }).join("");
}

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
        renderRecentDirs();
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
        const dirInput = document.getElementById(_activeDirInputId);
        dirInput.value = data.path;
        dirInput.dispatchEvent(new Event('change'));

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

export function browserSelectRecent(path) {
    loadBrowserEntries(path);
}

export function browserNavigateUp() {
    const parts = browserCurrentPath.split("/");
    if (parts.length > 1) {
        parts.pop();
        const parent = parts.join("/") || "/";
        loadBrowserEntries(parent);
    }
}
