/* What a stop says about its worktree, on screen (#6818, UI Accessibility's #6806 finding 3). A removed worktree is a short toast. One that was kept, or could not be removed, has a folder the person
 * may need to open: its words stay on screen until dismissed, with a Copy path button (the toast's own button, which leaves the notice open), instead of fading in a few seconds. The notice is
 * announced once, by showToast (a polite status; an error is an alert). On a phone's layout it is the bar of worktree_dock.js instead. The path is text only (textContent), never markup. */
import { showToast, copyText } from './utils.js';
import { announceNotice } from './notifications.js';
import { worktreeAnswerWords } from './worktree_words.js';
import { isPhoneLayout } from './phone_layout.js';
import { showWorktreeDock } from './worktree_dock.js';

/** Say the kill answer's `worktree` on screen. `subject` is 'team' or 'agent' (the words differ). Returns the toast, or null when there is nothing to say or it went to the phone's bar. */
export function showWorktreeNotice(w, subject = 'team') {
    const words = worktreeAnswerWords(w, subject);
    if (!words) return null;
    if (!(w.path && (w.state === 'kept' || w.state === 'failed'))) return showToast(words.text, words.error);
    // a phone's layout has no room over the page for a notice that stays: a bar in the page's flow instead (#6859); a desktop keeps the toast
    if (isPhoneLayout() && showWorktreeDock({ text: words.text, path: w.path, error: words.error })) return null;
    const toast = showToast(words.text, words.error, Infinity, { action: {
        label: 'Copy path', keepOpen: true,
        onClick: async button => {
            const ok = await copyText(String(w.path));
            announceNotice(ok ? 'Path copied.' : 'Could not copy the path.', !ok);
            if (button) { const was = button.textContent; button.textContent = ok ? 'Copied' : 'Not copied'; setTimeout(() => { if (button.isConnected) button.textContent = was; }, 2000); }
        },
    }, expanded: true });
    toast.classList.add('worktree-notice');
    // the answer to the person's own Kill: the Notifications column is last in Tab order, so focus goes to its Copy path button, unless the person has already gone on to something else
    // (focus on the page itself, or still on the closed confirmation, is not "something else")
    const a = document.activeElement;
    if (!a || a === document.body || a.closest('#confirm-modal') || !a.isConnected) { const b = toast.querySelector('.toast-action'); if (b) b.focus(); }
    // dismissed (its x or Escape) while it holds the focus: the focus must not fall to the page, so it goes back to where the person works (#6837): the first agent in the Agents list (the team's own row
    // went with its stop), else the message box, else the list's search box
    const giveBack = () => {
        if (!toast.contains(document.activeElement)) return;
        setTimeout(() => {
            const a = document.activeElement;
            if (a && a !== document.body && a.isConnected) return; // the person is already somewhere
            const row = [...document.querySelectorAll('#live-sessions-list [tabindex="0"], #live-sessions-list button, #live-sessions-list a[href]')].find(e => e.getClientRects().length);
            const box = document.getElementById('command-input');
            const search = [...document.querySelectorAll('input.sidebar-search-input')].find(e => e.getClientRects().length); // the sidebar's, or the phone's own list's
            const target = row || (box && box.getClientRects().length ? box : null) || search;
            if (target) target.focus();
        }, 0);
    };
    toast.addEventListener('click', e => { if (e.target.closest('.toast-close')) giveBack(); }, true);
    toast.addEventListener('keydown', e => { if (e.key === 'Escape') giveBack(); }, true);
    return toast;
}
