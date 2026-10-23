// Through Coral Hub an artifact's comments ask the computer that owns it (#6245). The artifact's bytes are read from the context server (the open agent's or team's computer); the Comments panel
// used to read its routes from the hub itself (/api/artifacts/<id>/comments, a 404 there) unless the caller named the server. Real dashboard, the comments routes stood in for (helpers/comments_scale.js
// STUB): the open agent lives on a registered computer "ws1", and the pane (no server named) and the viewer dialog (server named, and not named) must all list and post under /api/remote/ws1/.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { STUB, HEX } = require('./helpers/comments_scale');
const BASE = require('./test_base').BASE;

const CONTENT = '# Plan\n\nThe backfill commits one batch at a time.\n';
const FX = { filename: 'plan.md', content: CONTENT, size: CONTENT.length, mediaType: 'text/markdown', comments: [
    { id: 1, parent_id: null, anchor: { whole: true }, body: 'Looks right.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date().toISOString() }] };

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(STUB(FX));
        // every request URL, in order, on top of the stub
        await ev(`(() => { const inner = window.fetch; window.__urls = []; window.fetch = (u, o) => { window.__urls.push(((o && o.method) || 'GET') + ' ' + String(u)); return inner(u, o); }; return true; })()`);
        await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); window.AV = await import(st + 'artifact_viewer.js'); const { showView } = await import(st + 'utils.js');
            state.currentSession = { type: 'live', name: 'boss', session_id: 'boss-sid', agent_type: 'claude', server: 'ws1' }; showView('live-session-view'); window.PP.setPreviewSession('boss-sid'); return true; })()`);
        const comments = () => ev(`window.__urls.filter(u => u.includes('/comments'))`);
        const reset = () => ev(`window.__urls.length = 0; true`);

        // 1. the preview pane, no server named: the context server's route
        await ev(`window.PP.openArtifactTab('coral://artifacts/' + ${JSON.stringify(HEX)}, { name: 'plan.md', mediaType: 'text/markdown' }); true`);
        await waitFor(`document.querySelectorAll('#preview-main .ac-card').length > 0 || window.__urls.some(u => u.includes('/comments'))`, 'the pane asks for comments', { timeout: 8000 });
        let urls = await comments();
        check('the preview pane asks the artifact\'s own computer for its comments', urls.length > 0 && urls.every(u => u.includes('/api/remote/ws1/api/artifacts/' + HEX + '/comments')), JSON.stringify(urls));
        check('no comments request goes to the hub\'s own route', !urls.some(u => /^[A-Z]+ \/api\/artifacts\//.test(u)));

        // 2. the viewer dialog, server not named, then named
        for (const named of [false, true]) {
            await reset();
            await ev(`window.AV.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'plan.md', mediaType: 'text/markdown'${named ? ", server: 'ws1'" : ''} }); true`);
            await waitFor(`window.__urls.some(u => u.includes('/comments'))`, `the viewer asks for comments (server ${named ? 'named' : 'not named'})`, { timeout: 8000 });
            urls = await comments();
            check(`the viewer dialog (server ${named ? 'named' : 'not named'}) asks the artifact's own computer`, urls.every(u => u.includes('/api/remote/ws1/api/artifacts/' + HEX + '/comments')), JSON.stringify(urls));
        }

        // 3. a post goes the same way
        await ev(`(() => { const t = document.querySelector('.artifact-viewer .ac-toggle, #artifact-viewer .ac-toggle'); if (t) t.click(); return true; })()`);
        await reset();
        const posted = await ev(`(async () => { const { createComment } = await import(${S} + 'artifact_comments_api.js'); const { serverUrl } = await import(${S} + 'server_base.js'); await createComment(${JSON.stringify(HEX)}, 'ws1', { body: 'Hello', anchor: { whole: true } }); return window.__urls.filter(u => u.startsWith('POST')); })()`);
        check('a comment posted for the remote artifact goes under /api/remote/ws1/', posted.length === 1 && posted[0].includes('/api/remote/ws1/api/artifacts/' + HEX + '/comments'), JSON.stringify(posted));
    } catch (e) { console.error(e && e.stack || e); results.push(false); } finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS hub artifact comments: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
