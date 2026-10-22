// "Who can read this" (#5577): a small disclosure for the Operator in the artifact viewer and in an Artifacts row's details, from
// GET /api/artifacts/{id}/readers. Against the real server: an agent uploads an artifact, its Artifacts tab row has the disclosure in its
// details, opening it says who holds the artifact and carries the server's note that this is a Coral control, not an operating-system
// boundary; the viewer has it under its header. Then the machine's answers are stubbed: a refusal (anyone else) draws nothing, and
// hostile names are text, never markup. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const { coralReady } = require('./helpers/wait').bind(ev);
    const shot = async (name, clip) => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 250)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png', ...(clip ? { clip: { ...clip, scale: 1 } } : {}) })).data, 'base64')); };
    let sid = null, name = null;
    try {
        await Page.enable(); // the real live list, not the harness's mock agents: the agent below is a real session
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await coralReady();
        const launch = await ev(`fetch('/api/sessions/launch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ working_dir: ${JSON.stringify(require('node:os').tmpdir())}, agent_type: 'terminal', display_name: 'Readers QA' }) }).then(r => r.json())`);
        sid = launch.session_id; name = launch.session_name || launch.name;
        const up = await ev(`fetch('/api/agent/artifacts?session_id=${sid}', { method: 'POST', headers: { 'X-Artifact-Name': 'readers.txt', 'X-Artifact-Media-Type': 'text/plain' }, body: 'who can read this ' + Date.now() }).then(async r => ({ status: r.status, body: await r.json() }))`);
        assert.equal(up.status, 201, JSON.stringify(up));
        const hex = String(up.body.uri).split('/').pop();

        console.log('1. The row: the disclosure is in its details, closed, and says who holds it');
        await Page.navigate({ url: `${BASE}/agents/${sid}` }); await Page.loadEventFired(); await coralReady();
        await waitFor(`!!document.getElementById('agentic-tab-artifacts')`, 'the Artifacts tab', 15000);
        await ev(`document.getElementById('agentic-tab-artifacts').click(); true`);
        await waitFor(`!!document.querySelector('.team-artifact-row .team-artifact-info-btn')`, 'the row', 15000);
        assert.equal(await ev(`document.querySelectorAll('.artifact-readers').length`), 0, 'a listing asks nothing: the disclosure comes with the details');
        await ev(`document.querySelector('.team-artifact-row .team-artifact-info-btn').click(); true`);
        await waitFor(`!!document.querySelector('.team-artifact-row .artifact-readers')`, 'the disclosure in the row', 15000, `({ rows: document.querySelectorAll('.team-artifact-row').length, rd: document.querySelectorAll('.artifact-readers').length, html: (document.querySelector('.team-artifact-row') || {}).outerHTML && document.querySelector('.team-artifact-row').outerHTML.slice(0, 300) })`);
        assert.deepEqual(await ev(`(() => { const d = document.querySelector('.team-artifact-row .artifact-readers'); return { open: d.open, summary: d.querySelector('summary').textContent.replace('expand_more', ''), chevron: !!d.querySelector('summary .artifact-readers-chevron'), inDetails: !!d.closest('.team-artifact-details') }; })()`), { open: false, summary: 'Who can read this', chevron: true, inDetails: true });
        const turn = () => ev(`getComputedStyle(document.querySelector('.team-artifact-row .artifact-readers-chevron')).transform`);
        assert.equal(await turn(), 'none', 'the chevron points down while it is closed');
        await ev(`document.querySelector('.team-artifact-row .artifact-readers > summary').click(); true`);
        await waitFor(`document.querySelector('.team-artifact-row .artifact-readers').open`, 'open');
        await new Promise(r => setTimeout(r, 250)); // sleep-ok: the chevron's turn is a 0.12 s transition, there is no event to wait for
        assert.notEqual(await turn(), 'none', 'and turned once it is open');
        await waitFor(`document.querySelectorAll('.team-artifact-row .artifact-readers-body p').length >= 2`, 'the readers', 10000);
        const body = await ev(`[...document.querySelectorAll('.team-artifact-row .artifact-readers-body p')].map(p => p.textContent)`);
        assert.match(body[0], /^On this computer, the Operator reads, manages and deletes every artifact/, JSON.stringify(body));
        assert.ok(body.some(t => /^Held by the Operator on this computer/.test(t)), JSON.stringify(body));
        assert.match(body[body.length - 1], /not an operating-system boundary.*its own shell\.$/, 'the whole plain note is last: ' + body[body.length - 1]);
        await shot('readers-row', { x: 980, y: 90, width: 420, height: 800 }); // the panel only: the terminal beside it shows the operator's own prompt

        console.log('2. The viewer: the disclosure sits under its header');
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'artifact_viewer.js'); m.openArtifactViewer('${hex}', { filename: 'readers.txt' }); return true; })()`);
        await waitFor(`!!document.querySelector('#artifact-viewer .artifact-viewer-readers .artifact-readers')`, 'the disclosure in the viewer', 10000);
        assert.equal(await ev(`document.querySelector('#artifact-viewer .artifact-viewer-readers').hidden`), false);
        await ev(`document.querySelector('#artifact-viewer .artifact-readers > summary').click(); true`);
        await waitFor(`document.querySelectorAll('#artifact-viewer .artifact-readers-body p').length >= 2`, 'the readers in the viewer', 10000);
        await shot('readers-viewer', { x: 80, y: 290, width: 1240, height: 320 });
        await ev(`document.querySelector('#artifact-viewer .artifact-viewer-close').click(); true`);

        console.log('2b. No answer is not remembered: a 404 or a network error hides it once, a later answer shows it');
        const stubSeq = (answers) => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'artifact_readers.js'); m._resetForTests(); const real = window.__realFetch || (window.__realFetch = window.fetch); const seq = ${JSON.stringify(answers)}; let i = 0;
            window.fetch = (u, o) => { if (!String(u).includes('/readers')) return real(u, o); const a = seq[Math.min(i++, seq.length - 1)]; return a === 'error' ? Promise.reject(new TypeError('offline')) : Promise.resolve(new Response(JSON.stringify(a.body), { status: a.status, headers: { 'content-type': 'application/json' } })); }; return true; })()`);
        const GOOD = { status: 200, body: { id: hex, machine_owner: 'reads everything', owners: [], linked_teams: [], note: 'n' } };
        const openViewer = () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'artifact_viewer.js'); m.openArtifactViewer('${hex}', { filename: 'readers.txt' }); return true; })()`);
        const closeViewer = () => ev(`document.querySelector('#artifact-viewer .artifact-viewer-close').click(); true`);
        for (const first of ['error', { status: 404, body: { error: 'not found' } }, { status: 500, body: {} }]) {
            await stubSeq([first, GOOD]);
            await openViewer(); await new Promise(r => setTimeout(r, 500)); // sleep-ok: absence of a drawing
            assert.equal(await ev(`document.querySelectorAll('#artifact-viewer .artifact-readers').length`), 0, 'no answer: not drawn');
            await closeViewer();
            await openViewer();
            await waitFor(`!!document.querySelector('#artifact-viewer .artifact-readers')`, 'a later answer shows it (the failure was not remembered)', 8000);
            await closeViewer();
        }
        await stubSeq([{ status: 403, body: { code: 'owner_only' } }, GOOD]);
        await openViewer(); await new Promise(r => setTimeout(r, 500)); await closeViewer(); await openViewer(); await new Promise(r => setTimeout(r, 500)); // sleep-ok: absence of a drawing
        assert.equal(await ev(`document.querySelectorAll('#artifact-viewer .artifact-readers').length`), 0, 'a refusal IS remembered for the page');
        await closeViewer();

        console.log('3. Refused (anyone else): nothing is drawn; hostile names are text');
        const stub = (status, body) => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'artifact_readers.js'); m._resetForTests();
            const real = window.__realFetch || (window.__realFetch = window.fetch); window.fetch = (u, o) => String(u).includes('/readers') ? Promise.resolve(new Response(JSON.stringify(${JSON.stringify(body)}), { status: ${status}, headers: { 'content-type': 'application/json' } })) : real(u, o); return true; })()`);
        await stub(403, { code: 'owner_only', error: 'owner only' });
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'artifact_viewer.js'); m.openArtifactViewer('${hex}', { filename: 'readers.txt' }); return true; })()`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: absence of a drawing; the refusal settles in a few frames
        assert.equal(await ev(`document.querySelectorAll('#artifact-viewer .artifact-readers').length`), 0, 'refused: no disclosure');
        assert.equal(await ev(`document.querySelector('#artifact-viewer .artifact-viewer-readers').hidden`), true);
        await ev(`document.querySelector('#artifact-viewer .artifact-viewer-close').click(); true`);
        await stub(200, { id: hex, machine_owner: 'reads <img src=x onerror=alert(1)> everything', owners: [{ kind: 'hub_user', id: 'ada\u202e<b>x</b>', records: 2, teams: [{ id: 't1', label: '<script>bad</script>' }], reason: 'the agents of every team this owner owns read it' }, 'junk', null], linked_teams: [{ id: 'l1', label: 'Linked <i>team</i>' }], note: 'A <b>note</b>.' });
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'artifact_viewer.js'); m.openArtifactViewer('${hex}', { filename: 'readers.txt' }); return true; })()`);
        await waitFor(`!!document.querySelector('#artifact-viewer .artifact-readers')`, 'the disclosure');
        await ev(`document.querySelector('#artifact-viewer .artifact-readers > summary').click(); true`);
        await waitFor(`document.querySelectorAll('#artifact-viewer .artifact-readers-body p').length >= 3`, 'the lines');
        const host = await ev(`(() => { const b = document.querySelector('#artifact-viewer .artifact-readers-body'); return { html: b.querySelectorAll('img, script, b, i').length, text: b.textContent }; })()`);
        assert.equal(host.html, 0, 'a hostile name is text: ' + host.text);
        assert.ok(/Coral Hub user ada<b>x<\/b>/.test(host.text) && /Linked <i>team<\/i>/.test(host.text) && /not an|A <b>note<\/b>/.test(host.text), host.text);
        console.log('   ✓');
    } finally {
        if (sid) await ev(`fetch('/api/sessions/live/${encodeURIComponent(name)}/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: '${sid}' }) }).then(r => r.status)`).catch(() => {});
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
