/* Rename a board (#3536): Team settings' "Rename board" changes the board's LABEL only. Its id,
   and so its URL, its agents, tasks and history, stay as they are. PATCH /api/board/<id> {label}
   is the Operator's; Coral answers 400 invalid_board_name for a label that breaks the name rule,
   and 403 when this dashboard may not rename it (through Coral Hub). Errors show inline, as text. */

import { boardFetch, boardLabel, splitKey, noteBoardLabel } from './server_base.js';
import { boardNameProblem } from './utils.js';

let _seq = 0;

/** The "Rename board" control for team (a board key). onRenamed(label) runs after a rename. */
export function boardRenameControl(team, { onRenamed } = {}) {
    const n = ++_seq;
    const wrap = document.createElement('div');
    wrap.className = 'board-rename';
    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'btn btn-sm board-rename-toggle';
    toggle.textContent = 'Rename board';
    toggle.setAttribute('aria-expanded', 'false');
    toggle.setAttribute('aria-controls', `board-rename-form-${n}`);

    // A group, not a <form>: Team settings' own form is found by querySelector('form') (its
    // unsaved-changes check), so this must not be one. Enter in the field renames.
    const form = document.createElement('div');
    form.className = 'board-rename-form';
    form.id = `board-rename-form-${n}`;
    form.setAttribute('role', 'group');
    form.setAttribute('aria-label', 'Rename board');
    form.hidden = true;
    const label = document.createElement('label');
    label.htmlFor = `board-rename-input-${n}`;
    label.textContent = 'Board name';
    const input = document.createElement('input');
    input.id = `board-rename-input-${n}`;
    input.name = 'board_label';
    input.maxLength = 64;
    input.autocomplete = 'off';
    input.spellcheck = false;
    input.setAttribute('aria-describedby', `board-rename-error-${n}`);
    const save = document.createElement('button');
    save.type = 'button';
    save.className = 'btn btn-sm btn-primary';
    save.textContent = 'Rename';
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'btn btn-sm';
    cancel.textContent = 'Cancel';
    const error = document.createElement('p');
    error.className = 'board-rename-error';
    error.id = `board-rename-error-${n}`;
    error.setAttribute('role', 'alert');
    const note = document.createElement('p');
    note.className = 'board-rename-note';
    note.textContent = 'Changes the name people see. Its agents, tasks, history and links stay the same.';
    form.append(label, input, save, cancel, error, note);
    wrap.append(toggle, form);

    const showError = msg => {
        error.textContent = msg || '';
        if (msg) input.setAttribute('aria-invalid', 'true');
        else input.removeAttribute('aria-invalid');
    };
    const open = isOpen => {
        form.hidden = !isOpen;
        toggle.setAttribute('aria-expanded', String(isOpen));
        showError('');
        if (isOpen) { input.value = boardLabel(team); input.focus(); input.select(); }
        else toggle.focus();
    };
    toggle.addEventListener('click', () => open(form.hidden));
    cancel.addEventListener('click', () => open(false));
    input.addEventListener('input', () => showError(''));
    form.addEventListener('keydown', e => {
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); open(false); }
        else if (e.key === 'Enter' && e.target === input) { e.preventDefault(); rename(); }
    });
    save.addEventListener('click', () => rename());
    const rename = async () => {
        if (save.disabled) return;
        const value = input.value.trim();
        const problem = boardNameProblem(value);
        if (problem) { showError(problem); input.focus(); return; }
        save.disabled = true;
        try {
            const resp = await boardFetch(team, '', {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ label: value }),
            });
            const data = await resp.json().catch(() => null);
            if (!resp.ok) {
                showError((data && (data.message || data.error)) || (resp.status === 403
                    ? "You can't rename this board from here."
                    : `Couldn't rename the board (HTTP ${resp.status}).`));
                input.focus();
                return;
            }
            const { server, name } = splitKey(team);
            const newLabel = (data && typeof data.board_label === 'string' && data.board_label) || value;
            noteBoardLabel(server, name, newLabel);
            open(false);
            if (onRenamed) onRenamed(newLabel);
        } catch {
            showError("Couldn't reach Coral to rename the board.");
        } finally {
            save.disabled = false;
        }
    };
    return wrap;
}
