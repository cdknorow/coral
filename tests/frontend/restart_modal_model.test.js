const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT) });
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

    await ev(`(async () => {
      const { state } = await import('/static/state.js');
      const controls = await import('/static/controls.js');
      await import('/static/modals.js');
      window.testState = state;
      window.testControls = controls;
      window.__restartRequests = [];

      window.fetch = async (url, opts = {}) => {
        const u = String(url);
        if (u.includes('/restart') && opts.method === 'POST') {
          const body = JSON.parse(opts.body);
          window.__restartRequests.push(body);
          return new Response(JSON.stringify({ ok: true, session_id: 'new-sid-999', session_name: 'codex-new' }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        if (u.includes('/api/settings')) {
          return new Response(JSON.stringify({ settings: { default_model_claude: 'claude-opus-5', default_model_codex: 'gpt-6-sol' } }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        if (u.includes('/api/agent-models')) {
          return new Response(JSON.stringify({ claude: ['claude-opus-5'], codex: ['gpt-6-sol', 'gpt-4o'], agy: [] }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        return new Response('{}', { status: 200 });
      };
    })()`);

    // S1: Stored model 'gpt-6-sol' -> User clears model -> confirmRestart sends model: ""
    await ev(`(async () => {
      testState.currentSession = {
        name: 'codex-6a5f',
        session_id: 'codex-6a5f-sid',
        agent_type: 'codex',
        model: 'gpt-6-sol',
        prompt: 'developer prompt',
      };
      // Render restart modal form
      window.renderAgentConfigForm('restart-acf', {
        showPreset: false,
        showName: false,
        value: {
          agentType: 'codex',
          model: 'gpt-6-sol',
          prompt: 'developer prompt',
        },
      });
      await new Promise(r => setTimeout(r, 150));
      // Clear model input
      const modelInput = document.querySelector('#restart-acf .acf-model');
      modelInput.value = '';
      modelInput.dispatchEvent(new Event('input', { bubbles: true }));
      // Confirm restart
      await testControls.confirmRestart();
    })()`);

    const requests = await ev(`window.__restartRequests`);
    assert.equal(requests.length, 1);
    const s1Req = requests[0];
    assert.equal(s1Req.session_id, 'codex-6a5f-sid');
    assert.equal(s1Req.agent_type, 'codex');
    assert.equal(s1Req.model, '', 'cleared model must be explicitly sent as empty string (""), not omitted');
    assert.ok('model' in s1Req, 'model key must be present in payload');
    assert.equal((await ev(`window.testState.currentSession.session_id`)), 'new-sid-999', 'restart must select the replacement Coral session');
    assert.equal((await ev(`window.testState.currentSession.name`)), 'codex-new', 'restart must select the replacement tmux session name');

    // S2: Stored model 'gpt-6-sol' -> User changes model to 'gpt-4o' -> confirmRestart sends model: "gpt-4o"
    await ev(`(async () => {
      window.renderAgentConfigForm('restart-acf', {
        showPreset: false,
        showName: false,
        value: {
          agentType: 'codex',
          model: 'gpt-6-sol',
          prompt: 'developer prompt',
        },
      });
      await new Promise(r => setTimeout(r, 150));
      const modelInput = document.querySelector('#restart-acf .acf-model');
      modelInput.value = 'gpt-4o';
      modelInput.dispatchEvent(new Event('input', { bubbles: true }));
      await testControls.confirmRestart();
    })()`);

    const s2Req = (await ev(`window.__restartRequests`))[1];
    assert.equal(s2Req.model, 'gpt-4o');

    // S3: Provider switch codex -> claude with cleared model -> confirmRestart sends agent_type: 'claude', model: ""
    await ev(`(async () => {
      window.renderAgentConfigForm('restart-acf', {
        showPreset: false,
        showName: false,
        value: {
          agentType: 'codex',
          model: 'gpt-6-sol',
          prompt: 'developer prompt',
        },
      });
      await new Promise(r => setTimeout(r, 150));
      const typeSelect = document.querySelector('#restart-acf .acf-agent-type');
      typeSelect.value = 'claude';
      typeSelect.dispatchEvent(new Event('change', { bubbles: true }));
      await new Promise(r => setTimeout(r, 150));
      const modelInput = document.querySelector('#restart-acf .acf-model');
      modelInput.value = '';
      modelInput.dispatchEvent(new Event('input', { bubbles: true }));
      await testControls.confirmRestart();
    })()`);

    const s3Req = (await ev(`window.__restartRequests`))[2];
    assert.equal(s3Req.agent_type, 'claude');
    assert.equal(s3Req.model, '', 'provider switch with cleared model must send empty string');

    // S4: Whitespace model input '   ' -> confirmRestart sends model: ""
    await ev(`(async () => {
      window.renderAgentConfigForm('restart-acf', {
        showPreset: false,
        showName: false,
        value: {
          agentType: 'codex',
          model: 'gpt-6-sol',
          prompt: 'developer prompt',
        },
      });
      await new Promise(r => setTimeout(r, 150));
      const modelInput = document.querySelector('#restart-acf .acf-model');
      modelInput.value = '   ';
      modelInput.dispatchEvent(new Event('input', { bubbles: true }));
      await testControls.confirmRestart();
    })()`);

    const s4Req = (await ev(`window.__restartRequests`))[3];
    assert.equal(s4Req.model, '', 'whitespace model input must trim to empty string');

    fs.writeFileSync('/tmp/coral-restart-modal-model.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
    console.log(JSON.stringify({ s1Req, s2Req, s3Req, s4Req }));
    console.log('All restart modal model frontend tests passed!');
  } finally {
    await client.close();
  }
})().catch(error => {
  console.error(error);
  process.exit(1);
});
