/* The dropdown list the composer's two autocompletes share (#3794): @file (file_mention.js) and /command (command_mention.js).
 * Each module keeps what is its own (what to search, how to rank, what a row looks like, what picking does); this holds what was
 * the same in both: the list element beside the textarea, its rows and their click, the selection and the keys that move it,
 * Escape and blur closing it, and finding where a trigger character starts a mention. Rows are given as data (text runs), never
 * as markup: the list builds them with text nodes, so nothing a file or command is called can be read as HTML. */

/** Where the mention being typed starts: the index of `trigger` at the end of `text` when it begins the text or follows
 *  whitespace and nothing breaks the word after it; otherwise -1 ("foo@bar" and "a/b" open nothing). */
export function findTriggerStart(text, trigger) {
    // Walk backwards from the end: whitespace before the trigger means there is no active mention.
    for (let i = text.length - 1; i >= 0; i--) {
        const ch = text[i];
        if (ch === ' ' || ch === '\n' || ch === '\t') return -1;
        if (ch === trigger) return (i === 0 || /\s/.test(text[i - 1])) ? i : -1;
    }
    return -1;
}

/** Text runs for a name with matched letters: consecutive matched characters are merged into one {strong} run.
 *  `matched` is a Set of character positions. Returns parts: strings and {strong: string}. */
export function highlightRuns(text, matched) {
    const parts = [];
    let cur = '', inStrong = false;
    for (let i = 0; i < text.length; i++) {
        const isMatch = matched.has(i);
        if (isMatch !== inStrong && cur) { parts.push(inStrong ? { strong: cur } : cur); cur = ''; }
        inStrong = isMatch;
        cur += text[i];
    }
    if (cur) parts.push(inStrong ? { strong: cur } : cur);
    return parts;
}

/** Append parts (strings and {strong}) to `parent` as text nodes and <strong> elements. */
function appendParts(parent, parts) {
    for (const part of parts) {
        if (typeof part === 'string') parent.appendChild(document.createTextNode(part));
        else { const b = document.createElement('strong'); b.textContent = part.strong; parent.appendChild(b); }
    }
}

/** A mention list. `classes` names its CSS: { dropdown, item } (written out by each owner, so the class names stay greppable and the
 *  unused-CSS lint sees them). Fields the owner
 *  reads and sets: results (what the rows stand for), selected (the row index), start (where the mention begins in the text).
 *  opts: onInput(e), onPick(index), onHide() for the owner's own reset. */
export function createMentionList(classes, opts) {
    const list = {
        el: null,
        results: [],
        selected: 0,
        start: -1,
        itemClass: classes.item,

        /** Create the list beside the composer and listen to it. */
        mount(input) {
            list.el = document.createElement('div');
            list.el.className = classes.dropdown;
            list.el.style.display = 'none';
            // Appended to the command-editor wrapper so it positions relative to the textarea.
            input.parentElement.appendChild(list.el);
            input.addEventListener('input', opts.onInput);
            input.addEventListener('keydown', list.onKeydown);
            // Hidden a moment after the blur, so a click on a row can fire first.
            input.addEventListener('blur', () => setTimeout(list.hide, 200));
        },

        /** Show rows: [{ cells: [{ cls?, parts }], sep? }] (sep: text between the cells, as the markup had whitespace between them; a cell with a cls is a <span class=cls>, one without is bare text in the
         *  row), the selected one marked, and an optional header { cls, text } above them. A click on a row picks it. */
        show(rows, header = null) {
            const nodes = [];
            if (header) {
                const h = document.createElement('div');
                h.className = header.cls;
                h.textContent = header.text;
                nodes.push(h);
            }
            rows.forEach((row, i) => {
                const div = document.createElement('div');
                div.className = list.itemClass + (i === list.selected ? ' selected' : '');
                div.dataset.index = String(i);
                row.cells.forEach((cell, k) => {
                    if (k > 0 && row.sep) div.appendChild(document.createTextNode(row.sep));
                    if (cell.cls) {
                        const span = document.createElement('span');
                        span.className = cell.cls;
                        appendParts(span, cell.parts);
                        div.appendChild(span);
                    } else appendParts(div, cell.parts);
                });
                div.addEventListener('mousedown', (e) => {
                    e.preventDefault(); // keep the textarea focused
                    opts.onPick(i);
                });
                nodes.push(div);
            });
            list.el.replaceChildren(...nodes);
            list.el.style.display = 'block';
        },

        hide() {
            if (list.el) list.el.style.display = 'none';
            list.results = [];
            list.start = -1;
            if (opts.onHide) opts.onHide();
        },

        visible() {
            return !!list.el && list.el.style.display !== 'none';
        },

        updateSelection() {
            if (!list.el) return;
            list.el.querySelectorAll(`.${list.itemClass}`).forEach((el, i) => el.classList.toggle('selected', i === list.selected));
            // Keep the selected row in view.
            const selected = list.el.querySelector(`.${list.itemClass}.selected`);
            if (selected) selected.scrollIntoView({ block: 'nearest' });
        },

        onKeydown(e) {
            if (!list.visible()) return;
            if (e.key === 'ArrowDown') {
                e.preventDefault();
                list.selected = Math.min(list.selected + 1, list.results.length - 1);
                list.updateSelection();
            } else if (e.key === 'ArrowUp') {
                e.preventDefault();
                list.selected = Math.max(list.selected - 1, 0);
                list.updateSelection();
            } else if (e.key === 'Tab' || e.key === 'Enter') {
                if (list.results.length > 0) {
                    e.preventDefault();
                    e.stopImmediatePropagation();
                    opts.onPick(list.selected);
                }
            } else if (e.key === 'Escape') {
                e.preventDefault();
                e.stopImmediatePropagation();
                list.hide();
            }
        },
    };
    return list;
}
