/* What a stop says about its worktree, on screen (#6818, UI Accessibility's #6806 finding 3). A removed worktree is a short toast. One that was kept, or could not be removed, has a folder the person
 * may need to open: its words stay on screen until dismissed, with a Copy path button (the toast's own button, which leaves the notice open), instead of fading in a few seconds. The notice is
 * announced once, by showToast (a polite status; an error is an alert). The path is text only (textContent), never markup. */
import { showToast, copyText } from './utils.js';
import { announceNotice } from './notifications.js';
import { worktreeAnswerWords } from './worktree_words.js';

/** Say the kill answer's `worktree` on screen. `subject` is 'team' or 'agent' (the words differ). Returns the toast, or null when there is nothing to say. */
export function showWorktreeNotice(w, subject = 'team') {
    const words = worktreeAnswerWords(w, subject);
    if (!words) return null;
    if (!(w.path && (w.state === 'kept' || w.state === 'failed'))) return showToast(words.text, words.error);
    return showToast(words.text, words.error, Infinity, { action: {
        label: 'Copy path', keepOpen: true,
        onClick: async button => {
            const ok = await copyText(String(w.path));
            announceNotice(ok ? 'Path copied.' : 'Could not copy the path.', !ok);
            if (button) { const was = button.textContent; button.textContent = ok ? 'Copied' : 'Not copied'; setTimeout(() => { if (button.isConnected) button.textContent = was; }, 2000); }
        },
    }, expanded: true });
}
