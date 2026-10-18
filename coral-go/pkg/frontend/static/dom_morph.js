/* Keyed-by-source DOM updates for markup the app builds as one HTML string
 * (#3121, the agent list). Building the string is cheap; parsing it into tens of
 * thousands of nodes and laying them out again is not. morphHtml() splits the new
 * markup into its top-level nodes and keeps every existing node whose source
 * string is unchanged. Where a node's opening tag is unchanged but its contents
 * differ, it recurses (a team card whose one agent row changed re-parses that row
 * only). Anything it cannot match exactly (a different number of nodes, a parent
 * someone else rewrote, markup the parser would reshape) is replaced in full, so
 * the result is always what innerHTML would have produced.
 *
 * Kept nodes keep their runtime state (listeners, focus, text filled in later).
 * The caller gets the nodes that are new, to wire up only those.
 *
 * Kill switch: localStorage 'coral-no-morph' = '1' (read on each call), or
 * ?nomorph=1 in the URL the page was loaded with, makes every update a plain
 * innerHTML assignment. */
import { browserLocal } from './safe_storage.js';

// Read once at load: the app rewrites the query string at startup (filter state),
// which drops parameters it does not know.
const _urlNoMorph = (() => { try { return new URLSearchParams(location.search).get('nomorph') === '1'; } catch { return false; } })();

/** True when the kill switch asks for plain innerHTML updates. */
export function morphDisabled() {
    if (_urlNoMorph) return true;
    try { return browserLocal.get('coral-no-morph') === '1'; } catch { return false; }
}

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr']);
const RAW = new Set(['script', 'style', 'textarea', 'title']);
// Never recursed into: the parser treats their contents specially (foreign
// content, implied tags, preserved whitespace), so a child parsed on its own
// would not match.
const ATOMIC = new Set(['svg', 'math', 'table', 'thead', 'tbody', 'tr', 'select', 'pre', 'textarea', 'template', 'details', 'p']);

// One tag: '<', optional '/', the name, then attributes where quoted values may
// contain '<' and '>'. Sticky, so each match starts exactly at the '<' found.
const TAG = /<(\/?)([A-Za-z][\w:-]*)(?:[^"'>]|"[^"]*"|'[^']*')*>/y;

// A part's source outlives the markup it was cut from: it is compared with the next update's.
// html.slice() keeps a view of the whole string in V8 (a sliced string), so the parts of a list
// that grows one node at a time (a board taking a message every few seconds) each held on to the
// full markup of the update they arrived in: one copy per update, 40 MB in 6 minutes on a busy
// board (#4318). A copy holds only its own characters.
const own = s => (s.length < 13 ? s : JSON.parse(JSON.stringify(s)));

/** Split markup into its top-level nodes: [{ src, tag, openEnd }] (tag null for text/comments). */
 function splitTopLevel(html) {
    const out = [];
    const n = html.length;
    let i = 0, depth = 0, start = 0, top = null;
    const flushText = (end) => { if (end > start) out.push({ src: own(html.slice(start, end)), tag: null, openEnd: 0 }); };
    while (i < n) {
        const lt = html.indexOf('<', i);
        if (lt < 0) break;
        i = lt;
        if (html.startsWith('<!--', i)) {
            const end = html.indexOf('-->', i + 4);
            const stop = end < 0 ? n : end + 3;
            if (depth === 0) { flushText(i); out.push({ src: own(html.slice(i, stop)), tag: null, openEnd: 0 }); start = stop; }
            i = stop;
            continue;
        }
        TAG.lastIndex = i;
        const m = TAG.exec(html);
        if (!m) { i++; continue; } // a stray '<' in text
        const tagEnd = TAG.lastIndex;
        const name = m[2].toLowerCase();
        if (m[1]) { // closing tag
            depth = Math.max(0, depth - 1);
            i = tagEnd;
            if (depth === 0 && top) { out.push({ src: own(html.slice(top.start, i)), tag: top.tag, openEnd: top.openEnd - top.start }); start = i; top = null; }
            continue;
        }
        const selfClosing = VOID.has(name) || html.charCodeAt(tagEnd - 2) === 47; // '/>'
        if (depth === 0) { flushText(i); top = { start: i, tag: name, openEnd: tagEnd }; }
        if (selfClosing) {
            i = tagEnd;
            if (depth === 0) { out.push({ src: own(html.slice(top.start, i)), tag: name, openEnd: tagEnd - top.start }); start = i; top = null; }
            continue;
        }
        depth++;
        if (RAW.has(name)) {
            const close = html.toLowerCase().indexOf('</' + name, tagEnd);
            i = close < 0 ? n : close; // its closing tag is matched next
            continue;
        }
        i = tagEnd;
    }
    if (top) out.push({ src: own(html.slice(top.start)), tag: top.tag, openEnd: top.openEnd - top.start }); // unclosed: keep as one node
    else flushText(n);
    return out;
}

const _state = new WeakMap(); // parent -> { parts: [{src, tag, openEnd}], nodes: Node[] }
/** Counts since load, for the perf suite: full rebuilds and single-node replacements. */
export const morphStats = { full: 0, replaced: 0, kept: 0, appended: 0 };

/** splitTopLevel(html), reusing `prev` (the parts of the markup this replaces):
 *  pieces unchanged at the start and the end are matched with native string
 *  compares, and only the changed middle is tokenized. The tokenizer's state is
 *  the same after an identical prefix, so the result equals splitTopLevel(html). */
 function splitAgainst(html, prev) {
    if (!prev || !prev.length) return splitTopLevel(html);
    let off = 0, k = 0;
    while (k < prev.length && html.startsWith(prev[k].src, off)) { off += prev[k].src.length; k++; }
    if (k === prev.length && off === html.length) return prev;
    let end = html.length, j = prev.length;
    while (j > k && end - prev[j - 1].src.length >= off && html.endsWith(prev[j - 1].src, end)) { end -= prev[j - 1].src.length; j--; }
    return [...prev.slice(0, k), ...splitTopLevel(html.slice(off, end)), ...prev.slice(j)];
}

function parseNodes(html) {
    const t = document.createElement('template');
    t.innerHTML = html;
    return [...t.content.childNodes];
}

function full(parent, html, parts, added) {
    morphStats.full++;
    parent.innerHTML = html;
    const nodes = [...parent.childNodes];
    if (nodes.length === parts.length) _state.set(parent, { parts, nodes });
    else _state.delete(parent); // the parser reshaped it: next time is a full update again
    for (const nd of nodes) if (nd.nodeType === 1) added.push(nd);
}

function morphInto(parent, html, depth, added) {
    const prev = _state.get(parent);
    const parts = splitAgainst(html, prev && prev.parts);
    const live = parent.childNodes;
    const liveMatches = prev && live.length === prev.nodes.length && prev.nodes.every((nd, k) => live[k] === nd);
    // More nodes at the end and the old ones unchanged (a message posted to a board, an agent that joined): keep every old
    // node and parse only the tail. Anything the tail's own parse would reshape falls through to a full update.
    // What this relies on: the markup is balanced at the top level, each part a complete node. innerHTML on the whole string
    // and a parse of the tail alone then give the same nodes. That holds for the old parts because the state is only kept
    // when they parsed to exactly one node each (full() below), and liveMatches checks the live children are those nodes; for
    // the tail it is checked after the fact, by the tail parsing to exactly as many nodes as it has parts. A tail that the
    // parser would nest into an earlier part (a stray end tag, an unclosed element) changes that count and takes the full path.
    if (liveMatches && parts.length > prev.parts.length && prev.parts.every((was, k) => was.src === parts[k].src)) {
        const tail = parseNodes(parts.slice(prev.parts.length).map(p => p.src).join(''));
        if (tail.length === parts.length - prev.parts.length) {
            morphStats.kept += prev.parts.length;
            morphStats.appended += tail.length;
            for (const nd of tail) { parent.appendChild(nd); if (nd.nodeType === 1) added.push(nd); }
            _state.set(parent, { parts, nodes: [...prev.nodes, ...tail] });
            return;
        }
    }
    const aligned = liveMatches && prev.parts.length === parts.length;
    if (!aligned) { full(parent, html, parts, added); return; }
    const nodes = prev.nodes.slice();
    for (let k = 0; k < parts.length; k++) {
        const now = parts[k], was = prev.parts[k];
        if (now.src === was.src) { morphStats.kept++; continue; }
        const old = nodes[k];
        if (depth > 0 && old.nodeType === 1 && now.tag && now.tag === was.tag && !ATOMIC.has(now.tag)
            && now.src.slice(0, now.openEnd) === was.src.slice(0, was.openEnd)) {
            // Same element, same attributes: update its children only.
            if (!_state.has(old)) {
                const oldInner = was.src.slice(was.openEnd, was.src.lastIndexOf('</'));
                const oldParts = splitTopLevel(oldInner);
                if (old.childNodes.length === oldParts.length) _state.set(old, { parts: oldParts, nodes: [...old.childNodes] });
            }
            const inner = now.src.slice(now.openEnd, now.src.lastIndexOf('</'));
            morphInto(old, inner, depth - 1, added);
            continue;
        }
        const fresh = parseNodes(now.src);
        if (fresh.length !== 1) { full(parent, html, parts, added); return; }
        morphStats.replaced++;
        parent.replaceChild(fresh[0], old);
        nodes[k] = fresh[0];
        if (fresh[0].nodeType === 1) added.push(fresh[0]);
    }
    _state.set(parent, { parts, nodes });
}

// What names a control across a redraw: its tag, id and data-* attributes, and those of each ancestor up to the parent (classes and
// aria text are left out: they carry state, such as is-selected, that is what changed).
function focusKey(el, parent) {
    const out = [];
    for (let e = el; e && e !== parent; e = e.parentElement) {
        let k = e.tagName + (e.id ? '#' + e.id : '');
        for (const a of e.attributes) if (a.name.startsWith('data-')) k += `[${a.name}=${a.value}]`;
        out.push(k);
    }
    return out.join('<');
}

/** Call before a background redraw of `parent`; call the result after it. If keyboard focus was on a control inside `parent` and that node
 *  was replaced (focus falls to the body), focus goes to the control that matches it in the new markup: same tag, id and data-* attributes,
 *  and the same place among its twins. A control that is gone, or still in the page, is left alone. A screen-reader or keyboard user on
 *  a refreshed list stays where they were (#4882). */
export function rememberFocus(parent) {
    const a = document.activeElement;
    if (!a || a === document.body || a === parent || !parent.contains(a)) return () => {};
    const key = focusKey(a, parent);
    const same = e => focusKey(e, parent) === key;
    const nth = [...parent.querySelectorAll(a.tagName)].filter(same).indexOf(a);
    // A text field being typed in keeps what was typed and the caret: a redraw must not empty the box under the user's hands.
    const typing = (a.tagName === 'TEXTAREA' || (a.tagName === 'INPUT' && /^(text|search|email|url|tel|password|number)?$/.test(a.type))) && a.value
        ? { value: a.value, start: a.selectionStart, end: a.selectionEnd } : null;
    return () => {
        if (a.isConnected) return;
        const twins = [...parent.querySelectorAll(a.tagName)].filter(same);
        const again = twins[Math.min(Math.max(nth, 0), twins.length - 1)];
        if (!again) return;
        if (typing && !again.value) { again.value = typing.value; try { again.setSelectionRange(typing.start, typing.end); } catch { /* a type with no caret */ } }
        again.focus({ preventScroll: true });
    };
}

/** Make `parent`'s contents equal `html`, touching only what changed. Returns the
 *  element nodes that are new (the caller wires listeners on those only); when the
 *  whole contents were replaced, that is every top-level element. Focus on a control
 *  that was replaced goes to its replacement (rememberFocus). */
export function morphHtml(parent, html, { depth = 3 } = {}) {
    const restoreFocus = rememberFocus(parent);
    if (morphDisabled()) {
        _state.delete(parent);
        parent.innerHTML = html;
        restoreFocus();
        return [...parent.children];
    }
    const added = [];
    morphInto(parent, html, depth, added);
    restoreFocus();
    return added;
}

/** Forget what morphHtml knows about `parent` (after writing its contents another way). */
export function resetMorph(parent) { _state.delete(parent); }
