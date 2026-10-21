/* Board chat: the task queue's own lines (#3343).
   Every task posts "New Task", "claimed", "completed"… lines as "Coral Task Queue", so on a
   busy board they bury what people and agents wrote. In the chat, each run of consecutive
   task-queue lines is one muted row ("3 task updates · #3343 claimed by Backend Dev · …")
   that opens to show them. Only the display changes: the messages, and the @mention
   notifications they carry, are exactly as before. In select mode nothing is collapsed, so
   each line can still be selected. */

import { escapeHtml, escapeAttr } from './utils.js';

 const TASK_QUEUE_SENDER = 'Coral Task Queue';

 function isTaskQueueMessage(m) {
    if (!m) return false;
    // The server sets is_system for its own posts (#3387); a name alone is never trusted.
    return m.is_system === true && m.subscriber_id === TASK_QUEUE_SENDER;
}

/** Splits messages into items: {kind:'msg', m} or {kind:'queue', key, msgs} for each run
 *  of consecutive task-queue lines. collapse=false keeps every message as its own item. */
export function groupTaskQueueRuns(messages, collapse = true) {
    const items = [];
    for (const m of messages) {
        if (collapse && isTaskQueueMessage(m)) {
            const last = items[items.length - 1];
            if (last && last.kind === 'queue') { last.msgs.push(m); continue; }
            items.push({ kind: 'queue', key: String(m.id), msgs: [m] });
            continue;
        }
        items.push({ kind: 'msg', m });
    }
    return items;
}

// "[Task #3343 claimed by Backend Dev] Board chat: …" -> "Task #3343 claimed by Backend Dev"
function summaryOf(content) {
    const text = String(content || '').replace(/^(@\S+\s+)+/, '');
    const bracket = text.match(/\[([^\]]{1,120})\]/);
    return (bracket ? bracket[1] : text.split('\n')[0]).trim().slice(0, 120);
}

// Rows the user opened stay open across re-renders (the chat re-renders on every poll).
// Remembered by their messages' ids, so a row that grows (a new line from a poll, or
// older ones from "Load earlier") stays open.
const openIds = new Set();

// One capture listener, not an inline ontoggle: the Coral Hub's CSP strips inline
// handlers, and 'toggle' doesn't bubble.
document.addEventListener('toggle', (e) => {
    const d = e.target;
    if (!d || !d.matches || !d.matches('details.mb-queue-run')) return;
    for (const line of d.querySelectorAll('.mb-queue-line')) {
        if (d.open) openIds.add(line.dataset.messageId); else openIds.delete(line.dataset.messageId);
    }
}, true);

/** One collapsed row for a run of task-queue lines. formatTime formats created_at. */
export function renderTaskQueueRun(item, formatTime) {
    const n = item.msgs.length;
    const summaries = item.msgs.slice(-3).reverse().map(m => summaryOf(m.content));
    const more = n > 3 ? ` · +${n - 3} more` : '';
    const label = n === 1 ? summaries[0] : `${n} task updates · ${summaries.join(' · ')}${more}`;
    const lines = item.msgs.map(m => `
            <div class="mb-queue-line" data-message-id="${escapeAttr(String(m.id))}">
                <span class="mb-queue-time">${escapeHtml(formatTime(m.created_at))}</span>
                <span class="mb-queue-text">${escapeHtml(String(m.content || ''))}</span>
            </div>`).join('');
    const open = item.msgs.some(m => openIds.has(String(m.id)));
    return `<details class="mb-queue-run" role="listitem" data-queue-run="${escapeAttr(item.key)}"${open ? ' open' : ''}>
        <summary class="mb-queue-summary" title="Task queue updates">
            <span class="mb-queue-icon" aria-hidden="true">⚙</span>
            <span class="mb-queue-label">${escapeHtml(label)}</span>
        </summary>${lines}
    </details>`;
}
