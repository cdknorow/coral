/* /command mention autocomplete for the command input textarea */

import { state } from './state.js';
import { isDropdownVisible as isFileMentionVisible } from './file_mention.js';
import { createMentionList, findTriggerStart, highlightRuns } from './mention_list.js';

// results, selection and the position of the '/' live on the shared list (mention_list.js).
const list = createMentionList({ dropdown: 'command-mention-dropdown', item: 'command-mention-item' }, { onInput: e => onInput(e), onPick: i => selectItem(i) });

export function initCommandMention() {
    const input = document.getElementById("command-input");
    if (!input) return;
    list.mount(input);
}

function onInput(e) {
    // Skip if file mention dropdown is active (mutual exclusion)
    if (isFileMentionVisible()) return;

    const input = e.target;
    const text = input.value;
    const cursor = input.selectionStart;

    // Find the '/' that starts the current command mention
    const beforeCursor = text.slice(0, cursor);
    const idx = findTriggerStart(beforeCursor, '/');

    if (idx === -1) {
        list.hide();
        return;
    }

    list.start = idx;
    const query = beforeCursor.slice(idx + 1); // text after '/'

    const filtered = fuzzyFilter(state.currentCommands || [], query);
    list.results = filtered;

    if (list.results.length === 0) {
        list.hide();
        return;
    }

    list.selected = 0;
    renderDropdown(query);
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
    if (!list.el || list.results.length === 0) {
        list.hide();
        return;
    }

    list.show(list.results.map(cmd => ({
        sep: ' ',
        cells: [
            { cls: 'cmd-name', parts: highlightParts(cmd.command, query) },
            { cls: 'cmd-desc', parts: [cmd.description || ''] },
        ],
    })));
}

function highlightParts(command, query) {
    if (!query) return [command];
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
    return highlightRuns(command, matched);
}

function selectItem(index) {
    const input = document.getElementById("command-input");
    if (!input || index < 0 || index >= list.results.length) return;

    const cmd = list.results[index];
    const text = input.value;
    const cursor = input.selectionStart;

    // Replace /query with the full command
    const before = text.slice(0, list.start);
    const after = text.slice(cursor);
    input.value = before + cmd.command + " " + after;

    // Position cursor after the inserted command + space
    const newPos = list.start + cmd.command.length + 1;
    input.selectionStart = input.selectionEnd = newPos;
    input.focus();

    list.hide();
}

export function isDropdownVisible() {
    return list.visible();
}
