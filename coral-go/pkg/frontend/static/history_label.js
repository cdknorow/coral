/** The title of a chat in the list and in its header (#6200): the title its notes or summary give it, else the start of its first reply with any path cut to its last folder, else a plain "Chat": never the session's id. */
export function historyLabel(s) {
    const title = String((s && s.summary_title) || '').trim();
    if (title) return title;
    const first = String((s && s.summary) || '').replace(/`/g, '').replace(/\s+/g, ' ').trim()
        .replace(/(?:\/[\w.@~+-]+){2,}\/?/g, m => m.replace(/\/+$/, '').split('/').pop()) // a path is its last folder
        .replace(/\b[A-Za-z]:\\(?:[^\\\s]+\\)*([^\\\s]+)/g, '$1');
    return first || 'Chat';
}
