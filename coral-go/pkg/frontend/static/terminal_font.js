/* The terminal's default font size (#6577, the operator: "the phone terminal is hard to use, text could be smaller"). With no size chosen in Settings (terminal_font_size is unset), a window of a phone's width
 * draws the terminal at PHONE_FONT: a 390 px screen then holds about 60 columns instead of 46, so an agent's screen wraps less; every other window keeps DESKTOP_FONT. A size the person chose in Settings always wins,
 * on every window, and the fit-to-width floors (xterm_renderer.js, #6231) are unchanged. Its one import is phone_layout.js (no imports of its own). */
import { isPhoneLayout } from './phone_layout.js';
export const DESKTOP_FONT = 13;
export const PHONE_FONT = 10;

/** Is this window a phone's (phone_layout.js, #6683)? A narrow window, or a touch screen at most 500 px high: a phone turned to landscape is wider than the breakpoint and gets the phone's layout, follows the
 *  shared pane's size and never sets it (#6675), and draws the phone's font. A tablet (touch, taller) and a desktop window (a mouse) are not phones. */
export function isPhoneViewer() { return isPhoneLayout(); }

/** The default size for this window: the phone's when it is a phone, else the desktop's. */
export function defaultTerminalFont() {
    return isPhoneViewer() ? PHONE_FONT : DESKTOP_FONT;
}

/** The size to draw at: the person's own setting when it is a number, else this window's default. */
export function terminalFontFor(setting) {
    return parseInt(setting, 10) || defaultTerminalFont();
}
