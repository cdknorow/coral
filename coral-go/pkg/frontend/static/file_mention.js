/* @file mention autocomplete for the command input textarea */

import { state, sessionKey } from './state.js';
import { escapeHtml } from './utils.js';
import { serverFetch, currentServer } from './server_base.js';

let dropdown = null;
let selectedIndex = 0;
let currentResults = [];
let mentionStart = -1; // position of the '@' character

// Client-side file list cache (fetched once per session, refreshed on TTL)
let cachedFiles = [];       // string[]
let cachedSessionKey = null;
let cachedTimestamp = 0;
const CACHE_TTL_MS = 60_000;
let fetchPromise = null;    // dedup concurrent fetches
let renderTimer = null;     // 30ms render throttle

// Directory browsing state
let currentDir = '';        // current directory path for directory browsing mode
let dirCache = {};          // cached directory listings: { dir: { entries, timestamp } }
const DIR_CACHE_TTL_MS = 30_000;

export function initFileMention() {
    const input = document.getElementById("command-input");
    if (!input) return;

    // Create dropdown element
    dropdown = document.createElement("div");
    dropdown.className = "file-mention-dropdown";
    dropdown.style.display = "none";
    // Append to the command-editor wrapper so it positions relative to the textarea
    input.parentElement.appendChild(dropdown);

    input.addEventListener("input", onInput);
    input.addEventListener("keydown", onKeydown);
    input.addEventListener("blur", () => {
        // Delay hide so click on dropdown item can fire first
        setTimeout(hideDropdown, 200);
    });
}

export async function fetchFileList() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        return [];
    }

    const key = sessionKey(state.currentSession);
    const now = Date.now();

    // Return cached if fresh and same session
    if (cachedFiles.length > 0 && cachedSessionKey === key && (now - cachedTimestamp) < CACHE_TTL_MS) {
        return cachedFiles;
    }

    // Dedup concurrent fetches
    if (fetchPromise) return fetchPromise;

    fetchPromise = (async () => {
        try {
            const name = encodeURIComponent(state.currentSession.name);
            const params = new URLSearchParams();
            if (state.currentSession.session_id) {
                params.set("session_id", state.currentSession.session_id);
            }
            const resp = await serverFetch(currentServer(), `/api/sessions/live/${name}/search-files?${params}`);
            if (!resp.ok) return cachedFiles;
            const data = await resp.json();
            cachedFiles = data.files || [];
            cachedSessionKey = key;
            cachedTimestamp = Date.now();
            return cachedFiles;
        } catch {
            return cachedFiles;
        } finally {
            fetchPromise = null;
        }
    })();

    return fetchPromise;
}

/** Fetch directory entries for directory browsing mode.
 *  Returns array of { name, type } where type is 'dir' or 'file'. */
export async function fetchDirEntries(dir) {
    const now = Date.now();
    const cacheKey = dir || '.';
    const cached = dirCache[cacheKey];
    if (cached && (now - cached.timestamp) < DIR_CACHE_TTL_MS) {
        return cached.entries;
    }

    if (!state.currentSession || state.currentSession.type !== "live") return [];

    try {
        const name = encodeURIComponent(state.currentSession.name);
        const params = new URLSearchParams();
        if (state.currentSession.session_id) {
            params.set("session_id", state.currentSession.session_id);
        }
        params.set("dir", dir || ".");
        const resp = await serverFetch(currentServer(), `/api/sessions/live/${name}/search-files?${params}`);
        if (!resp.ok) return cached?.entries || [];
        const data = await resp.json();
        const entries = data.entries || [];
        dirCache[cacheKey] = { entries, timestamp: Date.now() };
        return entries;
    } catch {
        return cached?.entries || [];
    }
}

function fuzzyMatch(text, query, original) {
    // Walk text left-to-right matching query chars in order
    // Returns a score (lower is better) or null if no match
    // `original` is the un-lowered filepath for boundary detection
    let qi = 0;
    let score = 0;
    let lastMatchPos = -1;

    // Basename bonus: find where filename starts
    const slashIdx = original.lastIndexOf('/');
    const basenameStart = slashIdx >= 0 ? slashIdx + 1 : 0;
    const basename = text.slice(basenameStart);
    const boundaryChars = '/_-.';

    // Strong bonus for exact basename match
    if (basename === query) return -10000;
    // Bonus for basename starting with query
    if (basename.startsWith(query)) score -= 500;
    // Bonus for basename containing query as substring
    else if (basename.includes(query)) score -= 200;

    for (let ti = 0; ti < text.length && qi < query.length; ti++) {
        if (text[ti] === query[qi]) {
            // Bonus for consecutive matches
            if (lastMatchPos === ti - 1) {
                score -= 3;
            }
            // Bonus for matching at word boundaries
            if (ti === 0 || boundaryChars.indexOf(text[ti - 1]) !== -1) {
                score -= 5;
            }
            // Basename bonus: matches in the filename score much higher
            if (ti >= basenameStart) {
                score -= 3;
            }
            lastMatchPos = ti;
            // Mild position penalty (capped) — deep paths shouldn't be penalized heavily
            score += Math.min(ti * 0.1, 10);
            qi++;
        }
    }

    // All query chars must match
    if (qi < query.length) return null;
    return score;
}

export function fuzzyFilter(files, query) {
    if (!query) return [];

    const qLower = query.toLowerCase();
    const scored = [];

    for (const fp of files) {
        const matchResult = fuzzyMatch(fp.toLowerCase(), qLower, fp);
        if (matchResult !== null) {
            scored.push({ fp, score: matchResult });
        }
    }

    // Sort by score (lower = better match)
    scored.sort((a, b) => a.score - b.score);
    const limit = parseInt((state.settings || {}).file_search_limit, 10) || 500;
    return scored.slice(0, limit).map(s => s.fp);
}

async function onInput(e) {
    // Skip if command mention dropdown is active (mutual exclusion)
    const cmdDropdown = document.querySelector(".command-mention-dropdown");
    if (cmdDropdown && cmdDropdown.style.display !== "none") return;

    const input = e.target;
    const text = input.value;
    const cursor = input.selectionStart;

    // Find the '@' that starts the current mention
    const beforeCursor = text.slice(0, cursor);
    const atIndex = findMentionStart(beforeCursor);

    if (atIndex === -1) {
        hideDropdown();
        return;
    }

    mentionStart = atIndex;
    const query = beforeCursor.slice(atIndex + 1); // text after '@'

    // Require at least 1 character (or show root dir in directory mode)
    const mode = (state.settings || {}).file_search_mode || 'directory';
    if (mode === 'directory') {
        await onInputDirectory(query);
    } else {
        await onInputFuzzy(query);
    }
}

/** Shared directory browsing logic — returns { entries, dir, filter, results } or null.
 *  Used by both @ mention and file browser search. */
export async function getDirBrowseResults(query) {
    const lastSlash = query.lastIndexOf('/');
    const dir = lastSlash >= 0 ? query.slice(0, lastSlash) : '';
    const filter = (lastSlash >= 0 ? query.slice(lastSlash + 1) : query).toLowerCase();

    const entries = await fetchDirEntries(dir);
    if (entries.length === 0) return null;

    let filtered = entries;
    if (filter) {
        filtered = entries.filter(e => e.name.toLowerCase().includes(filter));
    }

    filtered.sort((a, b) => {
        if (a.type !== b.type) return a.type === 'dir' ? -1 : 1;
        return a.name.localeCompare(b.name);
    });

    if (filtered.length === 0) return null;

    const results = filtered.map(e => {
        // Use e.path (no trailing /) for building paths; add / for dirs
        const path = e.path || ((dir ? dir + '/' : '') + e.name.replace(/\/$/, ''));
        return path + (e.type === 'dir' ? '/' : '');
    });

    return { entries: filtered, dir, filter, results };
}

/** Directory browsing mode: tab-completion style navigation. */
async function onInputDirectory(query) {
    const browse = await getDirBrowseResults(query);
    if (!browse) {
        hideDropdown();
        return;
    }

    currentDir = browse.dir;
    currentResults = browse.results;
    selectedIndex = 0;
    renderDropdownDirectory(browse.entries, browse.dir, browse.filter);
}

/** Fuzzy matching mode (original behavior). */
async function onInputFuzzy(query) {
    if (query.length < 1) {
        hideDropdown();
        return;
    }

    // Fast path: use cached files synchronously if available
    let files = cachedFiles;
    const key = sessionKey(state.currentSession);
    const now = Date.now();
    if (files.length === 0 || cachedSessionKey !== key || (now - cachedTimestamp) >= CACHE_TTL_MS) {
        files = await fetchFileList();
    }

    if (files.length === 0) {
        hideDropdown();
        return;
    }

    // Throttle rendering to batch rapid keystrokes (30ms)
    clearTimeout(renderTimer);
    renderTimer = setTimeout(() => {
        // Re-read current query in case it changed during the throttle
        const currentInput = document.getElementById("command-input");
        if (!currentInput) return;
        const currentText = currentInput.value;
        const currentCursor = currentInput.selectionStart;
        const currentBefore = currentText.slice(0, currentCursor);
        const currentAtIndex = findMentionStart(currentBefore);
        if (currentAtIndex === -1) { hideDropdown(); return; }
        const currentQuery = currentBefore.slice(currentAtIndex + 1);
        if (currentQuery.length < 1) { hideDropdown(); return; }

        currentResults = fuzzyFilter(files, currentQuery);
        if (currentResults.length === 0) {
            hideDropdown();
            return;
        }
        selectedIndex = 0;
        renderDropdown(currentQuery);
    }, 30);
}

function findMentionStart(text) {
    // Walk backwards from end to find '@' that isn't preceded by a non-whitespace char
    // (so "foo@bar" doesn't trigger, but "foo @bar" or "@bar" does)
    for (let i = text.length - 1; i >= 0; i--) {
        const ch = text[i];
        // If we hit whitespace or newline before finding '@', no active mention
        if (ch === ' ' || ch === '\n' || ch === '\t') return -1;
        if (ch === '@') {
            // Valid if at start of text or preceded by whitespace
            if (i === 0 || /\s/.test(text[i - 1])) return i;
            return -1;
        }
    }
    return -1;
}

function renderDropdown(query) {
    if (!dropdown || currentResults.length === 0) {
        hideDropdown();
        return;
    }

    dropdown.innerHTML = currentResults.map((fp, i) => {
        const cls = i === selectedIndex ? "file-mention-item selected" : "file-mention-item";
        const highlighted = highlightMatch(fp, query);
        return `<div class="${cls}" data-index="${i}">${highlighted}</div>`;
    }).join("");

    dropdown.style.display = "block";

    // Add click handlers
    dropdown.querySelectorAll(".file-mention-item").forEach(el => {
        el.addEventListener("mousedown", (e) => {
            e.preventDefault(); // prevent blur
            const idx = parseInt(el.dataset.index);
            selectItem(idx);
        });
    });
}

function renderDropdownDirectory(entries, dir, filter) {
    if (!dropdown || currentResults.length === 0) {
        hideDropdown();
        return;
    }

    // Breadcrumb showing current directory path
    const breadcrumb = dir ? `<div class="file-mention-breadcrumb">${escapeHtml(dir)}/</div>` : '';

    const items = entries.map((entry, i) => {
        const cls = i === selectedIndex ? "file-mention-item selected" : "file-mention-item";
        const icon = entry.type === 'dir' ? '<span class="file-mention-dir-icon">&#128193;</span>' : '';
        // entry.name may already include trailing / for dirs
        const displaySuffix = entry.type === 'dir' && !entry.name.endsWith('/') ? '/' : '';
        const name = escapeHtml(entry.name + displaySuffix);
        const cleanName = entry.name.replace(/\/$/, ''); // name without trailing /
        // Highlight matching filter text
        let displayName = name;
        if (filter) {
            const idx = cleanName.toLowerCase().indexOf(filter);
            if (idx >= 0) {
                const before = escapeHtml(cleanName.slice(0, idx));
                const match = escapeHtml(cleanName.slice(idx, idx + filter.length));
                const after = escapeHtml(cleanName.slice(idx + filter.length) + displaySuffix);
                displayName = `${before}<strong>${match}</strong>${after}`;
            }
        }
        return `<div class="${cls}" data-index="${i}">${icon}${displayName}</div>`;
    }).join("");

    dropdown.innerHTML = breadcrumb + items;
    dropdown.style.display = "block";

    // Add click handlers
    dropdown.querySelectorAll(".file-mention-item").forEach(el => {
        el.addEventListener("mousedown", (e) => {
            e.preventDefault();
            const idx = parseInt(el.dataset.index);
            selectItem(idx);
        });
    });
}

function highlightMatch(filepath, query) {
    if (!query) return escapeHtml(filepath);
    const lower = filepath.toLowerCase();
    const qLower = query.toLowerCase();

    // Fuzzy match: walk filepath chars, matching query chars in order
    const matched = new Set();
    let qi = 0;
    for (let fi = 0; fi < lower.length && qi < qLower.length; fi++) {
        if (lower[fi] === qLower[qi]) {
            matched.add(fi);
            qi++;
        }
    }
    // If not all query chars matched, show without highlighting
    if (qi < qLower.length) return escapeHtml(filepath);

    // Build HTML, merging consecutive matched chars into <strong> runs
    let html = "";
    let inStrong = false;
    for (let i = 0; i < filepath.length; i++) {
        const isMatch = matched.has(i);
        if (isMatch && !inStrong) { html += "<strong>"; inStrong = true; }
        if (!isMatch && inStrong) { html += "</strong>"; inStrong = false; }
        html += escapeHtml(filepath[i]);
    }
    if (inStrong) html += "</strong>";
    return html;
}

function selectItem(index) {
    const input = document.getElementById("command-input");
    if (!input || index < 0 || index >= currentResults.length) return;

    const filepath = currentResults[index];
    const mode = (state.settings || {}).file_search_mode || 'directory';

    // In directory mode, selecting a directory expands into it
    if (mode === 'directory' && filepath.endsWith('/')) {
        const text = input.value;
        const before = text.slice(0, mentionStart + 1); // keep the '@'
        const after = text.slice(input.selectionStart);
        input.value = before + filepath + after;
        const newPos = mentionStart + 1 + filepath.length;
        input.selectionStart = input.selectionEnd = newPos;
        input.focus();
        // Trigger input event to refresh directory listing
        input.dispatchEvent(new Event('input', { bubbles: true }));
        return;
    }

    const text = input.value;
    const cursor = input.selectionStart;

    // Replace @query with the filepath (strip trailing / for files)
    const cleanPath = filepath.replace(/\/$/, '');
    const before = text.slice(0, mentionStart);
    const after = text.slice(cursor);
    input.value = before + cleanPath + " " + after;

    // Position cursor after the inserted path + space
    const newPos = mentionStart + cleanPath.length + 1;
    input.selectionStart = input.selectionEnd = newPos;
    input.focus();

    hideDropdown();
}

function onKeydown(e) {
    if (!dropdown || dropdown.style.display === "none") return;

    if (e.key === "ArrowDown") {
        e.preventDefault();
        selectedIndex = Math.min(selectedIndex + 1, currentResults.length - 1);
        updateSelection();
    } else if (e.key === "ArrowUp") {
        e.preventDefault();
        selectedIndex = Math.max(selectedIndex - 1, 0);
        updateSelection();
    } else if (e.key === "Tab" || e.key === "Enter") {
        if (currentResults.length > 0) {
            e.preventDefault();
            e.stopImmediatePropagation();
            selectItem(selectedIndex);
        }
    } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopImmediatePropagation();
        hideDropdown();
    }
}

function updateSelection() {
    if (!dropdown) return;
    dropdown.querySelectorAll(".file-mention-item").forEach((el, i) => {
        el.classList.toggle("selected", i === selectedIndex);
    });
    // Scroll selected item into view
    const selected = dropdown.querySelector(".file-mention-item.selected");
    if (selected) selected.scrollIntoView({ block: "nearest" });
}

function hideDropdown() {
    if (dropdown) dropdown.style.display = "none";
    currentResults = [];
    mentionStart = -1;
    currentDir = '';
    clearTimeout(renderTimer);
}

export function isDropdownVisible() {
    return dropdown && dropdown.style.display !== "none";
}

export function invalidateFileCache() {
    cachedFiles = [];
    cachedSessionKey = null;
    cachedTimestamp = 0;
    dirCache = {};
}
