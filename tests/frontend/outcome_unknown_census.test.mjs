// A write answered 502 outcome_unknown (#6342) is never sent again by itself and says one line. Where the dashboard reads that code is a short list, each reason named: a new reader of it has to be added here on purpose, with the
// proof that it does not retry (hub_outcome_unknown_browser counts every write twice, a wait apart). A file that mentions it and retries, or an upload's Retry offered for it, fails.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const st = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../coral-go/pkg/frontend/static');
const READERS = new Map([
    ['outcome_unknown.js', 'the one line and the test for the code'],
    ['cloud.js', "the hub's fetch wrapper puts the line in the body of any write's outcome_unknown answer"],
    ['server_status.js', 'responseErrorText: the sentence for a failed send'],
    ['board_chat.js', "the team board's message box says the line and puts the words back"],
    ['message_board.js', "the message board's post says the line"],
    ['image_attach.js', "an upload says the line with no Retry"],
]);
const walk = d => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => e.isDirectory() ? (['vendor', 'css', 'fonts'].includes(e.name) ? [] : walk(path.join(d, e.name))) : e.name.endsWith('.js') ? [path.join(d, e.name)] : []);
const mention = [];
for (const f of walk(st)) { const t = fs.readFileSync(f, 'utf8'); if (/outcome_unknown|outcomeUnknownLine|isOutcomeUnknown|OUTCOME_UNKNOWN_LINE/.test(t)) mention.push(path.relative(st, f)); }
const extra = mention.filter(f => !READERS.has(f)), missing = [...READERS.keys()].filter(f => !mention.includes(f));
assert.deepEqual(extra, [], `a script reads outcome_unknown and is not on the list (add it with the reason, and prove it never retries): ${extra.join(', ')}`);
assert.deepEqual(missing, [], `listed but no longer mentions outcome_unknown: ${missing.join(', ')}`);
// the readers never retry what they read: no timer or retry word in the lines that handle it
for (const f of ['board_chat.js', 'message_board.js', 'cloud.js', 'server_status.js']) {
    const lines = fs.readFileSync(path.join(st, f), 'utf8').split('\n');
    lines.forEach((l, i) => { if (/outcome_unknown|outcomeUnknownLine|isOutcomeUnknown/.test(l)) assert.doesNotMatch(lines.slice(Math.max(0, i - 1), i + 2).join('\n'), /setTimeout|setInterval|afterPause|\bretry\b|requestSubmit/i, `${f}:${i + 1} sits next to a retry`); });
}
const up = fs.readFileSync(path.join(st, 'image_attach.js'), 'utf8');
assert.match(up, /code === 'outcome_unknown'\) return \{ text: OUTCOME_UNKNOWN_LINE, retry: false \}/, 'an upload answered outcome_unknown offers no Retry');
console.log(`outcome_unknown_census: ok (${mention.length} files read the code, all listed; none retries)`);
