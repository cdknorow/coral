/* Directory browser for session launch modal */

import { escapeHtml, escapeAttr } from './utils.js';
import { registerActions, textOf } from './actions.js';
import { serverFetch, LOCAL_SERVER, normServer } from './server_base.js';
import { recentDirsKey, machineBlock } from './launch_server.js';
import { browserLocal } from './safe_storage.js';

/* Multi-server hub: a directory input may belong to a remote server. The
 * launch modals register a resolver (input id -> server id) so listing,
 * git checks and the "recent" list all go to / come from that server. With no
 * resolver (hub off) everything is local, exactly as before. */
let _dirServerResolver = null;
export function setDirServerResolver(fn) { _dirServerResolver = typeof fn === 'function' ? fn : null; }
function activeServer() {
    return normServer(_dirServerResolver ? _dirServerResolver(_activeDirInputId) : LOCAL_SERVER);
}

let browserCurrentPath = "~";
// Track which dir-input + browser pair is active
let _activeDirInputId = "launch-dir";
let _activeBrowserId = "dir-browser";
let _activeListId = "browser-list";
let _activePathId = "browser-current-path";

const MAX_RECENT_DIRS = 6;

export function getRecentDirs(server = LOCAL_SERVER) {
    try {
        const dirs = JSON.parse(browserLocal.get(recentDirsKey(server)) || "[]");
        return Array.isArray(dirs) ? dirs.filter(d => typeof d === "string") : [];
    } catch (_) {
        return [];
    }
}

/* Remember a working directory an agent was launched into (most recent first) */
export function recordRecentDir(path, server = LOCAL_SERVER) {
    path = (path || "").trim();
    if (path.length > 1) path = path.replace(/\/+$/, "");
    if (!path) return;
    const dirs = [path, ...getRecentDirs(server).filter(d => d !== path)].slice(0, MAX_RECENT_DIRS);
    try {
        browserLocal.set(recentDirsKey(server), JSON.stringify(dirs));
    } catch (_) {}
}

function renderRecentDirs() {
    const browser = document.getElementById(_activeBrowserId);
    if (!browser) return;
    let section = browser.querySelector(".dir-browser-recents");
    const dirs = getRecentDirs(activeServer());
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
            return `<div class="dir-browser-recent" data-on-click="browser-recent" role="button" tabindex="0" data-key-click data-dir="${escapeAttr(dir)}" title="${escapeAttr(dir)}">
                <span class="dir-icon">&#128338;</span>
                <span class="dir-name">${escapeHtml(name)}</span>
                <span class="dir-browser-recent-path">&lrm;${escapeHtml(dir)}</span>
            </div>`;
        }).join("");
}

function getCoralRoot() {
    // The hub's coral root is a hub-local path; a remote starts at its own home,
    // asked for as an empty path: the machine lists its home for that. It answers
    // "~" (never its absolute home, which names the account), and "~/folder" is
    // what a click asks for next: the hub tunnel takes that form on this route
    // (pkg/tunnel homeRef, #4527); any other "~" path is refused (safeRef).
    if (activeServer() !== LOCAL_SERVER) return "";
    const input = document.getElementById(_activeDirInputId);
    return (input && input.dataset.coralRoot) || "~";
}

/** Where Browse starts: the folder typed in the input, else the coral root. On a
 *  remote "~" and "~/folder" (what an earlier Browse put in the input) cross the hub
 *  tunnel (#4527); any other "~..." (a "~user") doesn't, so it starts at the remote's
 *  home instead (#3491). */
export function browseStartPath(inputPath, remote, coralRoot) {
    const typed = (inputPath || '').trim();
    if (remote) return typed.startsWith('~') && typed !== '~' && !typed.startsWith('~/') ? '' : typed;
    return typed || coralRoot;
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
        const inputPath = document.getElementById(_activeDirInputId).value;
        browserCurrentPath = browseStartPath(inputPath, activeServer() !== LOCAL_SERVER, getCoralRoot());
        renderRecentDirs();
        loadBrowserEntries(browserCurrentPath);
    }
}

async function loadBrowserEntries(path) {
    const list = document.getElementById(_activeListId);
    const pathDisplay = document.getElementById(_activePathId);
    list.innerHTML = '<li class="empty-state">Loading...</li>';

    const server = activeServer();
    // Coral Hub with no machine connected has no folders to browse: say what to do, not "Failed to load".
    if (machineBlock()) { list.innerHTML = `<li class="empty-state">${machineBlock()}</li>`; return; }
    try {
        const resp = await serverFetch(server, `/api/filesystem/list?path=${encodeURIComponent(path)}`);
        const data = await resp.json();
        // The input may have been re-pointed at another server while we waited;
        // never write a path from one server into a field now owned by another.
        if (server !== activeServer()) return;

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
            `<li data-on-click="browser-nav" role="button" tabindex="0" data-key-click data-name="${escapeAttr(name)}" title="${escapeAttr(name)}">
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
    // A recent folder is a choice, not a step on the way: the list closes once it is in the field.
    loadBrowserEntries(path).then(browserUse);
}

/** "Use this folder": the field already holds the folder the list is showing; close the list so what is under it (More options) has the room (#6199). */
export function browserUse() {
    const browser = document.getElementById(_activeBrowserId);
    if (browser) browser.style.display = "none"; // display-ok: the closed state toggleBrowser sets, the list is shown and hidden by its inline display
    document.getElementById(_activeDirInputId)?.focus();
}

export function browserNavigateUp() {
    const parts = browserCurrentPath.split("/");
    if (parts.length > 1) {
        parts.pop();
        const parent = parts.join("/") || "/";
        loadBrowserEntries(parent);
    }
}

// The folder browser's recent folders and its entries (#3645).
registerActions({
    'launch-dir-recent': el => { const d = textOf(el.dataset.dir, 4096), i = document.getElementById('launch-dir'); if (d && i) { i.value = d; i.dispatchEvent(new Event('input', { bubbles: true })); document.getElementById('launch-agent-next')?.focus(); } },
    'browser-recent': el => { const d = textOf(el.dataset.dir, 4096); if (d) window.browserSelectRecent(d); },
    'browser-nav': el => { const n = textOf(el.dataset.name, 1024); if (n) window.browserNavigateTo(n); },
});
