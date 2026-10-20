// Artifacts panel: every row with a link has a "Copy link" button that copies
// the absolute URL, and the header instructions no longer mention tasks.
// Run: node team_artifacts_copy_link.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { installDom, importUi, STATIC } from './helpers/jsdom_env.mjs';

installDom();
const digest = 'a'.repeat(64);
const artifacts = [
    { id: digest, name: 'report.md', uri: `coral://artifacts/${digest}`, media_type: 'text/markdown', source: 'upload' },
    { id: 'ext', name: 'Design doc', uri: 'https://docs.example/d/1', task_id: 7 },
    { id: 'gone', name: 'old.txt', uri: `coral://artifacts/${'b'.repeat(64)}`, available: false },
];
globalThis.fetch = window.fetch = async (url) => {
    assert.match(String(url), /\/api\/session-artifacts\?/);
    return { ok: true, status: 200, json: async () => ({ artifacts, session_id: 's1', has_more: false }) };
};
const copied = [];
let failCopy = false;
Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: async (text) => { if (failCopy) throw new Error('denied'); copied.push(text); } },
});

const { state } = await importUi('state');
state.currentSession = { type: 'live', session_id: 's1', name: 'dev' }; // standalone: no team
const { initFilesSourcePicker, selectFilesSource } = await importUi('team_artifacts');
initFilesSourcePicker(() => {}, () => {});
selectFilesSource('artifacts');
await new Promise(r => setTimeout(r, 0));

const view = document.getElementById('agentic-panel-artifacts-view');
const rows = [...view.querySelectorAll('tr.team-artifact-row')];
assert.equal(rows.length, 3);
const buttons = rows.map(r => r.querySelector('.team-artifact-copy-link'));
assert.ok(buttons[0] && buttons[1], 'linked rows have a Copy link button');
assert.equal(buttons[2], null, 'a missing artifact has nothing to copy');
assert.equal(buttons[0].getAttribute('aria-label'), 'Copy link to report.md');
assert.equal(buttons[0].title, 'Copy link');
assert.equal(buttons[0].type, 'button');
assert.ok(buttons[0].querySelector('svg[width="16"]'), 'inline 16px icon like the other row actions');
assert.equal(rows[0].querySelectorAll('.team-artifact-actions > *').length, 4, 'details, preview, download, copy link');

buttons[0].click();
await new Promise(r => setTimeout(r, 0));
assert.deepEqual(copied, [`https://hub.example/api/artifacts/${digest}?v=2`], 'managed artifacts copy an absolute URL in the v1.4.12 link form');
assert.ok(buttons[0].querySelector('path[d="m3 8.5 3 3 7-7"]'), 'success shows the check icon');
buttons[1].click();
await new Promise(r => setTimeout(r, 0));
assert.equal(copied[1], 'https://docs.example/d/1', 'external links copy their own URL');
failCopy = true;
buttons[1].click();
await new Promise(r => setTimeout(r, 0));
assert.ok(buttons[1].querySelector('circle'), 'failure shows the error icon, no dialog');

// The header copy button pastes instructions with no task or manifest step.
copied.length = 0; failCopy = false;
view.querySelector('.team-artifacts-help').click();
await new Promise(r => setTimeout(r, 0));
assert.equal(copied.length, 1);
assert.match(copied[0], /coral-agent artifact upload <file>/);
assert.match(copied[0], /appears in the Artifacts tab right away/);
assert.doesNotMatch(copied[0], /task|manifest/i);
// The source carries the same text the button copied (guards against a second copy of it).
assert.ok(fs.readFileSync(STATIC + 'team_artifacts.js', 'utf8').includes('It appears in the Artifacts tab right away.'));

console.log('team_artifacts_copy_link: ok');
process.exit(0);
