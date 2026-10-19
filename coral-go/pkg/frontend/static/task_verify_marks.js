/* The verification mark on a task row and the verification line in its dialog (moved out of tasks.js, #4996; no behaviour change). */
import { escapeAttr, escapeHtml } from './utils.js';
import { verificationOf } from './task_verification.js';
import { formatTaskDate } from './task_format.js';

// off by default, persisted
/** A finished task's verification (#4564), quietly: in the list a small muted icon after the title only for Evidence attached, its words only in the tooltip and the accessible name; a
 *  verified or an unverified task shows nothing. '' where there is nothing to say. */
export function _verifyMark(t) {
    const v = verificationOf(t, formatTaskDate);
    if (!v || !v.mark) return '';
    return `<span class="board-task-verify-mark ${v.state}" role="img" tabindex="0" title="${escapeAttr(v.detail)}" aria-label="${escapeAttr(v.detail)}"><span class="material-icons" aria-hidden="true">${v.icon}</span></span>`;
}

/** The task detail's one plain muted line: who verified it, or that evidence is attached; nothing for an unverified task. */
export function _verifyLine(t) {
    const v = verificationOf(t, formatTaskDate);
    return v && v.line ? `<div class="task-detail-verification">${escapeHtml(v.line)}</div>` : '';
}
