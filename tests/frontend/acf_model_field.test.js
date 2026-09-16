// End-to-end acceptance test for the Agent Config Form Model field
// (per-type default pre-fill, combo dropdown, dirty-flag preservation,
// terminal-hide, and cache invalidation on Settings save).
//
// Drives a running Coral server via headless Chrome + the Chrome DevTools
// Protocol so trusted user input events actually fire — crucial for the
// e.isTrusted guard on the dirty flag.
//
// Expects:
//   CORAL_URL  — base URL of a running Coral dev server (default http://127.0.0.1:8462)
//   CDP_PORT   — port of a running headless Chrome with --remote-debugging-port
//                (default 9222)
//
// Exits 0 on success, 1 on any failing scenario, 2 on harness error.

const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';

// Safety: shells spawned by a running Coral inherit CORAL_URL=http://127.0.0.1:8420
// (production). This suite mutates server state, so refuse the production port
// unless explicitly overridden. tests/frontend/run.sh always sets CORAL_URL to
// its isolated server.
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) {
    console.error(`Refusing to run against ${BASE}: that is the production Coral port. Use tests/frontend/run.sh.`);
    process.exit(2);
}
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const verdict = cond ? 'PASS' : 'FAIL';
    const line = `[${verdict}] ${label}${detail ? ' — ' + detail : ''}`;
    results.push({ verdict, label, line });
    console.log(line);
}

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Input, DOM } = client;
    await Promise.all([Page.enable(), Runtime.enable(), DOM.enable()]);

    async function putSettings(body) {
        await Runtime.evaluate({
            expression: `fetch("${BASE}/api/settings", {method:"PUT", headers:{"Content-Type":"application/json"}, body: ${JSON.stringify(JSON.stringify(body))}}).then(r=>r.ok)`,
            awaitPromise: true,
        });
    }

    async function evalInPage(expr) {
        const r = await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails));
        return r.result.value;
    }

    // Render a fresh ACF into #qa-test-acf and wait for the async pre-fill.
    async function freshACF() {
        await evalInPage(`
            (function(){
                const old = document.getElementById('qa-test-acf');
                if (old) old.remove();
                const c = document.createElement('div');
                c.id = 'qa-test-acf';
                document.body.appendChild(c);
                window.renderAgentConfigForm('qa-test-acf', { showPreset: false, showName: false });
            })();
        `);
        // Wait for the render-time _getDefaultModels()/_getAgentModels() fetches to resolve.
        await new Promise(r => setTimeout(r, 400));
    }

    async function switchType(t) {
        await evalInPage(`
            (function(){
                const s = document.querySelector('#qa-test-acf .acf-agent-type');
                s.value = ${JSON.stringify(t)};
                s.dispatchEvent(new Event('change', { bubbles: true }));
            })();
        `);
        await new Promise(r => setTimeout(r, 300));
    }

    async function modelValue() {
        return evalInPage(`document.querySelector('#qa-test-acf .acf-model').value`);
    }

    async function dirtyFlag() {
        return evalInPage(`document.querySelector('#qa-test-acf').dataset.acfModelDirty`);
    }

    await Page.navigate({ url: BASE + '/' });
    await Page.loadEventFired();
    // Give the app's JS bundle a moment to parse; renderAgentConfigForm must be present.
    await new Promise(r => setTimeout(r, 1200));

    // Baseline: clear any stale default model settings.
    await putSettings({
        default_model_claude: '',
        default_model_codex: '',
        default_model_gemini: '',
        default_model_terminal: '',
    });
    await evalInPage(`window._invalidateDefaultModels && window._invalidateDefaultModels()`);

    // ─── S1: default set → ACF opens → Model pre-fills ────────────
    await putSettings({ default_model_claude: 'claude-sonnet-4-6' });
    await evalInPage(`window._invalidateDefaultModels()`);
    await freshACF();
    check('S1 pre-fill with default set', await modelValue() === 'claude-sonnet-4-6');
    check('S1 dirty flag starts false', await dirtyFlag() === 'false');

    // ─── S2: switch to agent type with no default → clears ────────
    await switchType('gemini');
    check('S2 switch to gemini (no default) → clears', await modelValue() === '');

    // ─── S3: switch back → default re-appears ─────────────────────
    await switchType('claude');
    check('S3 switch back to claude → default re-appears', await modelValue() === 'claude-sonnet-4-6');

    // ─── S4: trusted keystrokes update value + set dirty ──────────
    // Clear programmatically first (untrusted set → no dirty flip), then type via CDP.
    await evalInPage(`document.querySelector('#qa-test-acf .acf-model').value = ''`);
    const { root } = await DOM.getDocument();
    const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: '#qa-test-acf .acf-model' });
    await DOM.focus({ nodeId });
    await Input.insertText({ text: 'my-custom-model' });
    await new Promise(r => setTimeout(r, 150));
    check('S4 trusted input updates field', await modelValue() === 'my-custom-model');
    check('S4 trusted input sets dirty=true', await dirtyFlag() === 'true');

    // ─── S5: custom value preserved across agent-type round-trip ──
    await switchType('gemini');
    await switchType('claude');
    check('S5 custom value preserved across type round-trip', await modelValue() === 'my-custom-model');

    // ─── S6: synthetic (untrusted) input event does NOT set dirty ─
    await freshACF();
    await evalInPage(`
        (function(){
            const inp = document.querySelector('#qa-test-acf .acf-model');
            inp.value = 'programmatic';
            inp.dispatchEvent(new InputEvent('input', { bubbles: true, data: 'p' }));
        })();
    `);
    check('S6 synthetic InputEvent (isTrusted=false) does NOT dirty the flag', await dirtyFlag() === 'false');

    // ─── S7: terminal agent → wrapper hidden + input zeroed ───────
    await switchType('terminal');
    const hidden = await evalInPage(`document.querySelector('#qa-test-acf .acf-model-wrap').style.display`);
    check('S7 terminal agent → wrapper hidden', hidden === 'none', `display='${hidden}'`);
    check('S7 terminal agent → input cleared', await modelValue() === '');

    // ─── S8: clearing default + invalidate → fresh ACF empty ──────
    await putSettings({ default_model_claude: '' });
    await evalInPage(`window._invalidateDefaultModels()`);
    await freshACF();
    check('S8 after clearing default → fresh ACF has empty field', await modelValue() === '');

    // ─── S9: new default + invalidate → ACF picks up new value ────
    await putSettings({ default_model_claude: 'claude-haiku-4-5-20251001' });
    await evalInPage(`window._invalidateDefaultModels()`);
    await freshACF();
    check('S9 cache invalidation picks up new default', await modelValue() === 'claude-haiku-4-5-20251001');

    // ─── S10: restart (setAgentConfig with stored model) → preserved ─
    await evalInPage(`
        (function(){
            const old = document.getElementById('qa-test-acf');
            if (old) old.remove();
            const c = document.createElement('div');
            c.id = 'qa-test-acf';
            document.body.appendChild(c);
            window.renderAgentConfigForm('qa-test-acf', { showPreset: false, showName: false });
            window.setAgentConfig('qa-test-acf', { agentType: 'claude', model: 'claude-opus-4-7' });
        })();
    `);
    await new Promise(r => setTimeout(r, 300));
    check('S10 setAgentConfig with stored model → dirty=true', await dirtyFlag() === 'true');
    check('S10 setAgentConfig with stored model → value preserved', await modelValue() === 'claude-opus-4-7');
    await switchType('gemini');
    check('S10 dirty=true preserves value across type switch', await modelValue() === 'claude-opus-4-7');

    await client.close();

    const failures = results.filter(r => r.verdict === 'FAIL');
    console.log(`\n=== SUMMARY ===`);
    console.log(`total: ${results.length}, failures: ${failures.length}`);
    if (failures.length) {
        process.exit(1);
    }
}

run().catch(e => {
    console.error('harness error:', e);
    process.exit(2);
});
