/* The stored file's name from a Content-Disposition header (moved out of artifact_preview.js, #7045): filename* (RFC 5987, UTF-8) first, then filename, quoted or bare.
 * The quoted value is read one character at a time, so a hostile header (an unterminated quote and a long run of backslashes) costs one pass: the regex that read it before
 * backtracked exponentially (CodeQL js/redos). An unterminated quote reads as a bare value, as it did. No imports. */
const ENCODED_RE = /(?:^|;)[ \t]*filename\*[ \t]*=[ \t]*UTF-8'[^']*'([^;\s]+)/i;
const PLAIN_RE = /(?:^|;)[ \t]*filename[ \t]*=[ \t]*/i;

export function responseFilename(disposition) {
    const header = String(disposition || '');
    const encoded = ENCODED_RE.exec(header);
    if (encoded) {
        try { return decodeURIComponent(encoded[1]); } catch { /* Use ordinary filename below. */ }
    }
    const start = PLAIN_RE.exec(header);
    if (!start) return '';
    const at = start.index + start[0].length;
    if (header[at] === '"') {
        let out = '';
        for (let i = at + 1; i < header.length; i++) {
            const c = header[i];
            if (c === '\\' && i + 1 < header.length) { out += header[++i]; continue; }
            if (c === '"') return out;
            out += c;
        }
    }
    const bare = header.slice(at).split(';')[0].trim(); // bare, or a quote that never closed
    return bare;
}
