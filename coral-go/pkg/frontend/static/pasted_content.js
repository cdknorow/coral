// Claude Code wraps text pasted into a prompt in <pasted_content id="N"> ... </pasted_content id="N">.
// In a chat bubble the opening tag vanished (the sanitizer drops an unknown element) while the
// closing one, which is not valid HTML, showed as text (#4208). unwrapPastedContent shows the
// pasted text itself: it removes a well-formed pair, an opening tag and the next closing tag with
// the same id (or both with none), and leaves anything else, a lone tag included, as written.

const OPEN_RE = /<pasted_content(?:\s+id="([^"<>]*)")?\s*>/g;
const CLOSE_RE = /<\/pasted_content(?:\s+id="([^"<>]*)")?\s*>/g;

/** text with each matching pasted_content pair replaced by the pasted text, as its own paragraph. */
export function unwrapPastedContent(text) {
    const s = String(text ?? '');
    if (!s.includes('pasted_content')) return s;
    let out = '';
    let pos = 0;
    OPEN_RE.lastIndex = 0;
    let open;
    while ((open = OPEN_RE.exec(s))) {
        const id = open[1] ?? '';
        CLOSE_RE.lastIndex = OPEN_RE.lastIndex;
        let close;
        while ((close = CLOSE_RE.exec(s)) && (close[1] ?? '') !== id) { /* another paste's closer */ }
        if (!close) continue; // no matching closer: the opening tag stays as written
        const before = s.slice(pos, open.index);
        const inner = s.slice(OPEN_RE.lastIndex, close.index).replace(/^[ \t]*\r?\n/, '').replace(/\r?\n[ \t]*$/, '');
        out += before + (before.trim() ? '\n\n' : '') + inner;
        pos = CLOSE_RE.lastIndex;
        if (s.slice(pos).trim()) out += '\n\n';
        OPEN_RE.lastIndex = pos;
    }
    return out + s.slice(pos);
}
