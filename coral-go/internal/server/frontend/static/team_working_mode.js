export async function showTeamWorkingMode(team) {
    document.getElementById('team-working-mode-dialog')?.remove();
    const dialog = document.createElement('dialog');
    dialog.id = 'team-working-mode-dialog';
    dialog.className = 'team-availability-dialog';
    dialog.setAttribute('aria-labelledby', 'working-mode-title');
    dialog.innerHTML = `<header><h2 id="working-mode-title">Team working mode</h2><button type="button" aria-label="Close">×</button></header>
      <p class="working-mode-team"></p>
      <form class="working-mode-form">
      <label>Working mode<select name="mode"><option value="none">None</option><option value="shared_checkout">Shared checkout</option><option value="worktrees">Worktrees</option></select></label>
      <label><input type="checkbox" name="dependency_guidance"> Include dependent-queue guidance</label>
      <label>Additional instructions<textarea name="custom_instructions" rows="5" placeholder="Team-specific conventions"></textarea></label>
      <p>Instructions are saved with each task on its first claim. Existing claimed tasks keep their instructions. This setting provides guidance; it does not create worktrees automatically.</p>
      <p class="working-mode-status" role="status"></p>
      <button type="submit" disabled>Save mode</button>
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
        status.textContent = '';
        save.disabled = false;
    } catch (error) { status.textContent = error.message; }
    form.onsubmit = async e => {
        e.preventDefault(); save.disabled = true;
        status.textContent = 'Saving…';
        try {
            const response = await fetch(url, {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify({mode:form.elements.mode.value, dependency_guidance:form.elements.dependency_guidance.checked, custom_instructions:form.elements.custom_instructions.value})});
            const data = await response.json();
            if (!response.ok) throw new Error(data.error || 'Could not save working mode.');
            status.textContent = 'Saved. Applies when a task is first claimed.';
        } catch (error) { status.textContent = error.message; }
        finally { save.disabled = false; }
    };
}
