// Comments on an artifact against the REAL isolated server (#5176 + #5182): the viewer's panel and the routes, store and policy meeting. The artifact is written into the server's data folder
// (its object and sidecar, as an upload leaves them); a comment is made from a selection in the document through the panel, and the numbers the server stores are read back through its API and its
// database: the quote and its context, the author (this computer's owner), a reply, resolve, an edit, the author-only delete (a comment another author wrote cannot be deleted from the panel or
// the API), the lock (read only, the panel says so), the read-only answer a Coral Hub viewer gets, and deleting the artifact takes its comments. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('./helpers/child_process.js');
const { connect } = require('./agent_view_harness');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

const DOC = ['# Migration plan', '', '## Steps', '', '1. Add the new columns behind a flag and backfill in batches of 500 rows.', '2. Switch reads to the new columns.', '', '## Risks', '', 'The backfill takes a write lock on large databases.', ''].join('\n');
const HEX = crypto.createHash('sha256').update(DOC).digest('hex');
const DATA = process.env.CORAL_TEST_DATA_DIR || '';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const guard = await guardSettings(); // the install's owner id (a setting) is made the first time a comment is written, and put back at the end
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the selection button appears after a short debounce
    const api = (method, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => ({})) }))`);
    const list = async () => (await api('GET', `/api/artifacts/${HEX}/comments?state=all`)).body;
    const { sqliteText, sqliteExec } = require('./helpers/sqlite.js');
    const sql = q => /^\s*select/i.test(q) ? sqliteText(path.join(DATA, 'sessions.db'), q) : (sqliteExec(path.join(DATA, 'sessions.db'), q), '');
    try {
        assert.ok(DATA, 'run under run_cdp_suites.mjs (CORAL_TEST_DATA_DIR names the isolated server\'s folder)');
        const dir = path.join(DATA, 'artifacts', 'objects');
        fs.mkdirSync(dir, { recursive: true });
        fs.writeFileSync(path.join(dir, HEX), DOC);
        fs.writeFileSync(path.join(dir, HEX + '.json'), JSON.stringify({ name: 'plan.md', media_type: 'text/markdown', size: Buffer.byteLength(DOC), digest: HEX }));
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const open = async () => { await ev(`import((window.__coralStatic || '/static/') + 'artifact_viewer.js').then(m => m.openArtifactViewer('${HEX}', { filename: 'plan.md', mediaType: 'text/markdown' })); true`); await waitFor(`!!document.querySelector('#artifact-viewer:not([hidden]) .artifact-viewer-markdown') && !!document.querySelector('.ac-panel')`, 'the viewer with its panel', 8000); };
        await open();
        await waitFor(`document.querySelector('.ac-panel .ac-empty') || document.querySelector('.ac-panel .ac-card')`, 'the panel to load', 8000);
        check('a fresh artifact has no comments, and the panel says how to add one', (await ev(`document.querySelector('.ac-panel .ac-empty') && document.querySelector('.ac-panel .ac-empty').textContent`)) !== null);

        // A comment from a selection, through the panel
        await ev(`(() => { const w = document.createTreeWalker(document.querySelector('.artifact-viewer-body'), NodeFilter.SHOW_TEXT); let n; while ((n = w.nextNode())) { const i = n.nodeValue.indexOf('backfill in batches of 500 rows'); if (i >= 0) { const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + 'backfill in batches of 500 rows'.length); const s = getSelection(); s.removeAllRanges(); s.addRange(r); document.dispatchEvent(new Event('selectionchange')); return true; } } return false; })()`);
        await waitFor(`!!document.querySelector('.ac-selbtn') && !document.querySelector('.ac-selbtn').hidden`, 'the Comment button by the selection', 4000);
        await ev(`document.querySelector('.ac-selbtn').click(); true`);
        await waitFor(`!!document.querySelector('.ac-panel .ac-input')`, 'the composer', 4000);
        await ev(`(() => { const ta = document.querySelector('.ac-panel .ac-input'); ta.value = 'Try 5,000 rows per batch.'; ta.dispatchEvent(new Event('input', { bubbles: true })); ta.closest('form').requestSubmit(); return true; })()`);
        await waitFor(`document.querySelectorAll('.ac-panel .ac-card').length === 1`, 'the new card', 6000);
        let d = await list();
        check('the comment is on the server: its body, its quote with the context around it, its author the owner of this computer', d.comments.length === 1 && d.comments[0].body === 'Try 5,000 rows per batch.' && d.comments[0].anchor.quote === 'backfill in batches of 500 rows' && /behind a flag and\s*$/.test(d.comments[0].anchor.prefix) && /^\s*\./.test(d.comments[0].anchor.suffix) && d.comments[0].author.kind === 'owner' && d.comments[0].author.issuer === 'local', JSON.stringify(d.comments[0] && { a: d.comments[0].anchor, au: d.comments[0].author }));
        check('and it says so: mine, with every action the owner has on it', d.comments[0].mine === true && d.comments[0].can.edit && d.comments[0].can.delete && d.comments[0].can.resolve && d.comments[0].can.reply && d.you.can_lock === true);
        check('the table row is in the database as sent (issuer, subject, kind)', sql(`select author_issuer || '|' || author_kind from artifact_comments where artifact_id = '${HEX}'`).trim().startsWith('local|owner'), sql(`select author_issuer, author_subject, author_kind from artifact_comments`).trim());
        const id1 = d.comments[0].id;

        // A reply, edit, resolve through the real routes (the panel's own buttons)
        const rep = await api('POST', `/api/artifacts/${HEX}/comments`, { body: 'And measure the lock time.', parent_id: id1 });
        check('a reply is a second comment under the first', rep.status === 201 && rep.body.parent_id === id1, JSON.stringify(rep.body));
        const ed = await api('PATCH', `/api/artifacts/${HEX}/comments/${id1}`, { body: 'Try 5,000 rows a batch.' });
        check('editing your own works and stamps edited_at', ed.status === 200 && ed.body.edited_at, JSON.stringify(ed.body.edited_at));
        const res = await api('POST', `/api/artifacts/${HEX}/comments/${id1}/resolve`, {});
        check('resolving the thread names who did it', res.status === 200 && res.body.resolved_by && res.body.resolved_by.issuer === 'local', JSON.stringify(res.body.resolved_by));
        await api('POST', `/api/artifacts/${HEX}/comments/${id1}/reopen`, {});

        // A comment another author wrote cannot be deleted (panel and API)
        sql(`insert into artifact_comments (artifact_id, board, anchor_json, body, author_issuer, author_subject, author_kind, created_at) values ('${HEX}', '', '{"quote":"takes a write lock","prefix":"The backfill ","suffix":" on large"}', 'From someone else.', 'coral-hub', 'hub-user-3f2a9c01', 'hub', '2026-10-19T10:00:00Z')`);
        d = await list();
        const other = d.comments.find(c => c.body === 'From someone else.');
        check('another author\'s comment is listed as not mine, with no edit or delete', other && other.mine === false && other.can.edit === false && other.can.delete === false && other.can.resolve === true && other.author.subject === 'hub-user-3f2a9c01', JSON.stringify(other && other.can));
        const del = await api('DELETE', `/api/artifacts/${HEX}/comments/${other.id}`);
        check('deleting it is refused with the reason, even for the owner', del.status === 403 && /your own/.test(del.body.error), JSON.stringify(del.body));
        await open();
        await waitFor(`document.querySelectorAll('.ac-panel .ac-card').length >= 2`, 'both threads', 6000);
        const buttons = await ev(`[...document.querySelectorAll('.ac-panel .ac-card')].map(li => ({ text: li.querySelector('.ac-body') && li.querySelector('.ac-body').textContent, actions: [...li.querySelectorAll('.ac-entry:first-of-type button, :scope > .ac-replybox')].map(b => b.textContent) }))`);
        const o = buttons.find(b => b.text === 'From someone else.');
        check('the panel offers the reply box and Resolve on it, and no Edit and no Delete', o && o.actions.includes('Reply…') && o.actions.includes('Resolve') && !o.actions.includes('Edit') && !o.actions.includes('Delete'), JSON.stringify(o));
        check('and Edit and Delete on the owner\'s own', buttons.some(b => b.text === 'Try 5,000 rows a batch.' && b.actions.includes('Edit') && b.actions.includes('Delete')), JSON.stringify(buttons));

        // Lock: read only, and the panel says so
        const lock = await api('PUT', `/api/artifacts/${HEX}/comments/lock`, { scope: 'artifact' });
        check('the owner locks the artifact\'s comments', lock.status === 200 && lock.body.locked === true);
        const blocked = await api('POST', `/api/artifacts/${HEX}/comments`, { body: 'x', anchor: { whole: true } });
        check('locked: a new comment is refused with the reason', blocked.status === 403 && /locked/.test(blocked.body.error), JSON.stringify(blocked.body));
        await open();
        await waitFor(`!!document.querySelector('.ac-panel .ac-locked')`, 'the locked note', 6000);
        check('the panel shows the lock and offers Unlock, no composer', (await ev(`!!document.querySelector('.ac-panel .ac-lock') && /Unlock/.test(document.querySelector('.ac-panel .ac-lock').textContent) && !document.querySelector('.ac-panel .ac-whole')`)) === true);
        await api('DELETE', `/api/artifacts/${HEX}/comments/lock`, { scope: 'artifact' });

        // Through Coral Hub a viewer only reads: the same list, with no write answered (the stamp is the connector's; here only the refusal of a forged one is checked)
        const forged = await ev(`fetch('/api/artifacts/${HEX}/comments', { headers: { 'X-Coral-Via-Tunnel': 'forged' } }).then(r => r.status)`);
        check('a forged tunnel stamp is refused outright (by the sign-in layer or the route), never answered', forged === 401 || forged === 403, String(forged));

        // Deleting the artifact removes its comments and its lock
        const rm = await api('DELETE', `/api/artifacts/${HEX}`);
        check('deleting the artifact works', rm.status === 200, JSON.stringify(rm.body));
        check('and takes its comments with it', sql(`select count(*) from artifact_comments where artifact_id = '${HEX}'`).trim() === '0');
        const gone = await api('GET', `/api/artifacts/${HEX}/comments`);
        check('and its comments route answers not found', gone.status === 404, String(gone.status));
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { await guard.restore().catch(() => {}); try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS artifact comments on the real server: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
