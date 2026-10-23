/* The keys a button may send to an agent's terminal (#6676): the names the server's key table already takes (ptymanager/named_keys.go), Ctrl plus a letter (C-a to C-z; Ctrl-C is C-c) and the few symbols a phone's keyboard
 * hides behind a second page, typed as themselves. A pure module (node-tested): the page's allowlist and the byte sequences for the WebSocket path, so a button cannot name a key the server would refuse. */

const NAMED = {
    Enter: '\r', Escape: '\x1b', Tab: '\t', BTab: '\x1b[Z', BSpace: '\x7f',
    Up: '\x1b[A', Down: '\x1b[B', Right: '\x1b[C', Left: '\x1b[D', Home: '\x1b[H', End: '\x1b[F',
    DC: '\x1b[3~', PageUp: '\x1b[5~', PageDown: '\x1b[6~',
};
const SYMBOLS = '|~/-';

/** The bytes the terminal gets for a key name, or null when it is not a key a button may send. */
export function rawKeySeq(k) {
    if (typeof k !== 'string') return null;
    if (Object.hasOwn(NAMED, k)) return NAMED[k];
    if (/^C-[a-z]$/.test(k)) return String.fromCharCode(k.charCodeAt(2) - 96);
    if (k.length === 1 && SYMBOLS.includes(k)) return k;
    return null;
}

/** Whether a button may send this key. */
export const rawKeyOk = k => rawKeySeq(k) !== null;
