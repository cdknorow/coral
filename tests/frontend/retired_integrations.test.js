// Isolated browser contract for retired proxy, Connected Apps and webhook surfaces.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

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
    const checks = await ev(`(window.__coral.buildAllLazy(), // the Settings dialog is built on first open (#4500): built, so a retired setting's absence means something
      ({
      connectedMenu: !!document.getElementById('connected-apps-menu-btn'),
      connectedView: !!document.getElementById('connected-apps-view'),
      proxySetting: !!document.getElementById('settings-proxy-enabled'),
      codexProxySetting: !!document.getElementById('settings-proxy-enabled-codex'),
      modelSelector: !!document.getElementById('settings-default-model-claude'),
      analyticsView: !!document.getElementById('cost-dashboard-view'),
      webhookModal: !!document.getElementById('webhook-modal')
    }))`);
    assert.equal(checks.connectedMenu, false);
    assert.equal(checks.connectedView, false);
    assert.equal(checks.proxySetting, false);
    assert.equal(checks.codexProxySetting, false);
    assert.equal(checks.modelSelector, true);
    assert.equal(checks.analyticsView, true);
    assert.equal(checks.webhookModal, false, 'webhooks are retired (#3646)');

    const source = await (await fetch(`${BASE}/static/modals.js`)).text();
    const cost = await (await fetch(`${BASE}/static/cost_dashboard.js`)).text();
    const app = await (await fetch(`${BASE}/static/app.js`)).text();
    assert.equal((await fetch(`${BASE}/api/connected-apps`)).status, 404);
    assert.equal((await fetch(`${BASE}/api/proxy/health`)).status, 404);
    assert.equal(source.includes('/api/proxy/'), false);
    assert.equal(source.includes('proxy_enabled'), false);
    assert.equal(cost.includes('/api/proxy/'), false);
    // Webhooks are retired (#3646): no route, no script, no code in the modals or the app.
    assert.equal((await fetch(`${BASE}/api/webhooks`)).status, 404);
    assert.equal((await fetch(`${BASE}/static/webhooks.js`)).status, 404);
    for (const [name, text] of [['modals.js', source], ['app.js', app]]) {
      assert.equal(/webhook/i.test(text), false, `${name} has no webhook code`);
    }
    console.log('PASS retired proxy, Connected Apps and webhook UI surfaces absent; models and analytics preserved');
  } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
