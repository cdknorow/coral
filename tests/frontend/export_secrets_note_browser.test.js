// #3765 (secrets audit #3762, finding 5): an export is plain text and holds agent output as written, so it may hold secrets
// the agents printed. The board's Export dialog says so in one line, above the Export button and without a way to miss it
// (always shown, not a tooltip).
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`(async () => { const mb = await import((window.__coralStatic || '/static/') + 'message_board.js'); mb.selectBoardProject('qa-export-board'); await mb.showExportBoardModal(); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('mb-export-modal')).display === 'flex'`, 'the Export dialog', 8000);

        console.log('1. The Export dialog says the export may hold secrets and is not encrypted');
        const note = await ev(`(() => {
            const n = document.getElementById('export-secrets-note'), btn = document.querySelector('#mb-export-actions [data-on-click=mb-export-do]');
            const r = n.getBoundingClientRect();
            return { text: n.textContent.trim(), shown: r.width > 0 && r.height > 0, inBody: !!n.closest('#mb-export-body'), above: !!(n.compareDocumentPosition(btn) & Node.DOCUMENT_POSITION_FOLLOWING), lines: Math.round(r.height / parseFloat(getComputedStyle(n).lineHeight || '18')), title: n.title };
        })()`);
        assert.match(note.text, /agent output as written/, note.text);
        assert.match(note.text, /may include secrets the agents printed/, note.text);
        assert.match(note.text, /not encrypted/, note.text);
        assert.ok(note.shown && note.inBody && note.above, `visible, in the dialog, above Export (${JSON.stringify(note)})`);
        console.log(`   ✓ "${note.text}"`);

        console.log('\nAll export-note checks passed');
    } finally {
        await removeBoards(ev, ['qa-export-board']);
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
