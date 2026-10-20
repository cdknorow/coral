// Settings labels are sentence case (#5423): "Show scrollbars", not "Show Scrollbars". The panes had both ("Group agents by board or team" beside "Show Scrollbars" and "Check for Updates"), so a
// label is checked here: a row title (<dt>) or a settings switch label (<label for="settings-...">) of two or more words may not start every word with a capital. Names (Coral, Hub, Claude, Codex,
// Antigravity, Git, API, CLI, tmux...) are allowed to be capitalised. Run on its own: node settings_label_case.test.mjs
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const html = fs.readFileSync(path.resolve(here, '../../coral-go/pkg/frontend/templates/includes/modals.html'), 'utf8');
const NAMES = new Set(['Coral', 'Hub', 'Claude', 'Codex', 'Antigravity', 'Pi', 'Git', 'GitHub', 'API', 'CLI', 'PTY', 'GiB', 'Persona', 'Role', 'Name']);
const strip = (s) => s.replace(/<[^>]*>/g, ' ').replace(/&[a-z]+;/g, ' ').replace(/\s+/g, ' ').trim();

/** The label texts to check: the row titles (<dt>) and switch labels (<label for="settings-...">) of the Settings dialog, each without the help text inside it. */
export function labels(markup) {
    const start = markup.indexOf('id="settings-modal"');
    const end = markup.indexOf('id="default-prompts-modal"');
    const region = start >= 0 ? markup.slice(start, end > start ? end : undefined) : markup;
    const own = (inner) => strip(inner.replace(/<(span|div|p|small|button)\b[^>]*>[\s\S]*?<\/\1>/g, ' '));
    const out = [];
    for (const m of region.matchAll(/<dt\b[^>]*>([\s\S]*?)<\/dt>/g)) out.push(own(m[1]));
    for (const m of region.matchAll(/<label\b[^>]*\bfor="settings-[^"]*"[^>]*>([\s\S]*?)<\/label>/g)) out.push(own(m[1]));
    return out.filter(Boolean);
}

/** Whether a label is Title Case: two or more words after the first that start with a capital and are not names. */
export function titleCased(text) {
    const words = text.replace(/[(][^)]*[)]/g, '').split(/\s+/).filter(w => /^[A-Za-z]/.test(w));
    if (words.length < 2) return false;
    const caps = words.slice(1).filter(w => /^[A-Z][a-z]/.test(w) && !NAMES.has(w));
    return caps.length >= 1;
}

// self-test: what it must catch and let through
assert.ok(titleCased('Show Scrollbars') && titleCased('Default Working Directory') && titleCased('File Search Mode'));
assert.ok(!titleCased('Show scrollbars') && !titleCased('Coral Hub') && !titleCased('Default models') && !titleCased('Artifact store limit (GiB)') && !titleCased('CLI paths') && !titleCased('Run as a hub'));
assert.deepEqual(labels('<dt>Theme</dt><label for="settings-x">Check Updates<span>Help Text Here</span></label><label for="other">Not Settings</label>'), ['Theme', 'Check Updates']);

const found = labels(html);
assert.ok(found.length > 30, 'the settings labels were found (' + found.length + ')');
const bad = [...new Set(found.filter(titleCased))];
assert.deepEqual(bad, [], 'Title Case settings labels (use sentence case): ' + bad.join(', '));
console.log(`settings_label_case: ok (${found.length} labels, none Title Case)`);
