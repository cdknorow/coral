const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime } = client;

  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };

  try {
    await Page.enable();
    await Page.navigate({ url: BASE });
    await Page.loadEventFired();

    // 1. Accessibility and element presence verification on real rendered template
    const elementsExist = await ev(`(() => {
      const tel = document.getElementById('settings-telemetry-enabled');
      const rem = document.getElementById('settings-remote-access-enabled');
      const status = document.getElementById('settings-remote-access-status');
      const help = document.getElementById('settings-telemetry-help');
      return {
        telemetryFound: !!tel,
        remoteFound: !!rem,
        statusFound: !!status,
        helpFound: !!help,
        telemetryAria: tel?.getAttribute('aria-describedby'),
        remoteAria: rem?.getAttribute('aria-describedby'),
        statusRole: status?.getAttribute('role'),
      };
    })()`);

    assert.equal(elementsExist.telemetryFound, true, 'telemetry checkbox must exist');
    assert.equal(elementsExist.remoteFound, true, 'remote access checkbox must exist');
    assert.equal(elementsExist.statusFound, true, 'remote access status element must exist');
    assert.equal(elementsExist.helpFound, true, 'telemetry help element must exist');
    assert.equal(elementsExist.telemetryAria, 'settings-telemetry-help', 'telemetry aria-describedby');
    assert.equal(elementsExist.remoteAria, 'settings-remote-access-status', 'remote access aria-describedby');
    assert.equal(elementsExist.statusRole, 'status', 'status role="status"');

    // 2. Open Settings modal and verify state loading against real server API
    await ev(`(async () => {
      const modals = await import('/static/modals.js');
      await modals.showSettingsModal();
    })()`);

    const initialModalState = await ev(`(() => {
      const tel = document.getElementById('settings-telemetry-enabled');
      const rem = document.getElementById('settings-remote-access-enabled');
      const status = document.getElementById('settings-remote-access-status');
      return {
        telemetryChecked: tel.checked,
        remoteChecked: rem.checked,
        statusText: status.textContent,
        statusState: status.dataset.state,
      };
    })()`);

    assert.equal(initialModalState.telemetryChecked, true, 'telemetry defaults to checked');
    // Remote access is opt-in: it defaults to off, so a loopback server is already running as saved.
    assert.equal(initialModalState.remoteChecked, false, 'remote access defaults to unchecked (explicit opt-in)');
    assert.doesNotMatch(initialModalState.statusText, /restart required/i, 'nothing to restart when saved and running are both local-only');

    // 3. Save telemetry disabled and remote access disabled
    await ev(`(async () => {
      document.getElementById('settings-telemetry-enabled').checked = false;
      document.getElementById('settings-remote-access-enabled').checked = false;
      const modals = await import('/static/modals.js');
      await modals.applySettings();
    })()`);

    // Verify status text updated after save
    const afterSaveState = await ev(`(() => {
      const status = document.getElementById('settings-remote-access-status');
      return {
        statusText: status.textContent,
        statusState: status.dataset.state,
      };
    })()`);

    assert.equal(afterSaveState.statusText, 'Saved and running: local-only.', 'status updates to saved and running local-only');
    assert.equal(afterSaveState.statusState, 'current');

    // 4. Verify backend persistence via direct fetch
    const backendPrivacy = await ev(`(async () => {
      const resp = await fetch('/api/system/privacy');
      return await resp.json();
    })()`);

    assert.equal(backendPrivacy.telemetry_enabled, false, 'backend telemetry_enabled saved as false');
    assert.equal(backendPrivacy.remote_access_enabled, false, 'backend remote_access_enabled saved as false');
    assert.equal(backendPrivacy.remote_access_restart_required, false, 'restart not required because both saved and running are local-only');

    // 5. Test failure feedback: simulated save error keeps modal open
    await ev(`(() => {
      document.getElementById('settings-modal').style.display = 'flex';
      const origFetch = window.fetch;
      window.fetch = async (url, opts = {}) => {
        if (String(url).endsWith('/api/settings') && opts.method === 'PUT') {
          return new Response('{"error":"simulated failure"}', { status: 500 });
        }
        return origFetch(url, opts);
      };
    })()`);

    await ev(`(async () => {
      const modals = await import('/static/modals.js');
      await modals.applySettings();
    })()`);

    const modalDisplayAfterError = await ev(`document.getElementById('settings-modal').style.display`);
    assert.equal(modalDisplayAfterError, 'flex', 'settings modal must remain open when save fails');

    console.log('PASS end-to-end privacy UI verification on live dashboard DOM');
  } finally {
    await client.close();
  }
})().catch(error => {
    console.error(error);
    process.exit(1);
});
