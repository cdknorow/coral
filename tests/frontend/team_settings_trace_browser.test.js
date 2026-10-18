// Team settings, step by step (#4653): showTeamWorkingMode (team_working_mode.js) is one 356-line function (the dialog, its tabs, the preset editor, the load and the save in one closure), and
// splitting it must change nothing. This opens it as the sidebar's workspace and as a dialog, with every answer of its endpoints (all good; the working mode, the presets, the global settings, the
// prompt inspection or the board settings failing), and does what a person does: the tabs by click and by Arrow, Home and End, the preset list, a new preset, Save preset and Reset, Save with a
// change and with none, Enter in the preset name. After each step it records the dialog's markup (hashed), its status line, and the requests the step sent (method, address, body). The
// whole trace is compared with fixtures/team_settings_trace.json (sha256 per step; the steps' readable lines are in the file too, so a change can be read). CORAL_UPDATE_GOLDEN=1 rewrites it.
// Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'team_settings_trace.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const trace = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        // One scenario in the page: stubs the endpoints, opens the dialog, runs the steps, returns [{step, html, status, requests}].
        await ev(`window.__trace = async (mode, answers, script) => {
            const st = window.__coralStatic || '/static/';
            const m = await import(st + 'team_working_mode.js');
            document.getElementById('team-working-mode-dialog')?.remove(); document.getElementById('team-settings-workspace')?.remove();
            let host = document.getElementById('agentic-panel-team-settings'); if (!host) { host = document.createElement('div'); host.id = 'agentic-panel-team-settings'; document.body.append(host); }
            host.replaceChildren();
            const reqs = [];
            const preset = (id, o = {}) => ({ id, name: id === 'none' ? 'None' : id === 'worktrees' ? 'Worktrees' : id, instructions: undefined, default_instructions: 'Default for ' + id, builtin: ['none', 'worktrees', 'shared_checkout'].includes(id), overridden: false, ...o });
            const state = { wm: { mode: 'none', dependency_guidance: false, custom_instructions: '' }, presets: [preset('none'), preset('shared_checkout'), preset('worktrees', { overridden: true, instructions: 'Local worktrees text' })] };
            const real = window.fetch;
            const json = (body, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => {
                const url = String(u), method = (o.method || 'GET').toUpperCase();
                reqs.push(method + ' ' + url + (o.body ? ' ' + o.body : ''));
                if (/\\/working-mode\\/presets\\/[^/]+\\/reset$/.test(url)) return answers.reset === 500 ? json({ error: 'reset failed' }, 500) : json({ preset: { ...state.presets.find(p => url.includes('/presets/' + p.id + '/')), overridden: false, instructions: undefined } });
                if (/\\/working-mode\\/presets\\/[^/?]+$/.test(url) && method === 'PUT') return answers.presetSave === 500 ? json({ error: 'preset not saved' }, 500) : json({ preset: { ...JSON.parse(o.body), id: url.split('/').pop() } });
                if (/\\/working-mode\\/presets$/.test(url)) return method === 'POST' ? (answers.presetSave === 500 ? json({ error: 'preset not saved' }, 500) : json({ preset: { ...JSON.parse(o.body), builtin: false } })) : (answers.presets === 500 ? json({ error: 'no presets' }, 500) : json({ presets: state.presets }));
                if (/\\/working-mode$/.test(url)) return method === 'PUT' ? (answers.wmSave === 500 ? json({ error: 'mode not saved' }, 500) : json({ ...state.wm, ...JSON.parse(o.body) })) : (answers.wm === 500 ? json({ error: 'no mode' }, 500) : json(state.wm));
                if (/\\/api\\/settings\\/prompt-inspection/.test(url)) return answers.inspect === 500 ? json({}, 500) : json({ orchestrator: { scope: 'team', applies: 'orchestrators', default_prompt: 'Default O', effective_prompt: 'Effective O' }, worker: { scope: 'global' }, task: { default_instructions: 'Task defaults' } });
                if (/\\/api\\/board\\/[^/]+\\/settings$/.test(url)) return method === 'PUT' ? (answers.boardSave === 500 ? json({ error: 'no' }, 500) : json({})) : (answers.board === 404 ? json({}, 404) : json({ cross_team_messaging: true, cross_team_artifacts: false }));
                if (/\\/api\\/settings$/.test(url)) return method === 'PUT' ? (answers.settingsSave === 500 ? json({}, 500) : json({})) : (answers.settings === 500 ? json({}, 500) : json({ settings: { board_health_monitor: 'false' } }));
                return real(u, o);
            };
            const q = (s, r = document) => r.querySelector(s);
            const root = () => document.getElementById('team-settings-workspace') || document.getElementById('team-working-mode-dialog');
            const norm = t => t.replace(/custom-[0-9a-z]+/g, 'custom-X').replace(/board-rename-([a-z]+)-\\d+/g, 'board-rename-$1-N');
            const out = [];
            let seen = 0;
            const record = step => out.push({ step, html: norm(root() ? root().outerHTML : ''), status: root() ? q('.working-mode-status', root()).textContent : '', form: root() && q('form', root()) ? norm(JSON.stringify([...q('form', root()).elements].filter(e => e.name).map(e => [e.name, e.type === 'checkbox' ? e.checked : e.value, e.disabled, e.readOnly]))) : '', requests: norm(reqs.slice(seen).filter(r => !/\\/api\\/system\\/auto-wake/.test(r)).join('\\n')) });
            // (the page's own auto-wake poll comes when it comes, not from the dialog: it is left out of the requests)
            const flush = () => { seen = reqs.length; };
            const settle = () => new Promise(r => setTimeout(r, 120)); // sleep-ok: each step's fetches and redraws settle over frames; there is no completion event
            try {
                await m.showTeamWorkingMode('team-x', { workspace: mode === 'workspace' });
                for (let i = 0; i < 100 && !(root() && q('[type=submit]', root()) && (!q('[type=submit]', root()).disabled || answers.wm === 500 || answers.presets === 500)); i++) await settle();
                await settle();
                record('opened and loaded'); flush();
                await script({ q, root, record, flush, settle, out, reqs });
            } finally { window.fetch = real; }
            return out;
        }; true`);
        const SCRIPTS = {
            tabs: `async ({ q, root, record, flush, settle }) => {
                for (const t of ['prompts', 'global', 'workflow']) { q('[data-settings-tab=' + t + ']', root()).click(); await settle(); record('tab ' + t); flush(); }
                const key = (el, k) => el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true }));
                for (const k of ['ArrowRight', 'ArrowRight', 'ArrowRight', 'ArrowLeft', 'End', 'Home']) { key(q('[data-settings-tab][aria-selected=true]', root()), k); await settle(); record('key ' + k); }
            }`,
            presets: `async ({ q, root, record, flush, settle }) => {
                const f = q('form', root());
                const sel = f.elements.preset; sel.value = 'shared_checkout'; sel.dispatchEvent(new Event('change', { bubbles: true })); await settle(); record('preset shared_checkout'); flush();
                sel.value = 'worktrees'; sel.dispatchEvent(new Event('change', { bubbles: true })); await settle(); record('preset worktrees'); flush();
                q('.preset-new', root()).click(); await settle(); record('new preset'); flush();
                f.elements.preset_name.value = ''; q('.preset-save', root()).click(); await settle(); record('save with no name'); flush();
                f.elements.preset_name.value = 'My preset'; q('.preset-save', root()).click(); await settle(); record('save with no instructions'); flush();
                f.elements.preset_instructions.value = 'Do the thing'; f.elements.preset_name.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); await settle(); record('save by Enter'); flush();
                sel.value = 'worktrees'; sel.dispatchEvent(new Event('change', { bubbles: true })); await settle();
                f.elements.preset_instructions.value = 'Changed worktrees text'; q('.preset-save', root()).click(); await settle(); record('save built-in override'); flush();
                q('.preset-reset', root()).click(); await settle(); record('reset built-in'); flush();
            }`,
            save: `async ({ q, root, record, flush, settle }) => {
                const f = q('form', root());
                f.requestSubmit(); await settle(); record('save with nothing changed'); flush();
                f.elements.mode.value = 'worktrees'; f.elements.dependency_guidance.checked = true; f.elements.custom_instructions.value = 'Team rule'; f.requestSubmit(); await settle(); record('save the working mode'); flush();
                f.elements.cross_team_messaging.checked = !f.elements.cross_team_messaging.checked; f.elements.board_health_monitor.checked = !f.elements.board_health_monitor.checked; f.requestSubmit(); await settle(); record('save the board and global settings'); flush();
                f.elements.mode.value = 'none'; f.requestSubmit(); await settle(); record('save again'); flush();
            }`,
            nothing: `async ({ record }) => { record('nothing more'); }`,
        };
        const SCEN = [
            ['workspace, all good, tabs', 'workspace', {}, 'tabs'],
            ['dialog, all good, tabs', 'dialog', {}, 'tabs'],
            ['workspace, presets', 'workspace', {}, 'presets'],
            ['dialog, presets', 'dialog', {}, 'presets'],
            ['workspace, save', 'workspace', {}, 'save'],
            ['dialog, save', 'dialog', {}, 'save'],
            ['preset save fails', 'workspace', { presetSave: 500, reset: 500 }, 'presets'],
            ['save fails (mode, board, global)', 'workspace', { wmSave: 500, boardSave: 500, settingsSave: 500 }, 'save'],
            ['global settings fail to load', 'workspace', { settings: 500 }, 'save'],
            ['board settings missing', 'workspace', { board: 404 }, 'save'],
            ['inspection fails', 'workspace', { inspect: 500 }, 'nothing'],
            ['working mode fails to load', 'workspace', { wm: 500 }, 'nothing'],
            ['presets fail to load', 'dialog', { presets: 500 }, 'nothing'],
        ];
        for (const [name, mode, answers, script] of SCEN) {
            trace[name] = await ev(`window.__trace(${JSON.stringify(mode)}, ${JSON.stringify(answers)}, ${SCRIPTS[script]})`);
            assert.ok(trace[name].length >= 1 && trace[name][0].html.length > 500, `${name}: the dialog drew`);
        }
        // The same twice: nothing in it depends on the clock.
        const again = await ev(`window.__trace('workspace', {}, ${SCRIPTS.tabs})`);
        const first = trace['workspace, all good, tabs'];
        for (let i = 0; i < first.length; i++) for (const f of ['html', 'status', 'form', 'requests']) if (again[i][f] !== first[i][f]) { let j = 0; while (j < first[i][f].length && first[i][f][j] === again[i][f][j]) j++; assert.fail(`not the same twice: step ${first[i].step}, ${f}, at ${j}:\n  1: ${first[i][f].slice(Math.max(0, j - 60), j + 120)}\n  2: ${again[i][f].slice(Math.max(0, j - 60), j + 120)}`); }
        assert.equal(sha(JSON.stringify(again)), sha(JSON.stringify(first)), 'the same trace twice');
        const lines = {}; const hashes = {};
        for (const [name, steps] of Object.entries(trace)) {
            hashes[name] = steps.map(s => ({ step: s.step, sha256: sha(JSON.stringify(s)) }));
            lines[name] = steps.map(s => `${s.step} | status: ${s.status} | requests: ${s.requests.replace(/\n/g, ' ; ')}`);
        }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what showTeamWorkingMode did, step by step, for team_settings_trace_browser.test.js: sha256 of each step (markup, status, form, requests) and its readable line', hashes, lines, sample: trace['workspace, all good, tabs'][0].html }, null, 1) + '\n');
            console.log('golden written: ' + Object.values(hashes).reduce((a, h) => a + h.length, 0) + ' steps');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same scenarios');
        for (const k of Object.keys(want.hashes)) {
            assert.equal(hashes[k].length, want.hashes[k].length, `${k}: the same number of steps`);
            hashes[k].forEach((h, i) => {
                if (h.sha256 !== want.hashes[k][i].sha256) assert.fail(`${k} / ${h.step}: changed.\n  now:  ${lines[k][i]}\n  was:  ${want.lines[k][i]}\n  (a markup or form change shows as the same line: CORAL_UPDATE_GOLDEN=1 on the earlier commit and this one, then diff)`);
            });
        }
        console.log(`PASS team settings trace: ${Object.values(want.hashes).reduce((a, h) => a + h.length, 0)} steps identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
