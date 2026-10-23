/* A textarea that grows with its text up to its CSS max-height, the way the chat's command box does (command_autosize.js), for the team board's composer (#6239). */

/** Sizes the box to its text now, and again on typing, on a programmatic value change (a send clears it, a mention fills it) and when its width changes. */
export function initGrowingTextarea(input) {
    if (!input || input.dataset.growing) return;
    input.dataset.growing = '1';
    let width = 0;
    const grow = () => {
        if (!input.offsetParent) return;   // hidden (select mode, another tab): the observer sizes it when it shows
        input.style.height = '0px';   // collapse first so scrollHeight measures the text, not the old height
        input.style.height = input.scrollHeight + 'px';
    };
    const desc = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value');
    Object.defineProperty(input, 'value', {
        configurable: true,
        get() { return desc.get.call(this); },
        set(v) { desc.set.call(this, v); grow(); },
    });
    input.addEventListener('input', grow);
    if (typeof ResizeObserver === 'function') {
        new ResizeObserver(entries => {
            const w = Math.round(entries[entries.length - 1].contentRect.width);
            if (w !== width) { width = w; grow(); }
        }).observe(input);
    }
    grow();
}
