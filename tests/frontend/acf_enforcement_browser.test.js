// The agent form says what the chosen CLI enforces (#4545, Security's #4550): Codex keeps only a sandbox and an
// approval mode and Antigravity only a mode, so for them new Deny entries can't be added (an existing one can be
// removed) and shell patterns are marked as not enforced; Claude's line says Allow restricts nothing in Bypass
// Permissions mode; the line follows the agent type, the permission mode and the chips, and the Launch dialog
// shows it under the persona too.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const c = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const ev = async expression => {
        const r = await c.Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    const shot = async name => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        const { data } = await c.Page.captureScreenshot({ format: 'png' });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from(data, 'base64'));
    };
    const line = () => ev(`(() => { const l = document.querySelector('#fixture .acf-perms-enforced'); return l.hidden ? null : l.textContent; })()`);
    const deny = () => ev(`[...document.querySelectorAll('#fixture [data-type="deny"]')].map(b => ({ cap: b.dataset.cap, disabled: b.disabled, active: b.classList.contains('active') }))`);
    const setType = t => ev(`(() => { const s = document.querySelector('#fixture .acf-agent-type'); s.value = ${JSON.stringify(t)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
    const setMode = m => ev(`(() => { const s = document.querySelector('#fixture .acf-permission-mode'); s.value = ${JSON.stringify(m)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
    const chip = (type, cap) => ev(`document.querySelector('#fixture [data-type="${type}"][data-cap="${cap}"]').click(); true`);
    try {
        await c.Page.enable();
        await c.Emulation.setDeviceMetricsOverride({ width: 1100, height: 900, deviceScaleFactor: 1, mobile: false });
        await c.Page.navigate({ url: BASE + '/' }); await c.Page.loadEventFired();
        await coralReady();
        await ev(`(async () => { await (await import((window.__coralStatic || '/static/') + 'lazy_panels.js')).loadModals();
            const p = document.createElement('div'); p.id = 'fixture'; p.className = 'modal-content';
            p.style.cssText = 'position:fixed;top:20px;left:20px;width:620px;height:860px;overflow:auto;z-index:99999;background:var(--bg-primary);padding:16px';
            document.body.append(p);
            const { AGENT_PRESETS } = await import((window.__coralStatic || '/static/') + 'agent_presets.js');
            window.__presets = Object.fromEntries(AGENT_PRESETS.map(x => [x.name, x.capabilities]));
            window.renderAgentConfigForm('fixture', { showPreset: false, showName: false, value: { agentType: 'codex', capabilities: window.__presets['Frontend Dev'] } });
            document.querySelector('#fixture .permissions-toggle-btn').click(); return true; })()`);

        console.log('Codex, Frontend Dev: a sandbox and an approval mode only; Deny and patterns marked');
        let t = await line();
        assert.match(t, /^Codex keeps only a sandbox and an approval mode: a sandbox that can write the working folder, and it asks only when the model requests it/);
        assert.match(t, /web search on.*Deny entries and shell patterns are not enforced/);
        assert.ok((await deny()).every(d => d.disabled), 'no new Deny entry for Codex');
        await chip('deny', 'shell');
        assert.ok((await deny()).every(d => !d.active), 'a disabled Deny chip does nothing');
        assert.equal(await ev(`document.querySelector('#fixture .perms-patterns-note').textContent`), 'Not enforced by this agent');
        assert.equal(await ev(`document.querySelector('#fixture .perms-deny-note').textContent`), 'Not enforced by this agent');
        await shot('acf-enforcement-codex');

        console.log('Claude: Allow pre-approves; in Bypass Permissions mode it restricts nothing');
        await setType('claude'); await setMode('default');
        assert.match(await line(), /^Claude: Allow pre-approves these tools and others still run after it asks; Deny blocks\.$/);
        assert.ok((await deny()).every(d => !d.disabled), 'Claude enforces Deny');
        assert.equal(await ev(`document.querySelector('#fixture .perms-patterns-note').textContent`), '');
        await setMode('bypassPermissions');
        assert.match(await line(), /Bypass Permissions mode: nothing asks, so Allow restricts nothing; only Deny blocks/);

        console.log('Antigravity: a mode only, and the line follows the chips');
        await setType('agy');
        assert.match(await line(), /^Antigravity keeps only a mode: edits are approved without asking \(--mode accept-edits\)\./);
        await chip('allow', 'shell');
        assert.match(await line(), /^Antigravity keeps only a mode: nothing asks \(--dangerously-skip-permissions\)\./, 'Shell with Write Files: skip-permissions');

        console.log('An existing Deny entry on Codex can be removed, not added to');
        await ev(`window.__coral.setAgentConfig('fixture', { agentType: 'codex', capabilities: { allow: ['file_read', 'shell'], deny: ['git_write'] } }); true`);
        let d = await deny();
        assert.deepEqual(d.filter(x => !x.disabled).map(x => x.cap), ['git_write'], 'only the active entry stays clickable');
        assert.match(await line(), /a sandbox that can write the working folder, and it asks only when the model requests it/, 'a Deny entry changes the mode');
        await chip('deny', 'git_write');
        d = await deny();
        assert.ok(d.every(x => x.disabled && !x.active), 'removed, and now none can be added');
        assert.match(await line(), /and it never asks/, 'without the Deny entry: never asks');

        console.log('Lead Developer on Codex: a workspace-write sandbox that never asks (no sandbox is an explicit mode only); Terminal: no line');
        await ev(`window.__coral.setAgentConfig('fixture', { agentType: 'codex', capabilities: window.__presets['Lead Developer'] }); true`);
        assert.match(await line(), /a sandbox that can write the working folder, and it never asks/);
        assert.doesNotMatch(await line(), /without a sandbox/);
        assert.match(await ev(`document.querySelector('#fixture [data-type="allow"][data-cap="git_write"]').title`), /Codex: Git Write does not change its sandbox/);
        await setType('terminal');
        assert.equal(await line(), null);

        console.log('The Launch dialog says it on Permissions only, not again under the persona (#6012)');
        await ev(`window.renderAgentConfigForm('launch-agent-acf', { showPreset: false, showName: false, value: { agentType: 'codex', capabilities: window.__presets['Security Reviewer'] } }); true`);
        assert.equal(await ev(`!!document.querySelector('[data-acf-enforced-for]')`), false, 'no second copy under the persona');
        assert.match(await ev(`document.querySelector('#launch-agent-acf .acf-perms-enforced').textContent`), /a read-only sandbox/);
        console.log('acf_enforcement: ok');
        await c.close(); process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await c.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
