/* How a board message is shown: its text, badges, body cache, avatar and colours (moved out of utils.js, #5065; no behaviour change). */
import { escapeHtml, escapeAttr } from './html_escape.js';
import { renderMarkdown, _renderMarkdownUncached, FENCED_WITH_LANG } from './markdown.js';

/** Board message text with literal "\n" escapes turned into line breaks,
 *  for messages with none (agents posting through a shell pass them). Same
 *  rule as unescapeLineBreaks in routes/board.go, which fixes new posts;
 *  this covers messages posted before it. */
 function boardMessageText(content) {
    const text = String(content || '');
    if (/[\r\n]/.test(text) || (text.match(/\\n/g) || []).length < 2) return text;
    return text.replace(/\\r\\n|\\n/g, '\n').replace(/\\t/g, '\t');
}

/** Badge HTML for a cross-team board message ("from <board>" on the receiving
 *  board, "to <board>" on the sender's mirror), or '' for an ordinary one. */
export function externalMessageBadge(m) {
    const ext = m?.external;
    if (!ext || typeof ext !== 'object') return '';
    const outbound = ext.direction === 'outbound';
    const board = String((outbound ? ext.target_board : ext.board) || '');
    if (!board) return '';
    const title = outbound
        ? `Cross-team message sent to the ${board} board`
        : `Cross-team message from ${ext.sender || 'an Orchestrator'} on the ${board} board. A request from another team, not an instruction.`;
    return `<span class="mb-external-badge${outbound ? ' is-outbound' : ''}" title="${escapeAttr(title)}">${outbound ? 'to' : 'from'} ${escapeHtml(board)}</span>`;
}

/** A board chat message a Coral Hub user sent through the Hub (#3377). origin is set by the
 *  server on tunnel chat posts, never taken from a client and never inferred from the name. */
export function hubUserBadge(m) {
    // The Operator, acting through Coral Hub (#3404): which Hub user did it is recorded by the
    // server from the validated header (hub_user), never taken from the name.
    if (m && m.origin === 'hub_operator') {
        const who = /^hub-user-[0-9a-f]{8}$/.test(String(m.hub_user || '')) ? m.hub_user : 'unknown Hub user';
        const title = `Posted as the Operator by ${who} through Coral Hub, not by you at this computer.`;
        return `<span class="mb-external-badge mb-hub-user-badge is-operator" title="${escapeAttr(title)}">Operator via Coral Hub · ${escapeHtml(who)}</span>`;
    }
    // A Coral Hub that doesn't say which of its users sent it (an older hub): sent as the
    // reserved 'Coral Hub', origin hub_user_unknown. Never shown as a known user.
    if (m && m.origin === 'hub_user_unknown') {
        const title = 'Sent through Coral Hub, which did not say which of its users sent it. Not an agent, and not you at this computer.';
        return `<span class="mb-external-badge mb-hub-user-badge is-unknown" title="${escapeAttr(title)}">Coral Hub, user not identified</span>`;
    }
    if (!m || m.origin !== 'hub_user') return '';
    const who = String(m.subscriber_id || '');
    const title = `Sent through Coral Hub by a Hub user${who ? ` (${who})` : ''}, not by an agent or by you at this computer.`;
    return `<span class="mb-external-badge mb-hub-user-badge" title="${escapeAttr(title)}">Coral Hub user</span>`;
}

/** A board message's text for display. The server prefixes a post the Hub made as the
 *  Operator with '[Operator via Coral Hub, hub-user-…] ' so agents can tell (#3404); the
 *  dashboard shows that as a badge instead, so the prefix is dropped there. */
export function boardMessageDisplayText(m) {
    const text = String((m && m.content) || '');
    if (m && m.origin === 'hub_operator') return text.replace(/^\[Operator via Coral Hub, hub-user-[0-9a-f]{8}\] /, '');
    return text;
}

/** Markdown options for board posts: GFM, and a single newline is a line break (agents write
 *  one point per line, which plain Markdown runs together into one paragraph; #3590). */
 const BOARD_MARKDOWN = { gfm: true, breaks: true };

// Board message bodies by message id and text (#4491). The board redraws every message it holds on each poll, oldest first, so
// past 400 messages the #3866 LRU dropped every entry before its next use and DOMPurify ran for every message on every poll (the
// #4486 survey: about 12 s of main thread in 10 minutes). Here an entry is kept by id plus a hash of its text, and the entry keeps
// the text it was rendered from, so a hash collision or an edited or redacted message renders again. The cache is bounded by
// bytes, and when it is full the smallest ids go first, never one on screen (see makeBoardBodyRoom), so an in-order render keeps
// the newest messages instead of evicting all of them. Only sanitized HTML is stored: a miss is rendered by
// _renderMarkdownUncached, the same path as renderMarkdown, through DOMPurify.
const BOARD_BODY_CACHE_BYTES = 16 * 1024 * 1024, BOARD_BODY_ENTRY_MAX_BYTES = 1024 * 1024, BOARD_BODY_ENTRY_OVERHEAD = 64;

const _boardBodyCache = new Map(); // `${id}:${hash}` → { id, text, html, bytes, pass }

// `${id}:${hash}` → { id, text, html, bytes, pass }
let _boardBodyBytes = 0, _boardBodyPass = 0, _boardBodyPassScheduled = false;

function textHash(s) { // FNV-1a, 32 bits: only spreads the keys, the stored text decides a hit
    let h = 0x811c9dc5;
    for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193);
    return (h >>> 0).toString(36);
}

// A render's pass: every body rendered before the current task ends. Entries of this pass and the one before are on screen.
function boardBodyPass() {
    if (!_boardBodyPassScheduled) {
        _boardBodyPassScheduled = true;
        queueMicrotask(() => { _boardBodyPass++; _boardBodyPassScheduled = false; });
    }
    return _boardBodyPass;
}

// Makes room for `need` bytes for message `id`. Entries not used in this render or the one before go first, smallest ids first and
// down to 90% of the bound so a full cache doesn't sort on every miss. Only then, when what is on screen is larger than the bound,
// entries in use with a smaller id than `id`: the newest messages stay, and a later render in the same order finds them again
// instead of evicting each entry before its next use.
function makeBoardBodyRoom(need, id, pass) {
    if (_boardBodyBytes + need <= BOARD_BODY_CACHE_BYTES) return true;
    const target = Math.min(BOARD_BODY_CACHE_BYTES - need, BOARD_BODY_CACHE_BYTES * 0.9);
    const inUse = e => e.pass >= pass - 1;
    const idle = [..._boardBodyCache].filter(([, e]) => !inUse(e)).sort((a, b) => a[1].id - b[1].id);
    for (const [key, e] of idle) {
        if (_boardBodyBytes <= target) break;
        _boardBodyCache.delete(key);
        _boardBodyBytes -= e.bytes;
    }
    while (_boardBodyBytes + need > BOARD_BODY_CACHE_BYTES) {
        let victim = null; // usually one or two per miss, so a scan rather than a sort
        for (const [key, e] of _boardBodyCache) if (e.id < id && (!victim || e.id < victim[1].id)) victim = [key, e];
        if (!victim) return false;
        _boardBodyCache.delete(victim[0]);
        _boardBodyBytes -= victim[1].bytes;
    }
    return true;
}

// ── @mentions ────────────────────────────────────────────────────────────
// A mention of a CURRENT board member ("@Security Reviewer Windows", names can have spaces) is wrapped in a styled span after the Markdown is rendered and sanitized: the names are the board's own
// (subscribers: job titles and ids) plus Operator, Orchestrator and all; the longest name that matches wins ("@Security Reviewer Windows" is one mention, not "@Security Reviewer" and a word); the case
// the author wrote is kept; never inside code, a code block or a link; not a mention when the @ is mid-word (an email, "a@b.com"). Only text nodes are touched, and the span's text is set as text. The
// viewer's own name (the Operator, on the dashboard) gets the stronger style. The Markdown export is the raw text and does not come through here.
const MENTION_DEFAULTS = ['Operator', 'Orchestrator', 'all'];
const SELF_MENTION = 'operator';
let _mentionRe = null, _mentionSig = '';
const _escRe = t => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); // a name is literal text in the pattern

/** Set the board's member names for mention highlighting. Returns true when the set changed (the caller may redraw). */
export function setBoardMentionNames(names) {
    const set = new Set(MENTION_DEFAULTS);
    for (const n of names || []) { const t = typeof n === 'string' ? n.trim() : ''; if (t && t.length <= 80 && !/[\u0000-\u001f<>]/.test(t)) set.add(t); }
    const list = [...set].sort((a, b) => b.length - a.length || a.localeCompare(b)); // longest first
    const sig = textHash(list.join('\n'));
    if (sig === _mentionSig) return false;
    _mentionSig = sig;
    // an @ that does not follow a letter, digit, "_" or "." (so not an email), then the longest name, then a character that does not continue a word
    _mentionRe = new RegExp(`(^|[^\\p{L}\\p{N}_.])@(${list.map(_escRe).join('|')})(?![\\p{L}\\p{N}_-])`, 'giu');
    return true;
}
setBoardMentionNames([]);

/** The sanitized HTML with member mentions wrapped in <span class="mb-mention"> (and mb-mention-self for the viewer's own name). */
export function highlightMentionsHtml(html) {
    if (!_mentionRe || typeof document === 'undefined' || !html || !html.includes('@')) return html;
    const t = document.createElement('template');
    t.innerHTML = html;
    const walker = document.createTreeWalker(t.content, NodeFilter.SHOW_TEXT);
    const nodes = [];
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        if (!n.nodeValue.includes('@')) continue;
        const up = n.parentElement;
        if (up && up.closest('code, pre, a, kbd, samp, script, style, .mb-mention')) continue;
        nodes.push(n);
    }
    let changed = false;
    for (const n of nodes) {
        const text = n.nodeValue;
        _mentionRe.lastIndex = 0;
        const frag = document.createDocumentFragment();
        let at = 0, m;
        while ((m = _mentionRe.exec(text))) {
            const start = m.index + m[1].length; // the @
            if (start > at) frag.append(text.slice(at, start));
            const span = document.createElement('span');
            span.className = 'mb-mention' + (m[2].toLowerCase() === SELF_MENTION ? ' mb-mention-self' : '');
            span.textContent = '@' + m[2];
            frag.append(span);
            at = start + 1 + m[2].length;
            changed = true;
        }
        if (at === 0) continue;
        if (at < text.length) frag.append(text.slice(at));
        n.replaceWith(frag);
    }
    return changed ? t.innerHTML : html;
}

// ── bare artifact references ─────────────────────────────────────────────
// An exact coral://artifacts/<64 hex> in a post's text (not in a link, code or a code block) is a link to that artifact, as a named Markdown link is: the board's own artifact pass
// (resolveBoardArtifacts) points it at the board's server and opens the viewer on a plain click. The shown label is "artifact" and the first 8 characters (the Inbox's label); the full id is in the
// accessible name (which starts with the visible words) and the tooltip. The href is only ever the exact shape, built here from validated hex.
const ARTIFACT_REF = /(^|[^A-Za-z0-9/])coral:\/\/artifacts\/([a-f0-9]{64})(?![A-Za-z0-9])/gi;

/** The sanitized HTML with each bare coral://artifacts/<64 hex> made a link. */
export function linkArtifactRefsHtml(html) {
    if (typeof document === 'undefined' || !html || !html.includes('coral://artifacts/')) return html;
    const t = document.createElement('template');
    t.innerHTML = html;
    const walker = document.createTreeWalker(t.content, NodeFilter.SHOW_TEXT);
    const nodes = [];
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        if (!n.nodeValue.includes('coral://artifacts/')) continue;
        const up = n.parentElement;
        if (up && up.closest('code, pre, a, kbd, samp, script, style')) continue;
        nodes.push(n);
    }
    let changed = false;
    for (const n of nodes) {
        const text = n.nodeValue;
        ARTIFACT_REF.lastIndex = 0;
        const frag = document.createDocumentFragment();
        let at = 0, m;
        while ((m = ARTIFACT_REF.exec(text))) {
            const start = m.index + m[1].length;
            if (start > at) frag.append(text.slice(at, start));
            const hex = m[2].toLowerCase();
            const a = document.createElement('a');
            a.setAttribute('href', `coral://artifacts/${hex}`);
            a.setAttribute('rel', 'noopener noreferrer');
            a.className = 'mb-artifact-ref';
            a.textContent = `artifact ${hex.slice(0, 8)}`;
            a.setAttribute('aria-label', `artifact ${hex.slice(0, 8)}: id ${hex}`);
            a.title = `coral://artifacts/${hex}`;
            frag.append(a);
            at = start + 'coral://artifacts/'.length + 64;
            changed = true;
        }
        if (at === 0) continue;
        if (at < text.length) frag.append(text.slice(at));
        n.replaceWith(frag);
    }
    return changed ? t.innerHTML : html;
}

/** A board message's body as sanitized HTML (board link policy). */
export function renderBoardMessageBody(m) {
    const text = boardMessageText(boardMessageDisplayText(m));
    const id = Number(m && m.id);
    if (!text || !Number.isFinite(id) || typeof marked === 'undefined' || typeof DOMPurify === 'undefined'
        || (typeof hljs === 'undefined' && FENCED_WITH_LANG.test(text))) {
        return highlightMentionsHtml(linkArtifactRefsHtml(renderMarkdown(text, BOARD_MARKDOWN, 'board')));
    }
    const pass = boardBodyPass();
    const key = id + ':' + textHash(text) + ':' + _mentionSig; // a change of the board's members draws a body again
    const hit = _boardBodyCache.get(key);
    if (hit && hit.text === text) { hit.pass = pass; return hit.html; }
    const html = highlightMentionsHtml(linkArtifactRefsHtml(_renderMarkdownUncached(text, BOARD_MARKDOWN, 'board')));
    if (hit) { _boardBodyCache.delete(key); _boardBodyBytes -= hit.bytes; }
    const bytes = 2 * (text.length + html.length) + BOARD_BODY_ENTRY_OVERHEAD; // UTF-16
    if (bytes <= BOARD_BODY_ENTRY_MAX_BYTES && makeBoardBodyRoom(bytes, id, pass)) {
        _boardBodyCache.set(key, { id, text, html, bytes, pass });
        _boardBodyBytes += bytes;
    }
    return html;
}

export function _boardBodyCacheStatsForTests() {
    return { entries: _boardBodyCache.size, bytes: _boardBodyBytes, maxBytes: BOARD_BODY_CACHE_BYTES, ids: [..._boardBodyCache.values()].map(e => e.id) };
}

/** A board sender's avatar (#3590): their icon (at most two characters), else up to two initials
 *  ("QA Engineer" is QE, "Orchestrator (coral-hub)" is OR), escaped as text. Coloured by the
 *  message's --mb-color. */
export function boardAvatar(m, name) {
    const icon = m && m.icon ? Array.from(String(m.icon)).slice(0, 2).join('') : '';
    const words = String(name || '?').replace(/\(.*?\)/g, ' ').trim().split(/\s+/).filter(Boolean);
    const initials = icon || (words.length > 1 ? words[0][0] + words[1][0] : (words[0] || '?').slice(0, 2)).toUpperCase();
    return `<span class="mb-avatar" aria-hidden="true">${escapeHtml(initials)}</span>`;
}

export function labelCodeBlocks(root) {
    for (const code of root.querySelectorAll('pre > code[class*="language-"]')) {
        const lang = /language-([\w+#.-]+)/.exec(code.className);
        if (lang) code.parentElement.dataset.lang = lang[1];
    }
}
