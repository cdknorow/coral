/* @file mention autocomplete for the command input textarea */

import { state, sessionKey } from './state.js';

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
            const resp = await fetch(`/api/sessions/live/${name}/search-files?${params}`);
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
    const boundaryChars = '/_-.';

    for (let ti = 0; ti < text.length && qi < query.length; ti++) {
        if (text[ti] === query[qi]) {
            // Bonus for consecutive matches
            if (lastMatchPos === ti - 1) {
                score -= 1;
            }
            // Bonus for matching at word boundaries
            if (ti === 0 || boundaryChars.indexOf(text[ti - 1]) !== -1) {
                score -= 2;
            }
            // Basename bonus: matches in the filename score higher
            if (ti >= basenameStart) {
                score -= 1;
            }
            lastMatchPos = ti;
            score += ti; // Penalize matches later in the string
            qi++;
        }
    }

    // All query chars must match
    if (qi < query.length) return null;
    return score;
}

function fuzzyFilter(files, query) {
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
    return scored.slice(0, 50).map(s => s.fp);
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

    // Require at least 1 character
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

function escapeHtml(str) {
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function selectItem(index) {
    const input = document.getElementById("command-input");
    if (!input || index < 0 || index >= currentResults.length) return;

    const filepath = currentResults[index];
    const text = input.value;
    const cursor = input.selectionStart;

    // Replace @query with the filepath
    const before = text.slice(0, mentionStart);
    const after = text.slice(cursor);
    input.value = before + filepath + " " + after;

    // Position cursor after the inserted path + space
    const newPos = mentionStart + filepath.length + 1;
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
    clearTimeout(renderTimer);
}

export function isDropdownVisible() {
    return dropdown && dropdown.style.display !== "none";
}

export function invalidateFileCache() {
    cachedFiles = [];
    cachedSessionKey = null;
    cachedTimestamp = 0;
}
