/* The command box grows with what is typed (moved out of live_chat.js, #5065; no behaviour change). */
import { DESKTOP_QUERY } from './phone_layout.js';
import { state } from './state.js';
import { fitTerminal } from './xterm_renderer.js';

// On desktop the command box grows with its text, like a chat composer
// (CSS caps it at 40vh). Mobile keeps its own compact input bar.
const desktopComposer = window.matchMedia(DESKTOP_QUERY);

// What the last autosize measured for: the text, the box's width (a ResizeObserver keeps it; 0 while the box is hidden), the window
// height (the 40vh cap) and the mode. Measuring forces layout several times (#3866: 112 ms over five agent switches), so
// when none of these changed since the last run the result cannot have changed either and there is nothing to do.
let _autosizeKey = "";

let _inputWidth = 0;

// A phone's box has a fixed range of heights (mobile.css); at a large text size the placeholder wraps onto more lines than the range holds
// and is cut off. An empty box is made tall enough for its placeholder (up to 40 percent of the window); a box with text keeps the range.
function fitPlaceholder(input) {
    input.style.removeProperty("min-height");
    input.style.removeProperty("max-height");
    input.style.removeProperty("--ph-scale");
    if (input.value || !input.placeholder || input.clientWidth < 40) return;   // not laid out yet: nothing to measure
    const cs = getComputedStyle(input);
    // A word of the placeholder wider than the box broke inside itself ("Messag / e"): the placeholder's size is scaled down, as far as the box needs and no further (never below 60 percent), until its widest word
    // fits; the placeholder wraps only at spaces then, whatever the word, the box or the text size (#6971; the CSS reads --ph-scale on a phone's layout).
    const room = input.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    const ruler = document.createElement("span");
    ruler.style.cssText = `position:absolute;visibility:hidden;white-space:nowrap;font:${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily};letter-spacing:${cs.letterSpacing}`;
    document.body.appendChild(ruler);
    let widest = 0;
    for (const word of input.placeholder.split(/\s+/)) { if (!word) continue; ruler.textContent = word; widest = Math.max(widest, ruler.getBoundingClientRect().width); }
    ruler.remove();
    const scale = widest > room && widest > 0 ? Math.max(0.6, Math.floor((room / widest) * 100) / 100) : 1;
    if (scale < 1) input.style.setProperty("--ph-scale", String(scale));
    const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom) + parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth);
    const probe = document.createElement("div");
    probe.style.cssText = `position:absolute;visibility:hidden;white-space:pre-wrap;overflow-wrap:break-word;box-sizing:content-box;width:${input.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)}px;font:${cs.font};line-height:${cs.lineHeight};letter-spacing:${cs.letterSpacing};font-size:calc(${cs.fontSize} * ${scale})`;
    probe.textContent = input.placeholder;
    document.body.appendChild(probe);
    const need = Math.min(probe.getBoundingClientRect().height + pad, window.innerHeight * 0.4);
    probe.remove();
    if (need > input.offsetHeight + 0.5) {
        input.style.setProperty("min-height", `${need}px`, "important");
        input.style.setProperty("max-height", `${need}px`, "important");
    }
}

export function autosizeCommandInput() {
    const input = document.getElementById("command-input");
    if (!input) return;
    const key = [input.value, input.placeholder, _inputWidth, window.innerHeight, desktopComposer.matches, getComputedStyle(input).fontSize,
        document.getElementById("command-pane")?.classList.contains("chat-mode")].join("\u0001");
    if (key === _autosizeKey) return;
    _autosizeKey = key;
    const before = input.offsetHeight;
    if (!desktopComposer.matches || !input.offsetParent) {
        input.style.height = "";
        fitPlaceholder(input);
        return;
    }
    // Collapse first so scrollHeight measures the text, not the old height
    input.style.height = "0px";
    input.style.height = input.scrollHeight + "px";
    if (input.offsetHeight === before) return;
    if (document.getElementById("capture-wrapper")?.classList.contains("chat-mode")) {
        // Keep the newest reply in view as the box grows
        const messages = document.getElementById("live-history-messages");
        if (messages && state.autoScroll) messages.scrollTop = messages.scrollHeight;
    } else {
        // The terminal area changed size; refit xterm to it
        fitTerminal();
    }
}

/** Autosize the command box on typing and on every programmatic value change
 *  (send clears it, session switches restore drafts, macros fill it). */
export function initCommandInputAutosize() {
    const input = document.getElementById("command-input");
    if (!input) return;
    const desc = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    Object.defineProperty(input, "value", {
        configurable: true,
        get() { return desc.get.call(this); },
        set(v) { desc.set.call(this, v); autosizeCommandInput(); },
    });
    input.addEventListener("input", autosizeCommandInput);
    window.addEventListener("resize", autosizeCommandInput);
    if (typeof ResizeObserver === "function") {
        new ResizeObserver(entries => {
            const width = Math.round(entries[entries.length - 1].contentRect.width);
            if (width !== _inputWidth) { _inputWidth = width; autosizeCommandInput(); }
        }).observe(input);
    }
}
