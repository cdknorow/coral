/* The dropdown list the composer's two autocompletes share (#3794): @file (file_mention.js) and /command (command_mention.js).
 * Each module keeps what is its own (what to search, how to rank, what a row looks like, what picking does); this holds what was
 * the same in both: the list element beside the textarea, its rows and their click, the selection and the keys that move it,
 * Escape and blur closing it, and finding where a trigger character starts a mention. */

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

/** A mention list. `prefix` names its CSS ("file-mention" gives .file-mention-dropdown and .file-mention-item). Fields the owner
 *  reads and sets: results (what the rows stand for), selected (the row index), start (where the mention begins in the text).
 *  opts: onInput(e), onPick(index), onHide() for the owner's own reset. */
export function createMentionList(prefix, opts) {
    const list = {
        el: null,
        results: [],
        selected: 0,
        start: -1,
        itemClass: `${prefix}-item`,

        /** Create the list beside the composer and listen to it. */
        mount(input) {
            list.el = document.createElement('div');
            list.el.className = `${prefix}-dropdown`;
            list.el.style.display = 'none';
            // Appended to the command-editor wrapper so it positions relative to the textarea.
            input.parentElement.appendChild(list.el);
            input.addEventListener('input', opts.onInput);
            input.addEventListener('keydown', list.onKeydown);
            // Hidden a moment after the blur, so a click on a row can fire first.
            input.addEventListener('blur', () => setTimeout(list.hide, 200));
        },

        /** Show the rows (html, one .item per result with its data-index); a click on a row picks it. */
        show(html) {
            list.el.innerHTML = html;
            list.el.style.display = 'block';
            list.el.querySelectorAll(`.${list.itemClass}`).forEach(el => {
                el.addEventListener('mousedown', (e) => {
                    e.preventDefault(); // keep the textarea focused
                    opts.onPick(parseInt(el.dataset.index));
                });
            });
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
