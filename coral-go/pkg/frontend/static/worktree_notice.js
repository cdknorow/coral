/* What a stop says about its worktree, on screen (#6818, UI Accessibility's #6806 finding 3). A removed worktree is a short toast. One that was kept, or could not be removed, has a folder the person
 * may need to open: its words stay on screen until dismissed, with a Copy path button (the toast's own button, which leaves the notice open), instead of fading in a few seconds. The notice is
 * announced once, by showToast (a polite status; an error is an alert). On a phone's layout it is the bar of worktree_dock.js instead. The path is text only (textContent), never markup. */
import { showToast } from './utils.js';
import { copyWorktreePath, focusAfterOwnKill, giveFocusBack } from './worktree_notice_shared.js';
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
    const toast = showToast(words.text, words.error, Infinity, { action: { label: 'Copy path', keepOpen: true, onClick: button => copyWorktreePath(w.path, button) }, expanded: true });
    toast.classList.add('worktree-notice');
    focusAfterOwnKill(toast.querySelector('.toast-action')); // the Notifications column is last in Tab order (#6830)
    // dismissed (its x or Escape) while it holds the focus (#6837)
    const giveBack = () => { if (toast.contains(document.activeElement)) giveFocusBack(); };
    toast.addEventListener('click', e => { if (e.target.closest('.toast-close')) giveBack(); }, true);
    toast.addEventListener('keydown', e => { if (e.key === 'Escape') giveBack(); }, true);
    return toast;
}
