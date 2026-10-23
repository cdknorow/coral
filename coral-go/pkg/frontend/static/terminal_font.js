/* The terminal's default font size (#6577, the operator: "the phone terminal is hard to use, text could be smaller"). With no size chosen in Settings (terminal_font_size is unset), a window of a phone's width
 * draws the terminal at PHONE_FONT: a 390 px screen then holds about 60 columns instead of 46, so an agent's screen wraps less; every other window keeps DESKTOP_FONT. A size the person chose in Settings always wins,
 * on every window, and the fit-to-width floors (xterm_renderer.js, #6231) are unchanged. No imports: the Settings dialog, its save diff and the renderer all ask it. */
export const DESKTOP_FONT = 13;
export const PHONE_FONT = 10;
const PHONE_MAX_WIDTH = 767; // the mobile layout's breakpoint (mobile.js, css/mobile.css)
const PHONE_SHORT_SIDE = 500; // a phone's short side, turned either way: a tablet's is more

/** Is this window a phone's? Narrow (the mobile layout's breakpoint), or a touch screen whose short side is a phone's: a phone turned to landscape (844x390, 800x360, with the keyboard up 844x200) is wider than
 *  the breakpoint and draws the desktop layout, but it is still a phone: it follows the shared pane's size and never sets it (#6675), and it draws the phone's font. */
export function isPhoneViewer() {
    if (typeof window === 'undefined') return false;
    if (window.innerWidth <= PHONE_MAX_WIDTH) return true;
    const coarse = typeof window.matchMedia === 'function' && window.matchMedia('(pointer: coarse)').matches;
    return coarse && Math.min(window.innerWidth, window.innerHeight) <= PHONE_SHORT_SIDE;
}

/** The default size for this window: the phone's when it is a phone, else the desktop's. */
export function defaultTerminalFont() {
    return isPhoneViewer() ? PHONE_FONT : DESKTOP_FONT;
}

/** The size to draw at: the person's own setting when it is a number, else this window's default. */
export function terminalFontFor(setting) {
    return parseInt(setting, 10) || defaultTerminalFont();
}
