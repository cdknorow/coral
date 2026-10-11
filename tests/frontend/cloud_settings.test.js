// Unit test for the hub's per-user settings helpers (jsdom, no browser):
//   node cloud_settings.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');
const { JSDOM } = require('jsdom');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    const dom = new JSDOM(`<!doctype html><body><div id="settings-modal"><dl class="settings-grid">
      <dt>Theme</dt><dd><select id="settings-theme"></select></dd>
      <dt>Default Working Directory</dt><dd><input id="settings-working-dir"></dd>
      <dt>CLI Paths</dt><dd><input id="settings-cli-path-claude"></dd><dd><span>more</span></dd>
      <dt>Fit</dt><dd><input type="checkbox" id="settings-fit-pane-width"></dd>
      <dt>Remote</dt><dd><input type="checkbox" id="settings-remote-access-enabled"></dd></dl><div id="license-section"></div></div></body>`, { url: 'https://hub.example/' });
    for (const k of ['window', 'document']) Object.defineProperty(globalThis, k, { value: dom.window[k], configurable: true, writable: true });
    const m = await import(pathToFileURL(path.join(dir, 'cloud_settings.js')).href);

    // keys
    const { kept, dropped } = m.filterSettingsPayload({ theme: 'dark', default_agent_type: 'codex', terminal_font_size: '14', cli_path_claude: '/x', default_working_dir: '/', remote_access_enabled: true, __proto__x: 1, macros: '[]' });
    assert.deepStrictEqual(Object.keys(kept).sort(), ['default_agent_type', 'macros', 'theme']);
    assert.deepStrictEqual(dropped.sort(), ['__proto__x', 'cli_path_claude', 'default_working_dir', 'remote_access_enabled', 'terminal_font_size']);
    assert.deepStrictEqual(m.filterSettingsPayload(null), { kept: {}, dropped: [] });
    for (const machine of ['default_working_dir', 'remote_access_enabled', 'telemetry_enabled', 'cli_path_claude', 'api_key', 'hooks']) assert.ok(!m.HUB_SETTING_KEYS.has(machine), machine);

    // messages
    assert.match(m.settingsErrorMessage(413, {}), /too large/);
    assert.match(m.settingsErrorMessage(400, { error: 'unknown_setting' }), /kept on the machine/);
    assert.match(m.settingsErrorMessage(400, { error: 'invalid_value', key: 'theme' }), /not allowed for theme/);
    assert.ok(!m.settingsErrorMessage(400, { error: 'invalid_value', key: 'x'.repeat(500) }).includes('x'.repeat(61)));
    assert.strictEqual(m.settingsErrorMessage(400, { error: 'other' }), '');
    assert.strictEqual(m.settingsErrorMessage(500, {}), '');

    // dialog: rows with no hub control are hidden, the note is added once
    m.applyCloudSettingsDialog(); m.applyCloudSettingsDialog();
    const rows = Object.fromEntries([...document.querySelectorAll('dt')].map(dt => [dt.textContent, dt.hidden]));
    assert.deepStrictEqual(rows, { Theme: false, 'Default Working Directory': true, 'CLI Paths': true, Fit: false, Remote: true });
    assert.strictEqual(document.getElementById('settings-working-dir').closest('dd').hidden, true);
    assert.strictEqual(document.querySelector('span').closest('dd').hidden, true, 'all dd rows of a hidden dt are hidden');
    assert.strictEqual(document.getElementById('license-section').hidden, true);
    assert.strictEqual(document.querySelectorAll('.settings-cloud-note').length, 1);
    console.log('cloud_settings.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
