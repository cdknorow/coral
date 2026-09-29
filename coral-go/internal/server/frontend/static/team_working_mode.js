export async function showTeamWorkingMode(team) {
    document.getElementById('team-working-mode-dialog')?.remove();
    const dialog = document.createElement('dialog');
    dialog.id = 'team-working-mode-dialog';
    dialog.className = 'team-availability-dialog';
    dialog.setAttribute('aria-labelledby', 'working-mode-title');
    dialog.innerHTML = `<header><h2 id="working-mode-title">Team settings</h2><button type="button" aria-label="Close">×</button></header>
      <p class="working-mode-team"></p>
      <form class="working-mode-form">
      <fieldset class="team-settings-section">
        <legend>Working mode (this team)</legend>
        <label>Working mode<select name="mode"><option value="none">None</option><option value="shared_checkout">Shared checkout</option><option value="worktrees">Worktrees</option></select></label>
        <label><input type="checkbox" name="dependency_guidance"> Include dependent-queue guidance</label>
        <label>Additional instructions<textarea name="custom_instructions" rows="4" placeholder="Team-specific conventions"></textarea></label>
        <p class="working-mode-hint">Working mode instructions are saved with each task on its first claim. Existing claimed tasks keep their instructions. This setting provides guidance; it does not create worktrees automatically.</p>
      </fieldset>
      <fieldset class="team-settings-section global-settings-section">
        <legend>Global settings</legend>
        <label><input type="checkbox" name="board_health_monitor"> Run global board health monitor</label>
        <p class="working-mode-hint">Applies to all teams. Takes effect on next Coral server restart.</p>
      </fieldset>
      <p class="working-mode-status" role="status"></p>
      <button type="submit" disabled>Save settings</button>
      </form>`;
    dialog.querySelector('.working-mode-team').textContent = team;
    document.body.append(dialog);
    dialog.querySelector('header button').onclick = () => dialog.close();
    dialog.addEventListener('close', () => dialog.remove());
    dialog.addEventListener('click', e => { if (e.target === dialog) dialog.close(); });
    dialog.showModal();
    const form = dialog.querySelector('form');
    const status = dialog.querySelector('[role=status]');
    const save = form.querySelector('[type=submit]');
    const url = `/api/board/${encodeURIComponent(team)}/working-mode`;
    status.textContent = 'Loading…';
    try {
        const response = await fetch(url);
        if (!response.ok) throw new Error('Could not load working mode. Close and reopen to retry.');
        const data = await response.json();
        form.elements.mode.value = data.mode;
        form.elements.dependency_guidance.checked = data.dependency_guidance;
        form.elements.custom_instructions.value = data.custom_instructions;
        const settingsResponse = await fetch('/api/settings');
        if (settingsResponse.ok) {
            const settings = await settingsResponse.json();
            form.elements.board_health_monitor.checked = settings.settings?.board_health_monitor === 'true';
        }
        status.textContent = '';
        save.disabled = false;
    } catch (error) { status.textContent = error.message; }
    form.onsubmit = async e => {
        e.preventDefault(); save.disabled = true;
        status.textContent = 'Saving…';
        let modeErr = null;
        let healthErr = null;
        try {
            const response = await fetch(url, {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify({mode:form.elements.mode.value, dependency_guidance:form.elements.dependency_guidance.checked, custom_instructions:form.elements.custom_instructions.value})});
            if (!response.ok) {
                const data = await response.json().catch(() => ({}));
                modeErr = data.error || 'Could not save working mode.';
            }
        } catch (err) {
            modeErr = err.message;
        }
        try {
            const healthResponse = await fetch('/api/settings', {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify({board_health_monitor:form.elements.board_health_monitor.checked ? 'true' : 'false'})});
            if (!healthResponse.ok) {
                healthErr = 'Could not save board health setting.';
            }
        } catch (err) {
            healthErr = err.message;
        }

        if (!modeErr && !healthErr) {
            status.textContent = 'Saved. Working mode applies on first claim; global health monitor applies on server restart.';
        } else if (modeErr && healthErr) {
            status.textContent = `Save failed: ${modeErr}; ${healthErr}`;
        } else if (modeErr) {
            status.textContent = `Working mode save failed: ${modeErr}. Global health monitor setting was saved (applies on restart).`;
        } else {
            status.textContent = `Working mode saved (applies on first claim). Could not save global health monitor setting: ${healthErr}`;
        }
        save.disabled = false;
    };
}
