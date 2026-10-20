/* Writes that touch the DOM only when the value changes (#5010). The list and the open agent's header are redrawn from every feed message, and most
 * messages change none of what these write: setting a node's text, an attribute or its hidden flag to what it already holds still counts as a mutation
 * (it replaces the text node, or queues a style recalculation), and a page with a long chat in it pays for each one in the frame that follows. */

export function setText(el, text) {
    if (el && el.textContent !== text) el.textContent = text;
}

export function setHidden(el, hidden) {
    if (el && el.hidden !== hidden) el.hidden = hidden;
}

export function setAttr(el, name, value) {
    const v = String(value);
    if (el && el.getAttribute(name) !== v) el.setAttribute(name, v);
}

export function setTitle(el, title) {
    if (el && el.title !== title) el.title = title;
}

export function setClassName(el, className) {
    if (el && el.className !== className) el.className = className;
}

export function setDisplay(el, display) {
    if (el && el.style.display !== display) el.style.display = display;
}

export function setChecked(el, checked) {
    if (el && el.checked !== checked) el.checked = checked;
}

export function setHtml(el, html) {
    if (el && el.innerHTML !== html) el.innerHTML = html;
}

export function setData(el, key, value) {
    if (el && el.dataset[key] !== value) el.dataset[key] = value;
}

// An overlay pill (the "waiting for input" and "server unreachable" notices, #5287): it takes no layout space (css .live-pills), appears only after
// PILL_SHOW_DELAY_MS of being wanted, and goes at once when it is not. The wait is not restarted by the feed updates that repeat the same state, so a
// quick drop and return never flashes it, and a lasting one shows once. The text is read when it shows, and set at once while it is shown.
export const PILL_SHOW_DELAY_MS = 1500;
const _pills = new WeakMap();
export function setPill(el, show, text) {
    if (!el) return;
    let st = _pills.get(el);
    if (!st) _pills.set(el, st = { timer: null, text: '' });
    st.text = text;
    if (!show) {
        if (st.timer !== null) { clearTimeout(st.timer); st.timer = null; }
        setHidden(el, true);
        return;
    }
    if (!el.hidden) { setText(el, text); return; }
    if (st.timer !== null) return; // already counting: a repeat of the same state does not restart it
    st.timer = setTimeout(() => {
        st.timer = null;
        setText(el, st.text);
        setHidden(el, false);
    }, PILL_SHOW_DELAY_MS);
}

/** Replace `container`'s content with one placeholder line (#5162): <tag class="className">text</tag>, "No usage data yet" in the cost tables, "No messages yet" on the board. The text is text, never markup. */
export function emptyState(container, text, className = 'empty-state', tag = 'div') {
    const el = document.createElement(tag);
    el.className = className;
    el.textContent = text;
    container.replaceChildren(el);
}
