// Real-browser check of the Cloud Hub page policy: Coral's own dashboard, served with the hub's exact
// Content-Security-Policy (script-src 'self', no inline script, no third-party host) and the cloud-mode boot
// flag, must load and run with ZERO policy violations, including after the Settings dialog is opened.
//
// The hub injects the boot flag into the page; here a CDP Fetch interception does the same to Coral's index.
// Keep CSP in sync with coral-hub internal/webui ContentSecurityPolicy().
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const HOST = new URL(BASE).host;
// The mutation policy: it blocks Coral's own scripts, so loading the page under it MUST be reported as violations (checked below on every run).
const BROKEN_CSP = "default-src 'none'; script-src 'none'";
const CSP = [
  "default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'",
  "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'",
].join('; ');

// Every load happens in its OWN fresh tab (never the shared first tab other suites leave behind, whose buffered log entries and late
// frames, e.g. the panel suite's deliberately blocked leak attempts, would otherwise be replayed into this suite's listeners).
async function load(policy, { strict }) {
  const port = Number(process.env.CDP_PORT || 9222);
  const tab = await CDP.New({ port, url: 'about:blank' });
  const client = await CDP({ port, target: tab });
  const { Page, Runtime, Log, Network, Fetch } = client;
  const violations = [], exceptions = [], blocked = [];
  const note = (src, text) => { if (/content security policy|refused to (load|execute|apply|connect|frame)|violates the following/i.test(text)) violations.push(`${src}: ${text.slice(0, 300)}`); };
  Log.entryAdded(({ entry }) => note('log:' + entry.source, entry.text || ''));
  Runtime.consoleAPICalled(({ type, args }) => { if (type === 'error' || type === 'warning') note('console', args.map(a => a.value || a.description || '').join(' ')); });
  Runtime.exceptionThrown(({ exceptionDetails }) => exceptions.push((exceptionDetails.exception && exceptionDetails.exception.description) || exceptionDetails.text));
  Network.loadingFailed(e => { if (e.blockedReason) blocked.push(`${e.blockedReason} ${e.type}`); });
  try {
    await Promise.all([Page.enable(), Runtime.enable(), Log.enable(), Network.enable()]);
    await Network.setCacheDisabled({ cacheDisabled: true });
    // Coral registers a service worker (/static/sw.js); an earlier suite in the same Chrome profile may have left it active, and it
    // would answer the document before the Fetch interception ever sees the request.
    await Network.setBypassServiceWorker({ bypass: true });
    await Page.navigate({ url: 'about:blank' });
    await Page.loadEventFired();
    await Fetch.enable({ patterns: [
      { urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' },
      { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }, // the hub's session endpoint; Coral has none
    ] });
    let served = 0;
    safePaused(client, async ev => {
      if (ev.request.url.endsWith('/auth/session')) {
        return Fetch.fulfillRequest({ requestId: ev.requestId, responseCode: 200,
          responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
          body: Buffer.from(JSON.stringify({ authenticated: true, email: 'tester@example.com', csrf: 'test-csrf' })).toString('base64') });
      }
      if (ev.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: ev.requestId });
      const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: ev.requestId });
      let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
      html = html.replace('"mode":"local"', '"mode":"cloud"');
      served++;
      const headers = (ev.responseHeaders || []).filter(h => !/^content-(security-policy|length)$/i.test(h.name))
        .concat([{ name: 'Content-Security-Policy', value: policy }]);
      await Fetch.fulfillRequest({ requestId: ev.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
    });
    await Page.navigate({ url: BASE + '/' });
    await Page.loadEventFired();
    await new Promise(r => setTimeout(r, 3000)); // sleep-ok: a negative: the page must NOT redirect to sign-in in the time a redirect would take (a wait on the first request returned before it could have)
    assert.equal(served, 1, 'the page was served through the policy interception');
    if (strict) {
      const where = await Runtime.evaluate({ expression: `location.pathname`, returnByValue: true });
      assert.equal(where.result.value, '/agents', 'stayed on the dashboard (no redirect to sign-in; "/" is the Agents page since #3532)');
      const mode = await Runtime.evaluate({ expression: `JSON.parse(document.getElementById('coral-boot').textContent).mode`, returnByValue: true });
      assert.equal(mode.result.value, 'cloud', 'cloud mode boot flag');
      const inline = await Runtime.evaluate({ expression: `document.querySelectorAll('script:not([src]):not([type="application/json"])').length`, returnByValue: true });
      assert.equal(inline.result.value, 0, 'no executable inline <script> in the page');
      // Exercise a few screens that build markup with handlers and styles.
      for (const expr of [
        `import((window.__coralStatic||'/static/')+'modals.js').then(m => m.showSettingsModal && m.showSettingsModal())`,
        `import((window.__coralStatic||'/static/')+'utils.js').then(() => 1)`,
      ]) { await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true }); await new Promise(r => setTimeout(r, 800)); /* sleep-ok: each probe's CSP violation report has no completion event */ }
    }
    return { violations, exceptions, blocked };
  } finally { await client.close(); await CDP.Close({ port, id: tab.id }).catch(() => {}); }
}

(async () => {
  // 1. The hub's policy: zero violations.
  const { violations, exceptions, blocked } = await load(CSP, { strict: true });
  assert.deepEqual(violations, [], 'CSP violations:\n' + violations.join('\n'));
  assert.deepEqual(blocked.filter(b => /csp/i.test(b)), [], 'resources blocked by CSP');
  const bad = exceptions.filter(e => /escapeAttr|is not defined|csp/i.test(e));
  assert.deepEqual(bad, [], 'page exceptions:\n' + bad.join('\n'));
  // 2. MUTATION CHECK, every run: a policy that blocks Coral's own scripts must be reported, so the filter above cannot be hiding a real
  //    violation (it counts everything its own tab reports; there is no allow-list).
  const mutant = await load(BROKEN_CSP, { strict: false });
  assert.ok(mutant.violations.length > 0 || mutant.blocked.some(b => /csp/i.test(b)), 'the suite did NOT notice a policy that blocks the page\'s own scripts');
  console.log(`PASS cloud-mode page under the hub CSP: no violations, no inline script (${exceptions.length} unrelated exceptions ignored); mutant policy caught (${mutant.violations.length} violations, ${mutant.blocked.length} blocked)`);
})().catch(e => { console.error(e); process.exit(1); });
