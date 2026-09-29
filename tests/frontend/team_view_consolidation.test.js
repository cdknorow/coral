const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime, Emulation } = client;
  const ev = async expression => {
    const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
    return r.result.value;
  };

  try {
    await Page.enable();
    await Page.navigate({ url: BASE });
    await Page.loadEventFired();

    console.log('Testing consolidated Team view and Team settings...');

    // 1. Verify kebab menu contains "Team view" and "Team settings", and NOT "Agent availability" or "Team details"
    const kebabHtml = await ev(`(() => {
      // Mock live sessions so renderLiveSessions can build kebab menus
      window.state = window.state || {};
      window.state.liveSessions = [
        {
          name: 'worker-1',
          board_project: 'test-team',
          agent_type: 'codex',
          working_directory: '/Users/test/workspace',
          branch: 'feature/consolidation',
          display_name: 'Worker 1',
          role: 'Developer'
        }
      ];
      // Import render module
      return import('/static/render.js').then(m => {
        const container = document.createElement('div');
        container.id = 'sessions-list';
        document.body.appendChild(container);
        m.renderLiveSessions(window.state.liveSessions);
        const menu = document.querySelector('.sidebar-kebab-menu');
        return menu ? menu.innerHTML : '';
      });
    })()`);

    assert.ok(kebabHtml.includes('Team view'), 'Kebab menu should contain "Team view"');
    assert.ok(kebabHtml.includes('Team settings'), 'Kebab menu should contain "Team settings"');
    assert.ok(!kebabHtml.includes('Agent availability'), 'Kebab menu should NOT contain "Agent availability"');
    assert.ok(!kebabHtml.includes('Team details'), 'Kebab menu should NOT contain "Team details"');
    console.log('✓ Kebab menu labels verified (Team view, Team settings)');

    await ev(`(async () => {
      const { state } = await import('/static/state.js');
      state.liveSessions = [
        {
          name: 'worker-1',
          board_project: 'test-team',
          agent_type: 'codex',
          working_directory: '/Users/test/workspace',
          branch: 'feature/consolidation',
          display_name: 'Worker 1',
          role: 'Developer'
        }
      ];
      const availModule = await import('/static/team_availability.js');
      window.originalFetch = window.fetch;
      window.fetch = async (url, opts) => {
        if (typeof url === 'string' && url.includes('/status')) {
          return {
            ok: true,
            json: async () => ({
              summary: { available: 1, busy: 1 },
              observed_at: new Date().toISOString(),
              agents: [
                {
                  name: 'Worker 1',
                  subscriber_id: 'Worker 1',
                  agent_type: 'codex',
                  role: 'Developer',
                  availability: 'available',
                  available: true,
                  reason: 'Idle and ready',
                  tasks: [],
                  reminder: true,
                  reminder_interval_seconds: 300
                },
                {
                  name: 'Worker 2',
                  subscriber_id: 'Worker 2',
                  agent_type: 'codex',
                  role: 'Tester',
                  availability: 'busy',
                  available: false,
                  reason: 'Testing changes',
                  tasks: [{ scope: 'board', id: 101, status: 'in_progress', title: 'Verify UI' }],
                  reminder: false
                }
              ],
              unassigned_tasks: [
                { scope: 'board', id: 102, status: 'pending', title: 'Doc update' }
              ],
              health_report: ['No issues detected.']
            })
          };
        }
        return window.originalFetch(url, opts);
      };
      availModule.showTeamAvailability('test-team');
    })()`);

    await ev(`new Promise(r => setTimeout(r, 100))`);

    const dialogText = await ev(`document.querySelector('#team-availability-dialog').textContent`);
    assert.ok(dialogText.includes('Team view'), 'Dialog title should be Team view');
    assert.ok(dialogText.includes('Directory'), 'Dialog should contain Directory metadata');
    assert.ok(dialogText.includes('Branch'), 'Dialog should contain Branch metadata');
    assert.ok(dialogText.includes('Agents'), 'Dialog should contain Agents count');
    assert.ok(dialogText.includes('/Users/test/workspace'), 'Dialog should display working directory');
    assert.ok(dialogText.includes('feature/consolidation'), 'Dialog should display branch');
    assert.ok(dialogText.includes('Worker 1'), 'Dialog should display agent Worker 1');
    assert.ok(dialogText.includes('Worker 2'), 'Dialog should display agent Worker 2');
    assert.ok(dialogText.includes('Verify UI'), 'Dialog should display assigned task');
    assert.ok(dialogText.includes('Unassigned work'), 'Dialog should display unassigned work section');
    console.log('✓ Team view content and metadata display verified');

    // 3. Verify compact agent row layout
    const agentGridStyle = await ev(`(() => {
      const agent = document.querySelector('.availability-agent');
      const style = window.getComputedStyle(agent);
      return { display: style.display, gridTemplateColumns: style.gridTemplateColumns };
    })()`);
    assert.equal(agentGridStyle.display, 'grid', 'Agent row should use grid layout');
    console.log('✓ Compact agent row grid layout verified');

    // 4. Verify mobile responsiveness at 390px
    await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    const mobileFits = await ev(`(() => {
      const d = document.querySelector('#team-availability-dialog');
      return d.scrollWidth <= d.clientWidth + 1 && d.getBoundingClientRect().width <= innerWidth;
    })()`);
    assert.ok(mobileFits, 'Dialog must fit mobile screen without overflowing');
    console.log('✓ Mobile responsive layout verified (390px)');
    await Emulation.clearDeviceMetricsOverride();

    // Close Team view
    await ev(`document.querySelector('#team-availability-dialog header button').click()`);
    await ev(`new Promise(r => setTimeout(r, 50))`);

    // 5. Open Team settings (showTeamWorkingMode) and verify settings + health monitor
    await ev(`(async () => {
      const modeModule = await import('/static/team_working_mode.js');
      modeModule.showTeamWorkingMode('test-team');
    })()`);
    await ev(`new Promise(r => setTimeout(r, 100))`);

    const settingsTitle = await ev(`document.querySelector('#team-working-mode-dialog header h2').textContent`);
    assert.equal(settingsTitle, 'Team settings', 'Settings dialog header must be "Team settings"');

    const hasHealthMonitorCheckbox = await ev(`Boolean(document.querySelector('.working-mode-form input[name="board_health_monitor"]'))`);
    assert.ok(hasHealthMonitorCheckbox, 'Team settings must include board_health_monitor checkbox');

    // Change settings and save
    await ev(`(async () => {
      const form = document.querySelector('.working-mode-form');
      form.elements.mode.value = 'shared_checkout';
      form.elements.dependency_guidance.checked = true;
      form.elements.board_health_monitor.checked = true;
      form.elements.custom_instructions.value = 'Custom instructions for test.';
      await form.onsubmit({ preventDefault() {} });
    })()`);

    const saveStatus = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(saveStatus.includes('Saved'), 'Settings status should indicate Saved');
    assert.ok(saveStatus.includes('server restart'), 'Status must clarify server restart for health monitor');
    console.log('✓ Team settings saved successfully with restart applicability notice');

    // Verify fieldset sectioning
    const legends = await ev(`Array.from(document.querySelectorAll('.team-settings-section legend')).map(l => l.textContent)`);
    assert.deepEqual(legends, ['Working mode (this team)', 'Global settings'], 'Form must clearly separate team and global settings');
    console.log('✓ Clear fieldset separation verified (team working mode vs global settings)');

    // Verify partial failure feedback when global settings PUT fails
    await ev(`(async () => {
      const origFetch = window.fetch;
      window.fetch = async (url, opts) => {
        if (typeof url === 'string' && url.includes('/api/settings') && opts && opts.method === 'PUT') {
          return { ok: false, status: 500, json: async () => ({ error: 'Simulated settings disk error' }) };
        }
        return origFetch(url, opts);
      };
      const form = document.querySelector('.working-mode-form');
      await form.onsubmit({ preventDefault() {} });
      window.fetch = origFetch;
    })()`);

    const partialStatus = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(partialStatus.includes('Working mode saved'), 'Should indicate working mode succeeded');
    assert.ok(partialStatus.includes('Could not save global health monitor setting'), 'Should indicate health setting failed');
    console.log('✓ Partial failure feedback verified');

    // Verify persisted settings via API
    const persistedWorkingMode = await ev(`fetch('/api/board/test-team/working-mode').then(r => r.json())`);
    assert.equal(persistedWorkingMode.mode, 'shared_checkout');
    assert.equal(persistedWorkingMode.dependency_guidance, true);
    assert.equal(persistedWorkingMode.custom_instructions, 'Custom instructions for test.');

    const persistedGlobalSettings = await ev(`fetch('/api/settings').then(r => r.json())`);
    assert.equal(persistedGlobalSettings.settings?.board_health_monitor, 'true');
    console.log('✓ Verified settings persistence (working-mode + board_health_monitor)');

    // Close settings dialog
    await ev(`document.querySelector('#team-working-mode-dialog header button').click()`);
    await ev(`new Promise(r => setTimeout(r, 50))`);

    console.log('ALL CONSOLIDATION CHECKS PASSED!');
  } finally {
    await client.close();
  }
})().catch(e => {
  console.error(e);
  process.exit(1);
});
