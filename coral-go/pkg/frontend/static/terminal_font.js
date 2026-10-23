/* The terminal's default font size (#6577, the operator: "the phone terminal is hard to use, text could be smaller"). With no size chosen in Settings (terminal_font_size is unset), a window of a phone's width
 * draws the terminal at PHONE_FONT: a 390 px screen then holds about 60 columns instead of 46, so an agent's screen wraps less; every other window keeps DESKTOP_FONT. A size the person chose in Settings always wins,
 * on every window, and the fit-to-width floors (xterm_renderer.js, #6231) are unchanged. No imports: the Settings dialog, its save diff and the renderer all ask it. */
export const DESKTOP_FONT = 13;
export const PHONE_FONT = 10;
const PHONE_MAX_WIDTH = 767; // the mobile layout's breakpoint (mobile.js, css/mobile.css)

/** The default size for this window: the phone's when it is phone-wide, else the desktop's. */
export function defaultTerminalFont() {
    return typeof window !== 'undefined' && window.innerWidth <= PHONE_MAX_WIDTH ? PHONE_FONT : DESKTOP_FONT;
}

/** The size to draw at: the person's own setting when it is a number, else this window's default. */
export function terminalFontFor(setting) {
    return parseInt(setting, 10) || defaultTerminalFont();
}
