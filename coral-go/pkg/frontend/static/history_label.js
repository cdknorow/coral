/** The title of a chat in the list and in its header (#6200, #6251): the title its notes give it, else the first thing the person typed (first_user_message, "" for a chat indexed before it was kept), else the start of its first reply, either with any path cut to its last folder, else a plain "Chat": never the session's id. */
export function historyLabel(s) {
    const title = String((s && s.summary_title) || '').trim();
    if (title) return title;
    const first = String((s && (s.first_user_message || s.summary)) || '').replace(/`/g, '').replace(/\s+/g, ' ').trim()
        .replace(/(?:\/[\w.@~+-]+){2,}\/?/g, m => m.replace(/\/+$/, '').split('/').pop()) // a path is its last folder
        .replace(/\b[A-Za-z]:\\(?:[^\\\s]+\\)*([^\\\s]+)/g, '$1');
    return first || 'Chat';
}
