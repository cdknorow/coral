/* The git branch in the agent header (live and history) is capped to a short width with an ellipsis; the whole name shows in place while it is hovered, focused with the keyboard or
 * tapped (#5142). The CSS draws it (data-full on the chip, its ::after); this only toggles .is-open for a tap or Enter/Space and closes it on a tap elsewhere, Escape and
 * blur. The text span carries the name for a screen reader in full, as its own aria-label. */
const SEL = '.terminal-branch-text, .title-branch .branch-text';

function closeAll(except) {
    for (const el of document.querySelectorAll(`${SEL}.is-open`)) {
        if (el === except) continue;
        el.classList.remove('is-open');
        el.setAttribute('aria-expanded', 'false');
    }
}

document.addEventListener('click', (e) => {
    const el = e.target.closest?.(SEL);
    if (!el) { closeAll(null); return; }
    const open = !el.classList.contains('is-open');
    closeAll(el);
    el.classList.toggle('is-open', open);
    el.setAttribute('aria-expanded', String(open));
});
document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { closeAll(null); return; }
    const el = e.target.matches?.(SEL) ? e.target : null;
    if (el && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); el.click(); }
});
document.addEventListener('focusout', (e) => {
    if (e.target.matches?.(SEL)) closeAll(null);
});

// A live header narrower than 22.25 rem of content (the phone's container query drops the branch there; a desktop header beside two panes squeezes it to a few pixels): data-narrow on the header,
// kept by one observer, and the capped text leaves the Tab order, since there is nothing of it to see or focus.
const NARROW_REM = 22.25;
const narrowObserver = new ResizeObserver((entries) => {
    const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
    for (const e of entries) {
        const narrow = e.contentRect.width < NARROW_REM * rem;
        e.target.toggleAttribute('data-narrow', narrow);
        const t = e.target.querySelector('.terminal-branch-text');
        if (t) t.tabIndex = narrow ? -1 : 0;
    }
});
const header = document.querySelector('#live-session-view .terminal-header');
if (header) narrowObserver.observe(header);
else document.addEventListener('DOMContentLoaded', () => { const h = document.querySelector('#live-session-view .terminal-header'); if (h) narrowObserver.observe(h); }, { once: true });
