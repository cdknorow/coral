/* Moved out of tasks.js as it was (#3837): the task list and dialog format helpers; no behaviour change. */
import { formatMonthDayHourMinute, formatMonthDayTime } from './format.js';

export const _priorityOrder = { critical: 0, high: 1, medium: 2, low: 3 };
const _priorityRank = { critical: 1, high: 2, medium: 3, low: 4 };
// Priority as the words people choose it by (the create dialog and the CLI), not its sort rank.
export const _priorityLabel = { critical: 'Critical', high: 'High', medium: 'Medium', low: 'Low' };

export function _formatCost(usd, precise) {
    if (usd == null) return '$0.00';
    if (usd === 0) return '$0.00';
    if (precise) {
        // Detail view: up to 4 decimal places, trim trailing zeros (keep at least 2)
        const s = usd.toFixed(4);
        return '$' + s.replace(/0{1,2}$/, '');
    }
    // Badge: show 2 decimals, but use <$0.01 for sub-cent costs
    if (usd > 0 && usd < 0.01) return '<$0.01';
    return '$' + usd.toFixed(2);
}

export function _formatTokenCount(n) {
    if (n == null) return '0';
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
    return n.toLocaleString();
}

export function _formatTaskTime(ts) {
    if (!ts) return '';
    try {
        return formatMonthDayHourMinute(new Date(ts));
    } catch { return ''; }
}

export function _agentTaskStatus(t) {
    if (t.status) return t.status;
    return t.completed === 1 ? 'completed' : t.completed === 2 ? 'in_progress' : t.completed === 3 ? 'skipped' : 'pending';
}

export function formatTaskDate(isoStr) {
    try {
        return formatMonthDayTime(new Date(isoStr));
    } catch {
        return isoStr;
    }
}
