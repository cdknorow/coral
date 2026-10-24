/* What the desktop's kept-worktree toast (worktree_notice.js) and the phone's bar (worktree_dock.js) share (#6893, a move out of both): copying the path with its feedback, taking the focus after the person's
 * own Kill, and giving it back when the notice is dismissed. One place, so the two cannot drift apart. */
import { copyText } from './utils.js';
import { announceNotice } from './notifications.js';

/** Copy a worktree's path, say so (a status; an alert when it failed) and show it on the button for two seconds. */
export async function copyWorktreePath(path, button) {
    const ok = await copyText(String(path));
    announceNotice(ok ? 'Path copied.' : 'Could not copy the path.', !ok);
    if (button) {
        const was = button.dataset.label || button.textContent; button.dataset.label = was;
        button.textContent = ok ? 'Copied' : 'Not copied';
        setTimeout(() => { if (button.isConnected) button.textContent = was; }, 2000);
    }
}

/** The answer to the person's own Kill: the notices' column is last in Tab order, so the focus goes to the notice's first control, unless the person has already gone on to something else (focus on the page
 *  itself, or still on the closed confirmation, is not "something else"). */
export function focusAfterOwnKill(control) {
    const a = document.activeElement;
    if (control && (!a || a === document.body || a.closest('#confirm-modal') || !a.isConnected)) control.focus();
}

/** A notice that held the focus was dismissed: the focus must not fall to the page, so it goes back to where the person works (#6837): the first agent in the Agents list (the stopped team's own row went with
 *  its stop), else the message box, else the list's search box (the sidebar's, or the phone's own list's). Nothing moves when the focus has already gone somewhere else. */
export function giveFocusBack() {
    setTimeout(() => {
        const a = document.activeElement;
        if (a && a !== document.body && a.isConnected) return; // the person is already somewhere
        const row = [...document.querySelectorAll('#live-sessions-list [tabindex="0"], #live-sessions-list button, #live-sessions-list a[href], #mobile-agent-list [tabindex="0"]')].find(e => e.getClientRects().length);
        const box = document.getElementById('command-input');
        const search = [...document.querySelectorAll('input.sidebar-search-input')].find(e => e.getClientRects().length);
        const target = row || (box && box.getClientRects().length ? box : null) || search;
        if (target) target.focus();
    }, 0);
}
