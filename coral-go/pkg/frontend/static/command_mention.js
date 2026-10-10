/* /command mention autocomplete for the command input textarea */

import { state } from './state.js';
import { isDropdownVisible as isFileMentionVisible } from './file_mention.js';
import { escapeHtml } from './utils.js';

let dropdown = null;
let selectedIndex = 0;
let currentResults = [];
let slashStart = -1; // position of the '/' character

export function initCommandMention() {
    const input = document.getElementById("command-input");
    if (!input) return;

    // Create dropdown element
    dropdown = document.createElement("div");
    dropdown.className = "command-mention-dropdown";
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

function onInput(e) {
    // Skip if file mention dropdown is active (mutual exclusion)
    if (isFileMentionVisible()) return;

    const input = e.target;
    const text = input.value;
    const cursor = input.selectionStart;

    // Find the '/' that starts the current command mention
    const beforeCursor = text.slice(0, cursor);
    const idx = findSlashStart(beforeCursor);

    if (idx === -1) {
        hideDropdown();
        return;
    }

    slashStart = idx;
    const query = beforeCursor.slice(idx + 1); // text after '/'

    const filtered = fuzzyFilter(state.currentCommands || [], query);
    currentResults = filtered;

    if (currentResults.length === 0) {
        hideDropdown();
        return;
    }

    selectedIndex = 0;
    renderDropdown(query);
}

function findSlashStart(text) {
    // Walk backwards from end to find '/' that is at start of text or preceded by whitespace
    for (let i = text.length - 1; i >= 0; i--) {
        const ch = text[i];
        // If we hit whitespace or newline before finding '/', no active mention
        if (ch === ' ' || ch === '\n' || ch === '\t') return -1;
        if (ch === '/') {
            // Valid if at start of text or preceded by whitespace
            if (i === 0 || /\s/.test(text[i - 1])) return i;
            return -1;
        }
    }
    return -1;
}

function fuzzyFilter(commands, query) {
    if (!Array.isArray(commands)) return [];
    if (!query) return commands.slice();

    const qLower = query.toLowerCase();
    const scored = [];

    for (const cmd of commands) {
        const searchText = (cmd.name + " " + (cmd.description || "")).toLowerCase();
        const matchResult = fuzzyMatch(searchText, qLower);
        if (matchResult !== null) {
            scored.push({ cmd, score: matchResult });
        }
    }

    // Sort by score (lower = better match)
    scored.sort((a, b) => a.score - b.score);
    return scored.map(s => s.cmd);
}

function fuzzyMatch(text, query) {
    // Walk text left-to-right matching query chars in order
    // Returns a score (lower is better) or null if no match
    let qi = 0;
    let score = 0;
    let lastMatchPos = -1;

    for (let ti = 0; ti < text.length && qi < query.length; ti++) {
        if (text[ti] === query[qi]) {
            // Bonus for consecutive matches
            if (lastMatchPos === ti - 1) {
                score -= 1;
            }
            // Bonus for matching at word boundaries
            if (ti === 0 || text[ti - 1] === ' ' || text[ti - 1] === '-' || text[ti - 1] === '/') {
                score -= 2;
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

function renderDropdown(query) {
    if (!dropdown || currentResults.length === 0) {
        hideDropdown();
        return;
    }

    dropdown.innerHTML = currentResults.map((cmd, i) => {
        const cls = i === selectedIndex ? "command-mention-item selected" : "command-mention-item";
        const nameHtml = highlightMatch(cmd.command, query);
        const descHtml = escapeHtml(cmd.description || "");
        return `<div class="${cls}" data-index="${i}">
            <span class="cmd-name">${nameHtml}</span>
            <span class="cmd-desc">${descHtml}</span>
        </div>`;
    }).join("");

    dropdown.style.display = "block";

    // Add click handlers
    dropdown.querySelectorAll(".command-mention-item").forEach(el => {
        el.addEventListener("mousedown", (e) => {
            e.preventDefault(); // prevent blur
            const idx = parseInt(el.dataset.index);
            selectItem(idx);
        });
    });
}

function highlightMatch(command, query) {
    if (!query) return escapeHtml(command);
    // The query is matched against name (without /), but we display the full /command
    // Strip the leading / from command for matching purposes
    const cmdBody = command.startsWith("/") ? command.slice(1) : command;
    const lower = cmdBody.toLowerCase();
    const qLower = query.toLowerCase();

    const matched = new Set();
    let qi = 0;
    for (let fi = 0; fi < lower.length && qi < qLower.length; fi++) {
        if (lower[fi] === qLower[qi]) {
            matched.add(fi + 1); // +1 to account for the leading /
            qi++;
        }
    }

    // Build HTML for the full command string
    const full = command;
    let html = "";
    let inStrong = false;
    for (let i = 0; i < full.length; i++) {
        const isMatch = matched.has(i);
        if (isMatch && !inStrong) { html += "<strong>"; inStrong = true; }
        if (!isMatch && inStrong) { html += "</strong>"; inStrong = false; }
        html += escapeHtml(full[i]);
    }
    if (inStrong) html += "</strong>";
    return html;
}

function selectItem(index) {
    const input = document.getElementById("command-input");
    if (!input || index < 0 || index >= currentResults.length) return;

    const cmd = currentResults[index];
    const text = input.value;
    const cursor = input.selectionStart;

    // Replace /query with the full command
    const before = text.slice(0, slashStart);
    const after = text.slice(cursor);
    input.value = before + cmd.command + " " + after;

    // Position cursor after the inserted command + space
    const newPos = slashStart + cmd.command.length + 1;
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
    dropdown.querySelectorAll(".command-mention-item").forEach((el, i) => {
        el.classList.toggle("selected", i === selectedIndex);
    });
    // Scroll selected item into view
    const selected = dropdown.querySelector(".command-mention-item.selected");
    if (selected) selected.scrollIntoView({ block: "nearest" });
}

function hideDropdown() {
    if (dropdown) dropdown.style.display = "none";
    currentResults = [];
    slashStart = -1;
}

export function isDropdownVisible() {
    return dropdown && dropdown.style.display !== "none";
}
