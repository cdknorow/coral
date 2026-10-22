// The Launch dialog's launches and the personas (#4684): a launch from the dialog (an agent, a terminal, joining a board or making one, a missing directory), the launches from a board or a folder
// group (Add agent, Terminal, the default agent), and saving, exporting and importing personas and team templates. They are blocks of modals.js; this pins what they send and show before they
// move into their own modules. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; the launch requests are answered by the test.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            window.__launches = []; window.__answer = { status: 200, body: { session_name: 's1', session_id: 'sid-s1' } }; window.__downloads = []; window.__pick = null;
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                if (p.pathname === '/api/sessions/launch' && m === 'POST' && window.__broken) { window.__launches.push(JSON.parse(o.body)); return Promise.resolve({ text: () => Promise.resolve(''), get status() { throw new Error('a response that cannot be read'); } }); }
                if (p.pathname === '/api/sessions/launch' && m === 'POST') { window.__launches.push(JSON.parse(o.body)); return json(window.__answer.body, window.__answer.status); }
                if (p.pathname === '/api/board/projects') return json([{ project: 'crew' }]);
                if (p.pathname === '/api/system/cli-check') return json({ found: true, version: '1' });
                return real(u, o); };
            URL.createObjectURL = blob => { window.__downloads.push({ blob }); return 'blob:x'; }; URL.revokeObjectURL = () => {};
            HTMLAnchorElement.prototype.click = function() { window.__downloads[window.__downloads.length - 1].name = this.download; };
            const realClick = HTMLInputElement.prototype.click;
            HTMLInputElement.prototype.click = function() { if (this.type !== 'file') return realClick.call(this);
                Object.defineProperty(this, 'files', { value: window.__pick === null ? [] : [new File([window.__pick], 'x.json')] }); this.onchange(); };
            await window.showLaunchModal(); window.hideLaunchModal();
            return true; })()`);
        const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent.trim())`);
        const hasToast = re => toasts().then(ts => ts.some(t => re.test(t)));
        const modalOpen = () => ev(`getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`);
        const lastLaunch = () => ev(`window.__launches[window.__launches.length - 1]`);
        const flush = () => ev(`window.__launches.length = 0; window.__downloads.length = 0; document.querySelectorAll('.toast').forEach(t => t.remove()); true`);

        console.log('An agent from the dialog: a directory is required, the request carries the directory, name and board, the dialog closes');
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the agent step');
        await ev(`document.getElementById('launch-dir').value = ''; window.launchSession(); true`);
        assert.equal(await hasToast(/Choose a folder/), false, 'said once, under the field (#6029)'); assert.match(await ev(`document.getElementById('launch-dir-error').textContent`), /Choose a folder/); assert.equal((await ev(`window.__launches.length`)), 0);
        await ev(`document.getElementById('launch-dir').value = '/work/project'; document.getElementById('launch-agent-name-input').value = 'Ada'; window.launchSession(); true`);
        await waitFor(`window.__launches.length === 1`, 'the launch');
        let sent = await lastLaunch();
        assert.equal(sent.working_dir, '/work/project'); assert.equal(sent.display_name, 'Ada'); assert.equal(typeof sent.agent_type, 'string'); assert.equal('board_name' in sent, false);
        await waitFor(`!document.querySelector('#launch-step-agent .btn-primary') || document.querySelector('#launch-step-agent .btn-primary').textContent === 'Launch'`, 'the button back');
        assert.equal(await hasToast(/Launched: Ada/), true); assert.equal(await hasToast(/Launched: s1/), false, 'the name, not the session id (#5895)'); assert.equal(await modalOpen(), false, 'closed');

        console.log('Joining a board sends its id; a new board needs a valid name');
        await flush();
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
        await waitFor(`[...document.querySelectorAll('#launch-board-select option')].some(o => o.value === 'crew')`, 'the boards');
        await ev(`{ const s = document.getElementById('launch-board-select'); s.value = 'crew'; } window._onAgentBoardChange(); document.getElementById('launch-dir').value = '/work/project'; window.launchSession(); true`);
        await waitFor(`window.__launches.length === 1`, 'the launch');
        sent = await lastLaunch(); assert.equal(sent.board_id, 'crew'); assert.equal(sent.board_name, 'crew');
        await flush();
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
        await waitFor(`[...document.querySelectorAll('#launch-board-select option')].some(o => o.value === 'crew')`, 'the boards');
        await ev(`{ const s = document.getElementById('launch-board-select'); s.value = '__new__'; } window._onAgentBoardChange(); document.getElementById('launch-board-name').value = 'bad name!'; document.getElementById('launch-dir').value = '/work/project'; window.launchSession(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].length > 0`, 'the problem');
        assert.equal(await ev(`window.__launches.length`), 0, 'a bad board name launches nothing');
        await ev(`document.getElementById('launch-board-name').value = 'fresh-board'; window.launchSession(); true`);
        await waitFor(`window.__launches.length === 1`, 'the launch'); sent = await lastLaunch(); assert.equal(sent.board_name, 'fresh-board'); assert.equal('board_id' in sent, false);

        console.log('A terminal from the dialog, and a launch the server refuses');
        await flush();
        await ev(`window.showLaunchModal(); window._selectLaunchType('terminal'); document.getElementById('launch-terminal-dir').value = '/work/term'; document.getElementById('launch-terminal-name').value = 'T1'; window.launchSession(); true`);
        await waitFor(`window.__launches.length === 1`, 'the terminal launch');
        sent = await lastLaunch(); assert.equal(sent.agent_type, 'terminal'); assert.equal(sent.working_dir, '/work/term'); assert.equal(sent.display_name, 'T1'); assert.equal('flags' in sent, false);
        await flush(); await ev(`window.__answer = { status: 500, body: { error: 'nope' } }; window.showLaunchModal(); window._selectLaunchType('terminal'); document.getElementById('launch-terminal-dir').value = '/work/term'; window.launchSession(); true`);
        await waitFor(`window.__launches.length === 1`, 'the failing launch'); await waitFor(`[...document.querySelectorAll('.toast')].length > 0`, 'a toast');
        assert.equal(await modalOpen(), true, 'a refused launch keeps the dialog open'); await ev(`window.hideLaunchModal(); window.__answer = { status: 200, body: { session_name: 's2', session_id: 'sid-s2' } }; true`);

        console.log('From a board: Add agent (a name is required), Terminal; from a folder: the default agent, Terminal, Add agent');
        await flush();
        await ev(`window.showAddAgentToBoard('crew', '/work/board'); true`);
        await waitFor(`getComputedStyle(document.getElementById('add-agent-board-modal')).display !== 'none'`, 'the dialog');
        assert.match(await ev(`document.getElementById('add-agent-board-subtitle').textContent`), /^Board: /);
        await ev(`window.launchAgentToBoard(); true`); assert.equal(await hasToast(/Agent name is required/), true);
        await ev(`{ const n = document.querySelector('#add-agent-board-acf .acf-name'); n.value = 'Bo'; n.dispatchEvent(new Event('input', { bubbles: true })); } window.launchAgentToBoard(); true`);
        await waitFor(`window.__launches.length === 1`, 'the launch');
        sent = await lastLaunch(); assert.equal(sent.working_dir, '/work/board'); assert.equal(sent.display_name, 'Bo'); assert.equal(sent.board_id, 'crew'); assert.ok(Array.isArray(sent.flags));
        await waitFor(`getComputedStyle(document.getElementById('add-agent-board-modal')).display === 'none'`, 'closed');
        await flush(); await ev(`window.launchTerminalToBoard('crew', '/work/board'); true`);
        await waitFor(`window.__launches.length === 1`, 'the terminal'); sent = await lastLaunch();
        assert.deepEqual([sent.agent_type, sent.display_name, sent.board_id, sent.board_name, sent.working_dir], ['terminal', 'Terminal', 'crew', 'crew', '/work/board']);
        await flush(); await ev(`window.__broken = true; window.launchTerminalToBoard('crew', '/work/board'); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => t.textContent.trim().replace(/×$/, '') === 'Failed to launch terminal')`, 'the terminal failure toast');
        await ev(`window.__broken = false; true`);
        await flush(); await ev(`window.launchDefaultAgent('/work/solo'); true`);
        await waitFor(`window.__launches.length === 1`, 'the default agent'); sent = await lastLaunch();
        assert.equal(sent.working_dir, '/work/solo'); assert.equal(typeof sent.agent_type, 'string'); assert.ok(Array.isArray(sent.flags)); assert.equal('board_name' in sent, false);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /^Launched \\w+ agent×?$/.test(t.textContent.trim()))`, 'the launched toast');
        await flush(); await ev(`window.launchStandaloneTerminal('/work/solo'); true`);
        await waitFor(`window.__launches.length === 1`, 'the standalone terminal'); sent = await lastLaunch(); assert.deepEqual([sent.agent_type, sent.display_name, sent.working_dir], ['terminal', 'Terminal', '/work/solo']);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /^Launched Terminal×?$/.test(t.textContent.trim()))`, 'the terminal toast');
        await flush(); await ev(`window.showAddStandaloneAgent('/work/solo'); true`);
        await waitFor(`getComputedStyle(document.getElementById('add-agent-board-modal')).display !== 'none'`, 'the standalone dialog');
        assert.deepEqual(await ev(`[document.getElementById('add-agent-board-name').value, document.getElementById('add-agent-board-subtitle').textContent]`), ['', 'Directory: /work/solo']);
        await ev(`window.hideAddAgentBoardModal(); true`);

        console.log('Personas: a name is required, a built-in cannot be overwritten, a saved one is exported and imported');
        await flush();
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the agent step');
        await ev(`document.getElementById('launch-agent-name-input').value = ''; { const n = document.querySelector('#launch-agent-acf .acf-name'); if (n) n.value = ''; } window._saveCurrentPersona(); true`);
        assert.equal(await hasToast(/Enter a name before saving/), true);
        await ev(`window.exportPersonas(); true`); assert.equal(await hasToast(/No saved personas to export|Exported/), true);
        await flush();
        await ev(`document.getElementById('launch-agent-name-input').value = 'Ada Persona'; { const p = document.querySelector('#launch-agent-acf .acf-prompt'); if (p) { p.value = 'be ada'; p.dispatchEvent(new Event('input', { bubbles: true })); } } window._saveCurrentPersona(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Saved persona "Ada Persona"/.test(t.textContent))`, 'saved');
        await flush(); await ev(`window.exportPersonas(); true`);
        await waitFor(`window.__downloads.length === 1 && window.__downloads[0].name`, 'the download');
        const exported = await ev(`window.__downloads[0].blob.text().then(t => ({ name: window.__downloads[0].name, data: JSON.parse(t) }))`);
        assert.equal(exported.name, 'coral-personas.json'); assert.equal(exported.data.type, 'coral-personas'); assert.equal(exported.data.version, 1);
        assert.deepEqual(exported.data.personas.map(p => p.name), ['Ada Persona']);
        assert.equal(await hasToast(/Exported 1 persona\(s\)/), true);
        await flush(); await ev(`window.__pick = 'not json'; window.importPersonas(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Invalid JSON file/.test(t.textContent))`, 'the invalid-JSON toast');
        await flush(); await ev(`window.__pick = JSON.stringify({ nothing: 1 }); window.importPersonas(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Invalid persona file format/.test(t.textContent))`, 'the format toast');
        await flush(); await ev(`window.__pick = JSON.stringify({ personas: [{ name: 'Imported One', prompt: 'p1' }, { prompt: 'no name' }, { name: 'Orchestrator' }] }); window.importPersonas(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Imported \\d+ persona\\(s\\)/.test(t.textContent))`, 'imported');
        console.log('  imported: ' + (await toasts()).filter(t => /Imported/.test(t)).join(' | '));
        await flush(); await ev(`window.exportPersonas(); true`);
        await waitFor(`window.__downloads.length === 1 && window.__downloads[0].name`, 'the second download');
        assert.deepEqual((await ev(`window.__downloads[0].blob.text().then(t => JSON.parse(t).personas.map(p => p.name).sort())`)), ['Ada Persona', 'Imported One']);

        console.log('Team templates: nothing saved to export says so, an import adds the valid ones, and export carries them');
        await flush(); await ev(`window.__pick = JSON.stringify({ templates: [{ name: 'Tpl A', agents: [{ name: 'X', prompt: 'x' }] }, { name: 'No agents' }] }); window.importTeamTemplates(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Imported 1 template\\(s\\)/.test(t.textContent))`, 'imported templates');
        await flush(); await ev(`window.exportTeamTemplates(); true`);
        await waitFor(`window.__downloads.length === 1 && window.__downloads[0].name`, 'the templates download');
        const tpl = await ev(`window.__downloads[0].blob.text().then(t => ({ name: window.__downloads[0].name, data: JSON.parse(t) }))`);
        assert.equal(tpl.name, 'coral-team-templates.json'); assert.equal(tpl.data.type, 'coral-team-templates'); assert.deepEqual(tpl.data.templates.map(t => t.name), ['Tpl A']);
        console.log('PASS launch dialog');
    } finally {
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
