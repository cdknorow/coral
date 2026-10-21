const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
// Team names unique to this run: a server reused across runs (a --stress shard) keeps each team's saved working
// mode and presets, so fixed names would start from the last run's state instead of a new team's (#3118).
const RUN = Date.now().toString(36);
const TEAM_A = 'team-alpha-' + RUN, TEAM_B = 'team-beta-' + RUN;

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime } = client;
  const ev = async expression => {
    const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
    return r.result.value;
  };

  // Condition waits (#3118): the dialog's form is up; the status line has its next message (cleared before
  // each action, so an earlier 'saved' can't satisfy it); the dialog is gone.
  const { waitFor } = require('./helpers/wait').bind(ev);
const { removeBoards } = require('./helpers/board_cleanup');
  // load() sets data-initial-state once the team's mode, presets and settings are in the form.
  const formOpen = () => waitFor(`document.querySelector('.working-mode-form')?.dataset.initialState !== undefined`, 'the working-mode form to load');
  const statusSet = () => waitFor(`(t => t && !/saving|resetting|loading/i.test(t))(document.querySelector('.working-mode-status')?.textContent.trim())`, 'the status message');
  const dialogGone = () => waitFor(`!document.getElementById('team-working-mode-dialog')`, 'the dialog to close');
  try {
    await Page.enable();
    await Page.navigate({ url: BASE });
    await Page.loadEventFired();

    console.log('Testing team workflow presets, overrides, and prompt inspection end to end...');

    // Load team settings dialog module
    await ev(`import((window.__coralStatic||'/static/')+'team_working_mode.js').then(m => { window.showTeamWorkingMode = m.showTeamWorkingMode; })`);

    // 1. Initial load for team-alpha
    await ev(`window.showTeamWorkingMode('${TEAM_A}')`);
    await formOpen();

    // Verify initial working mode is none
    const initialMode = await ev(`document.querySelector('.working-mode-form').elements.mode.value`);
    assert.equal(initialMode, 'none', 'Initial mode should be none');

    // Verify initial preset options include none, shared_checkout, worktrees
    const initialPresets = await ev(`Array.from(document.querySelector('.working-mode-form').elements.preset.options).map(o => o.value)`);
    assert.deepEqual(initialPresets, ['none', 'shared_checkout', 'worktrees']);

    const settingsTabs = await ev(`Array.from(document.querySelectorAll('[data-settings-tab]')).map(tab => ({label: tab.textContent.trim(), selected: tab.getAttribute('aria-selected')}))`);
    assert.deepEqual(settingsTabs, [
      { label: 'Workflow', selected: 'true' },
      { label: 'Prompt inspection', selected: 'false' },
      { label: 'Health check', selected: 'false' }, // #4230: the real settings API returns a health object
      { label: 'Messaging', selected: 'false' }, // #5516: shown whether or not the allowlist is in force
    ], 'Team settings should expose clear section tabs');
    await ev(`document.querySelector('[data-settings-tab="prompts"]').click()`);
    assert.equal(await ev(`document.querySelector('[data-settings-panel="workflow"]').hidden`), true, 'Workflow panel should hide while inspecting prompts');
    assert.equal(await ev(`document.querySelector('[data-settings-panel="prompts"]').hidden`), false, 'Prompt panel should be visible when selected');
    const scrolledLayout = await ev(`(() => { const d=document.querySelector('#team-working-mode-dialog'), f=document.querySelector('.working-mode-form'); f.scrollTop=f.scrollHeight; return {headerTop:d.querySelector('header').getBoundingClientRect().top, dialogTop:d.getBoundingClientRect().top, saveVisible:d.querySelector('.team-settings-actions').getBoundingClientRect().bottom <= d.getBoundingClientRect().bottom, details:[...d.querySelectorAll('.prompt-inspection-fragment')].every(detail => !detail.open)}; })()`);
    assert.ok(scrolledLayout.headerTop >= scrolledLayout.dialogTop, 'Dialog header should remain reachable at the bottom of long prompt content');
    assert.equal(scrolledLayout.saveVisible, true, 'Shared Save action should remain reachable from every tab');
    assert.equal(scrolledLayout.details, true, 'Long prompt fragments should be collapsed by default');
    await ev(`document.querySelector('[data-settings-tab="workflow"]').click()`);

    // Verify prompt inspection cards
    const inspectionText = await ev(`document.querySelector('.prompt-inspection-content').textContent`);
    assert.ok(inspectionText.includes('Orchestrator'), 'Prompt inspection includes Orchestrator');
    assert.ok(inspectionText.includes('Agent / worker'), 'Prompt inspection includes Agent / worker');
    assert.ok(inspectionText.includes('Task defaults'), 'Prompt inspection includes Task defaults');
    assert.ok(inspectionText.includes('Scope: Global role configuration; read-only here'), 'Prompt inspection includes scope');
    assert.ok(inspectionText.includes('Effective composed task preview (on first claim)'), 'Prompt inspection includes composed preview');

    // Check effective system prompt contains team-alpha
    const effectiveSystem = await ev(`document.querySelectorAll('.prompt-inspection-card')[0].innerText`);
    assert.ok(effectiveSystem.includes(TEAM_A), 'Effective system prompt should include project name');

    console.log('✓ Initial team settings and prompt inspection verified');

    // 2. Create custom preset
    await ev(`document.querySelector('.preset-new').click()`);
    const customPresetId = await ev(`document.querySelector('.working-mode-form').elements.preset.value`);
    assert.ok(customPresetId.startsWith('custom-'), 'New preset has custom- prefix');

    await ev(`(() => {
      const form = document.querySelector('.working-mode-form');
      form.elements.preset_name.value = 'Sprint Workflow';
      form.elements.preset_instructions.value = 'All PRs must include unit tests and pass lint.';
      document.querySelector('.working-mode-status').textContent = ''; document.querySelector('.preset-save').click();
    })()`);
    await statusSet();

    const saveStatus = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(saveStatus.includes('Workflow preset saved'), `Status should indicate saved: ${saveStatus}`);

    // Verify mode options now include the custom preset
    const modeOptionsAfterCreate = await ev(`Array.from(document.querySelector('.working-mode-form').elements.mode.options).map(o => o.value)`);
    assert.ok(modeOptionsAfterCreate.includes(customPresetId), 'Mode options should include the newly created custom preset');

    console.log('✓ Custom preset creation verified');

    // 3. Edit custom preset and re-save (must use PUT and succeed, not 409 conflict)
    await ev(`(() => {
      const form = document.querySelector('.working-mode-form');
      form.elements.preset_instructions.value = 'All PRs must include unit tests, pass lint, and have approval.';
      document.querySelector('.working-mode-status').textContent = ''; document.querySelector('.preset-save').click();
    })()`);
    await statusSet();

    const editStatus = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(editStatus.includes('Workflow preset saved'), `Re-save should succeed without 409 conflict: ${editStatus}`);

    console.log('✓ Custom preset re-saving (PUT) verified');

    // 4. Select custom preset as active team working mode and save team settings
    await ev(`(() => {
      const form = document.querySelector('.working-mode-form');
      form.elements.mode.value = '${customPresetId}';
      form.elements.dependency_guidance.checked = true;
      form.elements.custom_instructions.value = 'Follow ${TEAM_A} conventions.';
      document.querySelector('.working-mode-status').textContent = ''; form.querySelector('button[type=submit]').click();
    })()`);
    await statusSet();

    const teamSaveStatus = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(teamSaveStatus.includes('Saved. Working mode applies on first claim'), `Team settings saved: ${teamSaveStatus}`);

    console.log('✓ Team working mode selection with custom preset saved');

    // 5. Close dialog and reopen: verify persistence across reopen
    await ev(`document.querySelector('#team-working-mode-dialog header .modal-close-btn').click()`);
    await dialogGone();
    assert.equal(await ev(`document.getElementById('team-working-mode-dialog')`), null, 'Dialog should be closed');

    await ev(`window.showTeamWorkingMode('${TEAM_A}')`);
    await formOpen();

    const reopenedMode = await ev(`document.querySelector('.working-mode-form').elements.mode.value`);
    assert.equal(reopenedMode, customPresetId, 'Reopened mode should match saved custom preset');

    const reopenedCustomText = await ev(`(() => {
      const form = document.querySelector('.working-mode-form');
      form.elements.preset.value = '${customPresetId}';
      form.elements.preset.onchange();
      return form.elements.preset_instructions.value;
    })()`);
    assert.equal(reopenedCustomText, 'All PRs must include unit tests, pass lint, and have approval.');

    console.log('✓ Persistence across dialog close and reopen verified');

    // 6. Board-local built-in overrides and reset
    // Select shared_checkout
    await ev(`(() => {
      const form = document.querySelector('.working-mode-form');
      form.elements.preset.value = 'shared_checkout';
      form.elements.preset.onchange();
    })()`);

    const isResetHiddenInitially = await ev(`document.querySelector('.preset-reset').hidden`);
    assert.equal(isResetHiddenInitially, true, 'Reset button should be hidden for un-overridden built-in');

    // Override with empty instructions
    await ev(`(() => {
      const form = document.querySelector('.working-mode-form');
      form.elements.preset_instructions.value = '';
      document.querySelector('.working-mode-status').textContent = ''; document.querySelector('.preset-save').click();
    })()`);
    await statusSet();

    const emptyOverrideStatus = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(emptyOverrideStatus.includes('Workflow preset saved'), `Empty override should be accepted: ${emptyOverrideStatus}`);

    // Verify empty instructions are preserved (not falling back to default text)
    const currentInstructions = await ev(`document.querySelector('.working-mode-form').elements.preset_instructions.value`);
    assert.equal(currentInstructions, '', 'Empty built-in override must display empty text, not default text');

    const isResetVisibleNow = await ev(`document.querySelector('.preset-reset').hidden`);
    assert.equal(isResetVisibleNow, false, 'Reset button should be visible after override');

    // Reset the built-in preset
    await ev(`document.querySelector('.working-mode-status').textContent = ''; document.querySelector('.preset-reset').click()`);
    await statusSet();

    const resetStatus = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(resetStatus.includes('Built-in preset reset'), `Status should indicate reset: ${resetStatus}`);

    const restoredInstructions = await ev(`document.querySelector('.working-mode-form').elements.preset_instructions.value`);
    assert.ok(restoredInstructions.includes('In the shared checkout'), `Default instructions restored: ${restoredInstructions}`);

    const isResetHiddenAfterReset = await ev(`document.querySelector('.preset-reset').hidden`);
    assert.equal(isResetHiddenAfterReset, true, 'Reset button should be hidden after reset');

    console.log('✓ Built-in empty override, display, and reset verified');

    // 7. Validation and edit preservation on failure
    await ev(`document.querySelector('.preset-new').click()`);
    await ev(`(() => {
      const form = document.querySelector('.working-mode-form');
      form.elements.preset_name.value = '';
      form.elements.preset_instructions.value = 'Valuable text that must not be cleared.';
      document.querySelector('.working-mode-status').textContent = ''; document.querySelector('.preset-save').click();
    })()`);

    const valError = await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.ok(valError.includes('Preset name is required'), `Validation error shown: ${valError}`);

    const preservedText = await ev(`document.querySelector('.working-mode-form').elements.preset_instructions.value`);
    assert.equal(preservedText, 'Valuable text that must not be cleared.', 'Typed instructions must be preserved on validation error');

    console.log('✓ Validation and edit preservation verified');

    // Close dialog
    await ev(`document.querySelector('#team-working-mode-dialog header .modal-close-btn').click()`);
    await dialogGone();

    // 8. Team isolation: team-beta should NOT have team-alpha's custom preset
    await ev(`window.showTeamWorkingMode('${TEAM_B}')`);
    await formOpen();

    const betaPresets = await ev(`Array.from(document.querySelector('.working-mode-form').elements.preset.options).map(o => o.value)`);
    assert.ok(!betaPresets.includes(customPresetId), 'team-beta should not have team-alpha custom preset');

    const betaMode = await ev(`document.querySelector('.working-mode-form').elements.mode.value`);
    assert.equal(betaMode, 'none', 'team-beta mode should be isolated');

    await ev(`document.querySelector('#team-working-mode-dialog header .modal-close-btn').click()`);
    await dialogGone();

    console.log('✓ Team isolation verified');

    // 9. First-claim task snapshot preservation
    // Task claimed under team-alpha's custom mode captures the workflow instructions
    const claimResult = await ev(`(async () => {
      const post = async (path, body) => {
        const r = await fetch('/api/board/${TEAM_A}' + path, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body)
        });
        if (!r.ok) throw new Error(await r.text());
        return r.json();
      };
      const task = await post('/tasks', { title: 'First claim snapshot task', created_by: 'Operator' });
      const claimed = await post('/tasks/claim', { subscriber_id: 'worker-1', task_id: task.id });
      return { taskId: task.id, claimedInstructions: claimed.workflow.instructions };
    })()`);

    assert.ok(claimResult.claimedInstructions.includes('All PRs must include unit tests, pass lint, and have approval.'),
      'Claimed task captured custom preset instructions');
    assert.ok(claimResult.claimedInstructions.includes(`Follow ${TEAM_A} conventions.`),
      'Claimed task captured custom instructions');

    // Now change team-alpha's working mode via API to worktrees
    await ev(`(async () => {
      const r = await fetch('/api/board/${TEAM_A}/working-mode', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'worktrees', custom_instructions: 'Different mode entirely' })
      });
      if (!r.ok) throw new Error(await r.text());
    })()`);

    // Fetch the previously claimed task: instructions must NOT have changed
    const fetchedTask = await ev(`fetch('/api/board/${TEAM_A}/tasks/' + ${claimResult.taskId}).then(r => r.json())`);
    assert.equal(fetchedTask.workflow.instructions, claimResult.claimedInstructions,
      'Claimed task instructions must remain immutable after working mode change');

    console.log('✓ First-claim task snapshot immutability verified');

    // 10. Operator / Orchestrator only permissions for task creation and reassignment
    const nonOrchestratorCreate = await ev(`(async () => {
      const r = await fetch('/api/board/${TEAM_A}/tasks', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: 'Unauthorized task', created_by: 'worker-1' })
      });
      return { status: r.status, text: await r.text() };
    })()`);
    assert.equal(nonOrchestratorCreate.status, 403, 'Non-orchestrator agent cannot create tasks');

    const nonOrchestratorReassign = await ev(`(async () => {
      const r = await fetch('/api/board/${TEAM_A}/tasks/${claimResult.taskId}/reassign', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ subscriber_id: 'worker-1', assignee: 'worker-2' })
      });
      return { status: r.status, text: await r.text() };
    })()`);
    assert.equal(nonOrchestratorReassign.status, 403, 'Non-orchestrator agent cannot reassign tasks');

    console.log('✓ Operator/Orchestrator-only task creation/reassignment permissions verified');

    console.log('\nPASS: All editable team workflows and prompt inspection end-to-end checks passed!');
  } finally {
    await removeBoards(ev, [TEAM_A, TEAM_B]);
    await client.close();
  }
})().catch(e => {
  console.error(e);
  process.exit(1);
});
