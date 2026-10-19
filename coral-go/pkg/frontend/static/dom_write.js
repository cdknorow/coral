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
