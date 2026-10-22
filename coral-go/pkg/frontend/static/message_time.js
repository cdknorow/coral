// Sent time shared by chat and board; only the timestamp already on the message.
import { escapeAttr, escapeHtml } from './html_escape.js';

const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
const day = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });
const full = new Intl.DateTimeFormat(undefined, { dateStyle: 'full', timeStyle: 'long' });

export function messageTimeHtml(value, now = new Date()) {
    if ((typeof value !== 'string' && typeof value !== 'number') || value === '') return '';
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return '';
    const text = date.toDateString() === now.toDateString() ? clock.format(date) : `${day.format(date)}, ${clock.format(date)}`;
    return `<time class="message-time" datetime="${escapeAttr(date.toISOString())}" title="${escapeAttr(full.format(date))}">${escapeHtml(text)}</time>`;
}
