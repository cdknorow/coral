/* What a stop says about its worktree, on screen (#6818, UI Accessibility's #6806 finding 3; #6943). A removed worktree is a short toast. One that was kept, or could not be removed, has a folder the person may
 * need to open: it goes to the bar of worktree_dock.js, on every layout (a floating toast of this length covered the top bar and Settings' Save and Cancel at 200% text on a desktop, and the phone's Chat/Terminal
 * toggle): one line, "Worktree kept", with Copy path, Details and a x, which stays until dismissed and is announced once. The path is text only (textContent), never markup. */
import { showToast } from './utils.js';
import { worktreeAnswerWords } from './worktree_words.js';
import { showWorktreeDock } from './worktree_dock.js';
import { importOnce } from './import_once.js';

/** Say the kill answer's `worktree` on screen. `subject` is 'team' or 'agent' (the words differ). Returns the toast of a removal, or null when there is nothing to say or it went to the bar. */
export function showWorktreeNotice(w, subject = 'team') {
    const words = worktreeAnswerWords(w, subject);
    if (!words) return null;
    // Settings' Storage and cleanup may be open while this stop finishes: its list read the worktree as in use before the stop ended, so read it again now (#7055)
    if (w.state === 'kept' && document.getElementById('wt-list')) importOnce('./worktrees_panel.js').then(m => m.loadWorktrees({ keepSaid: true })).catch(() => {});
    if (w.path && (w.state === 'kept' || w.state === 'failed') && showWorktreeDock({ text: words.text, path: w.path, error: words.error })) return null;
    return showToast(words.text, words.error);
}
