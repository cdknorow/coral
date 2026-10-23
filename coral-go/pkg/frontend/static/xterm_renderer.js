/* xterm.js terminal renderer — streams raw ANSI output via WebSocket */

import { setTerminalFocused, setTerminalSize, setTerminalState } from './terminal_statusbar.js';
import { state } from './state.js';
import { markRestarting, currentIsRestarting, showRestartingOverlay, hideSessionOverlay } from './session_overlay.js';
import { isInteractiveOwner, claimOwnership, ownershipSessionId } from './ownership.js';
import { dbg, showToast } from './utils.js';
import { normServer, serverWsBase, sessionServer } from './server_base.js';
import { hubBlocksKeystrokes, applyHubReadOnlyFrame } from './hub_access.js';
import { feedRetryDelay } from './feed_pacing.js';
import { isSignedOut } from './overload.js';
import { browserLocal } from './safe_storage.js';
import { cellHeight, clearRenderer, onUserInput, screenElement } from './xterm_internals.js'; // the only place that reads what xterm does not document (#5411)

let terminal = null;
let fitAddon = null;
let terminalWs = null;
let _selectionDisposable = null;
let _onDataDisposable = null;
let _onResizeDisposable = null;
let _resizeObserver = null;
let _panelDragging = false;

// ── The pane's one size (#3422) ──────────────────────────────────────────
// The tmux pane has one size, which the server reports on the socket (terminal_size). Only a
// window that may set it (the interactive owner, with Interact, wider than a phone) fits its
// container and resizes the pane. Every other window (a viewer tab, View mode through the hub,
// a phone) renders the xterm at the pane's own size and scrolls, instead of wrapping lines drawn
// for a wider pane at its own width. A window that sets the size follows a change made elsewhere
// (another browser or device) instead of fighting it, until it is resized or typed in again.
// A backend that can't report the pane's size sends none: then every window fits its container.
let _pane = null;        // { cols, rows } the server last reported for this socket, or null
let _following = false;  // rendering at the pane's size rather than this window's own
let _containerEl = null;
// Each resize on a socket carries a sequence number, which the server echoes on the report that
// answers it: an answer to an earlier resize, and the server's first report (written before it reads
// anything this window sent), are not someone else's change.
let _seq = 0;            // the last resize sent on the current socket (0: none yet)
let _askedSize = null;   // { cols, rows } that resize asked for
let _followBox = '';     // the container's size when this window started following
const PHONE_MAX_WIDTH = 767; // the mobile layout's breakpoint (mobile.js, css/mobile.css)

// ── Fit to width (#3455) ─────────────────────────────────────────────────
// While following a pane wider than the window (a phone, a narrow viewer), the operator can scale
// the font down so every column fits instead of scrolling sideways. Only the font changes: the
// pane keeps its size for everyone. Below MIN_FIT_FONT it stops shrinking and scrolls the rest.
// Remembered per device.
const FIT_WIDTH_KEY = 'coral-terminal-fit-width';
const MIN_FIT_FONT = 7;
let _baseFont = 13;      // the font size from Settings, used whenever not fitting
let _fitWidth = (() => { try { return browserLocal.get(FIT_WIDTH_KEY) === '1'; } catch { return false; } })();

function _fitButton() {
    return document.getElementById('terminal-fit-width-btn');
}

// The width the xterm may take inside its container (no scrollbar, no padding).
function _availWidth() {
    const el = _containerEl;
    if (!el) return 0;
    const cs = getComputedStyle(el);
    return el.clientWidth - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0);
}

// The xterm's drawn width at the current font, and what it would be at the Settings font.
function _screenWidth() {
    const screen = screenElement(terminal);
    return screen ? screen.getBoundingClientRect().width : 0;
}

function _setFont(size) {
    if (terminal && terminal.options.fontSize !== size) terminal.options.fontSize = size;
}

// Applies the fit-to-width font, or the Settings font, and shows the toggle when it can help.
function _applyFitWidth() {
    if (!terminal) { _updateFitButton(false); return; }
    if (!_following || !_fitWidth) {
        _setFont(_baseFont);
        _updateFitButton(_following && _overflowsAtBase());
        return;
    }
    const avail = _availWidth();
    const cur = terminal.options.fontSize || _baseFont;
    const w = _screenWidth();
    if (!(avail > 0 && w > 0)) return; // not laid out yet: the next size change applies it
    // The drawn width roughly scales with the font: start at the size that just fits, then, as cells
    // are whole pixels, step down while it overflows and up while the next size still fits.
    let size = Math.max(MIN_FIT_FONT, Math.min(_baseFont, Math.floor((cur * avail / w) * 2) / 2));
    _setFont(size);
    while (size > MIN_FIT_FONT && _screenWidth() > avail) {
        size = Math.max(MIN_FIT_FONT, size - 0.5);
        _setFont(size);
    }
    while (size < _baseFont && _screenWidth() <= avail) {
        _setFont(size + 0.5);
        if (_screenWidth() > avail) { _setFont(size); break; }
        size += 0.5;
    }
    _updateFitButton(true);
}

// True when the pane at the Settings font is wider than the window.
function _overflowsAtBase() {
    const cur = terminal && terminal.options.fontSize;
    const w = _screenWidth();
    if (!cur || !w) return false;
    return w * (_baseFont / cur) > _availWidth() + 1;
}

function _updateFitButton(show) {
    const btn = _fitButton();
    if (!btn) return;
    btn.hidden = !show;
    btn.setAttribute('aria-pressed', String(_fitWidth));
    const label = _fitWidth ? 'Show the terminal at full size (scroll sideways)' : 'Fit the terminal to the screen width';
    btn.setAttribute('aria-label', label);
    btn.title = label;
}

/** Turns fit to width on or off on this device (#3455). */
 function toggleTerminalFitWidth(on = !_fitWidth) {
    _fitWidth = !!on;
    try { browserLocal.set(FIT_WIDTH_KEY, _fitWidth ? '1' : '0'); } catch { /* private mode */ }
    _applyFitWidth();
}

/** Tests: the font size the xterm draws at now. */
export function terminalFontSize() {
    return terminal ? terminal.options.fontSize : null;
}

function _isPhoneWidth() {
    return window.innerWidth <= PHONE_MAX_WIDTH;
}

/** True when this window may set the shared pane's size: the interactive owner of the selected
 *  agent, not limited to View by the hub or the machine, and not a phone (a phone never shrinks
 *  the pane for others). A window that has not joined the agent's ownership group yet (a popout
 *  booting) is not its owner: isInteractiveOwner() alone says yes there, and such a window once
 *  resized the shared pane to its own width under the window that owns it (#3422). */
export function mayResizePane() {
    const s = state.currentSession;
    // An agent without a session id has no ownership group: the rule before groups applies.
    if (s && s.session_id && ownershipSessionId() !== s.session_id) return false;
    if (!isInteractiveOwner() || _isPhoneWidth()) return false;
    return !(s && s.hubReadOnly);
}

/** True while the xterm shows the pane at the pane's size instead of fitting this window. */
export function isFollowingPane() {
    return _following;
}

function _sendResize(cols, rows) {
    if (terminalWs && terminalWs.readyState === WebSocket.OPEN) {
        terminalWs.send(JSON.stringify({ type: 'terminal_resize', cols, rows, seq: ++_seq }));
        _askedSize = { cols, rows };
    }
}

// The container's outer size: scrollbars that following adds or removes do not change it.
function _box() {
    return _containerEl ? _containerEl.offsetWidth + 'x' + _containerEl.offsetHeight : '';
}

// A terminal that follows a pane taller than its window (Coral Hub's View mode, a viewer, a phone) scrolls inside its container, and the container starts at the top: the prompt, at
// the bottom of the pane, was below the visible area (#4586). Keep the cursor line in view as the pane's size arrives, the window resizes and output moves the cursor, until the
// person scrolls the cursor out of view themselves; typing brings it back.
let _scrolledAway = false;  // the person scrolled so that the cursor line is out of view: leave it
let _revealing = false;     // our own scroll, which is not theirs
let _revealScroll = null;
let _cursorDisposable = null;

function _cursorBox(el) {
    const screen = screenElement(terminal);
    if (!screen || !(terminal.rows > 0)) return null;
    const sr = screen.getBoundingClientRect(), cr = el.getBoundingClientRect();
    const cell = sr.height / terminal.rows;
    const top = sr.top - cr.top + el.scrollTop + terminal.buffer.active.cursorY * cell;
    return { top, bottom: top + cell, pad: parseFloat(getComputedStyle(el).paddingBottom) || 0 };
}

function revealCursor() {
    const el = _containerEl;
    if (!terminal || !el || !_following || _scrolledAway) return;
    const box = _cursorBox(el);
    if (!box) return;
    _revealing = true;
    if (box.bottom + box.pad > el.scrollTop + el.clientHeight) el.scrollTop = box.bottom + box.pad - el.clientHeight;
    else if (box.top < el.scrollTop) el.scrollTop = box.top;
    requestAnimationFrame(() => { _revealing = false; });
}

// Render at the pane's size (horizontal scroll when it is wider than the window).
function _followPane() {
    if (!terminal || !_pane) return;
    if (!_following) _followBox = _box();
    _following = true;
    if (_containerEl) _containerEl.classList.add('xterm-follow-pane');
    if (terminal.cols !== _pane.cols || terminal.rows !== _pane.rows) terminal.resize(_pane.cols, _pane.rows);
    _applyFitWidth();
    requestAnimationFrame(revealCursor); // the pane's rows are in: show the prompt
}

// FitAddon counts rows from the container's computed height, which includes its padding (the page is border-box), so the last row
// landed in the bottom padding and, with the bar under the terminal (#4044), could touch it. Count the rows that fit inside the
// padding instead; the columns are the addon's, as before.
function _fit() {
    const dims = typeof fitAddon.proposeDimensions === 'function' ? fitAddon.proposeDimensions() : null;
    if (!dims) { fitAddon.fit(); return; }
    if (!(dims.cols > 0) || !(dims.rows > 0)) return;
    // A window-sized terminal never needs more rows or columns than the window has room for (a cell is never smaller than 6 x 3 px). The rows come from the container's own height, and a
    // container that is not constrained yet (the view just switched, a flex parent not laid out) is as tall as its content, which is the terminal: each fit then adds rows, and the next fit
    // adds more. A run on a Linux runner reached 113,987 rows (a four million pixel container, buffers and repaints for all of them: a 5 to 8 s animation-frame callback that left the page
    // answering nothing, #5396). The cap ends that loop at once, whatever starts it.
    const maxRows = Math.ceil(window.innerHeight / 6) + 2, maxCols = Math.ceil(window.innerWidth / 3) + 2;
    let rows = Math.min(dims.rows, maxRows);
    const cols = Math.min(dims.cols, maxCols);
    // The height of one row: the renderer's own measurement (what the addon's proposal uses). Dividing the screen element's height by the row count looked the same but is not: the screen's height
    // is set by the renderer a frame after a resize, so right after one (or when the view has just been shown) it is the OLD height over the NEW row count, a cell a fraction of its size, and the
    // rows counted from it are many times too many; the next fit multiplied them again (74 x 48 -> 2,339 -> 113,987, #5396). The DOM figure is only the fallback, and only when it is a plausible cell.
    const measured = cellHeight(terminal);
    const screen = screenElement(terminal);
    const fromDom = screen && terminal.rows > 0 ? screen.getBoundingClientRect().height / terminal.rows : 0;
    const cell = measured > 0 ? measured : (fromDom >= 6 && fromDom <= 120 ? fromDom : 0);
    if (_containerEl && cell > 0) {
        const cs = getComputedStyle(_containerEl);
        const pad = (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0);
        rows = Math.min(maxRows, Math.max(1, Math.floor((parseFloat(cs.height) - pad) / cell + 0.001)));
    }
    if (cols === terminal.cols && rows === terminal.rows) return;
    clearRenderer(terminal); // as FitAddon.fit() does
    terminal.resize(cols, rows);
}

// Something on this side changed: fit and set the pane's size when this window may, otherwise
// follow the pane's. `observed` is the container's ResizeObserver, which also fires when nothing
// the user did changed its size (the tab hidden or shown): while following, only a real change of
// the container's size takes the pane's size back. The view shown, a keystroke and taking control
// always do.
function _sizeLocal(observed = false) {
    if (!terminal || !fitAddon) return;
    if (_pane && !mayResizePane()) { _followPane(); return; }
    const el = _containerEl;
    if (el && (el.offsetWidth === 0 || el.offsetHeight === 0)) return; // hidden: sized when shown
    if (_following && observed && _box() === _followBox) return;
    const wasFollowing = _following;
    _following = false;
    if (el) el.classList.remove('xterm-follow-pane');
    _setFont(_baseFont); // fitting this window measures at the Settings font
    _updateFitButton(false);
    const before = terminal.cols + 'x' + terminal.rows;
    _fit();
    // fit() reports a change through onResize, which sends it. Taking the size back at an unchanged
    // size is sent here.
    if (wasFollowing && before === terminal.cols + 'x' + terminal.rows && mayResizePane()) {
        _sendResize(terminal.cols, terminal.rows);
    }
}

// The server reported the pane's size: `seq` when it answers a resize, `initial` for its first
// report on the socket.
function _onPaneSize(cols, rows, seq, initial) {
    if (!(cols > 0 && rows > 0)) return;
    _pane = { cols, rows };
    if (!terminal) return;
    const may = mayResizePane();
    if (may && seq) {
        if (seq < _seq) return; // answers a resize this window has since replaced
        // The answer to its latest resize: this window sets the size (again), unless tmux gave it
        // another size than asked, which is then the pane's.
        if (_askedSize && cols === _askedSize.cols && rows === _askedSize.rows) {
            _stopFollowing();
            if (terminal.cols !== cols || terminal.rows !== rows) terminal.resize(cols, rows);
            return;
        }
    } else if (may && !_following) {
        if (cols === terminal.cols && rows === terminal.rows) return;
        if (initial && _seq > 0) return; // predates this window's own resize; its answer follows
    }
    _followPane();
}

function _stopFollowing() {
    _following = false;
    if (_containerEl) _containerEl.classList.remove('xterm-follow-pane');
    _applyFitWidth();
}

// Input queue: buffers keystrokes while WebSocket is disconnected
let _inputQueue = [];
const MAX_INPUT_QUEUE = 256;

function _getXtermTheme() {
    // Read xterm colors from CSS custom properties (set by theme configurator or variables.css)
    const s = getComputedStyle(document.documentElement);
    const v = (name) => s.getPropertyValue(name).trim();
    return {
        background:          v('--xterm-background')           || '#1a1a1e',
        foreground:          v('--xterm-foreground')           || '#e0e0e4',
        cursor:              v('--xterm-cursor')               || '#e0e0e4',
        selectionBackground: v('--xterm-selection-background') || '#4a4a56',
        black:               v('--xterm-black')                || '#5a5a62',
        red:                 v('--xterm-red')                  || '#e05545',
        green:               v('--xterm-green')                || '#4aba60',
        yellow:              v('--xterm-yellow')               || '#d4a030',
        blue:                v('--xterm-blue')                 || '#61afef',
        magenta:             v('--xterm-magenta')              || '#c678dd',
        cyan:                v('--xterm-cyan')                 || '#56b6c2',
        white:               v('--xterm-white')                || '#e0e0e4',
        brightBlack:         v('--xterm-bright-black')         || '#7f848e',
        brightRed:           v('--xterm-bright-red')           || '#e88070',
        brightGreen:         v('--xterm-bright-green')         || '#6ccc7c',
        brightYellow:        v('--xterm-bright-yellow')        || '#e5c07b',
        brightBlue:          v('--xterm-bright-blue')          || '#7ec0f0',
        brightMagenta:       v('--xterm-bright-magenta')       || '#d2a0ee',
        brightCyan:          v('--xterm-bright-cyan')          || '#73ccd4',
        brightWhite:         v('--xterm-bright-white')         || '#f0f0f2',
    };
}

/** Update the live terminal theme (called when user switches theme). */
export function updateTerminalTheme() {
    if (terminal) {
        terminal.options.theme = _getXtermTheme();
    }
}

// Track which session_id the terminal WS is currently connected to,
// and a generation counter to suppress stale onclose reconnects.
let _connectedSessionId = null;
let _connectedServer = 'local';
let _wsGeneration = 0;
// Reconnect pacing (#3944): a dropped terminal socket retries with the live feed's backoff and jitter, so every open
// terminal does not knock in the same second after a server restart, and a refused socket does not retry every 3 s
// forever. The count resets when a socket opens, and when a different agent's terminal starts failing.
let _termFailures = 0;
let _termFailureSession = null;
export const terminalHooks = { schedule: (fn, ms) => setTimeout(fn, ms) };
// A socket that opened and then said nothing for this long is stuck (the server sends the replay and a hello at once on every
// connect): it is closed, and the reconnect takes over (#4518).
export const SILENT_SOCKET_MS = 4000;
let _paneClosed = false;  // true when server reports pane is gone
let _needsScrollToBottom = false;  // set on connect, cleared after settling
let _bottomWhenHidden = null;      // while the container is hidden: whether it was at the bottom
let _scrollSettleTimer = null;

// A restart of the agent with this session id starts or ends (#4481). The overlay follows the agent on screen: ending a restart
// of another agent leaves it alone, and so does starting one.
export function setRestarting(value, sessionId = state.currentSession && state.currentSession.session_id) {
    markRestarting(sessionId, value);
    if (!state.currentSession || state.currentSession.session_id !== sessionId) return;
    if (value) _setSessionEndedOverlay(true);
    else hideSessionOverlay();
}

function _setSessionEndedOverlay(visible) {
    const overlay = document.getElementById("session-ended-overlay");
    if (!overlay) return;
    if (visible && !currentIsRestarting()) setTerminalState('ended');

    if (visible && !currentIsRestarting()) {
        // Before showing "Session ended", check if we can reach the server.
        // If we can't, show "Lost connection" instead of "Restart Agent".
        const forSession = state.currentSession && state.currentSession.session_id;
        const stillHere = () => !!state.currentSession && state.currentSession.session_id === forSession; // the answer of a switched-away agent shows nothing
        fetch("/api/sessions", { method: "GET", signal: AbortSignal.timeout(3000) })
            .then(() => {
                if (!stillHere()) return;
                overlay.style.display = "";
                const defaultContent = document.getElementById("session-ended-default");
                const lostConn = document.getElementById("session-lost-connection");
                const restartingContent = document.getElementById("session-restarting");
                if (restartingContent) restartingContent.style.display = "none";
                if (defaultContent) defaultContent.style.display = "";
                if (lostConn) lostConn.style.display = "none";
            })
            .catch(() => {
                if (!stillHere()) return;
                overlay.style.display = "";
                const defaultContent = document.getElementById("session-ended-default");
                const lostConn = document.getElementById("session-lost-connection");
                const restartingContent = document.getElementById("session-restarting");
                if (restartingContent) restartingContent.style.display = "none";
                if (defaultContent) defaultContent.style.display = "none";
                if (lostConn) lostConn.style.display = "";
            });
    } else if (visible) {
        showRestartingOverlay();
    } else if (currentIsRestarting()) {
        showRestartingOverlay(); // a terminal that attaches to an agent still restarting does not take its overlay away
    } else {
        overlay.style.display = "none";
    }
}

function _setDisconnectedBadge(visible) {
    if (visible) setTerminalState('reconnecting');
    const badge = document.getElementById("xterm-disconnected-badge");
    if (badge) {
        badge.style.display = visible ? "" : "none";
    }
}

/** Reuse the existing terminal if possible — just disconnect WS and clear buffer.
 *  Destroying and recreating the xterm canvas causes blank renders in macOS
 *  WebKit webview (webview_go). Only create a new Terminal when none exists. */
// Controls a paste may not carry (#3686, terminal audit finding 1): ESC and the other C0 controls
// but tab, newline and CR, DEL, and the C1 controls. A pasted ESC[201~ would end bracketed paste
// early and turn the rest into typed input; without ESC it stays text inside the paste.
const PASTE_CONTROLS = /[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]/g;

 function sanitizePaste(text) {
    return String(text == null ? '' : text).replace(PASTE_CONTROLS, '');
}

function _onPaste(e) {
    if (!terminal) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    pasteText(e.clipboardData ? e.clipboardData.getData('text/plain') : '');
}

// Paste text into the terminal, cleaned. Output can switch bracketed paste off (ESC[?2004l), and
// then each line of a multi-line paste is sent as typed and runs on its own: so that case asks
// first. The answer applies to the terminal the paste was made in.
 function pasteText(text) {
    const t = terminal;
    const clean = sanitizePaste(text);
    if (!t || !clean) return;
    const go = () => { if (terminal === t) { t.paste(clean); t.focus(); } };
    if (t.modes.bracketedPasteMode || !/[\r\n]/.test(clean)) {
        go();
        return;
    }
    const lines = clean.replace(/(\r\n|\r|\n)$/, '').split(/\r\n|\r|\n/).length;
    const confirm = window.showConfirmModal;
    if (typeof confirm !== 'function') return; // no way to ask: don't paste blind
    confirm(`Paste ${lines} line${lines === 1 ? '' : 's'}?`,
        'This terminal isn\u2019t in paste mode, so each line is sent as if typed and runs on its own.', go);
}

/** The terminal already built, for the next agent: its socket let go and a full reset queued behind what it still has to draw. */
function _reuseTerminal() {
    disconnectTerminalWs();
    terminal.write("\x1bc");
    dbg('createTerminal: reusing existing terminal, queued buffer reset');
    if (fitAddon) {
        requestAnimationFrame(() => _sizeLocal());
    }
    return terminal;
}

/** A new xterm terminal with the saved scrollback and font size, the theme and its add-ons. */
function _buildTerminal() {
    const scrollback = parseInt((state.settings || {}).terminal_scrollback, 10) || 20000;
    const fontSize = parseInt((state.settings || {}).terminal_font_size, 10) || 13;
    _baseFont = fontSize;
    terminal = new Terminal({
        cursorBlink: true,
        cursorStyle: 'block',
        disableStdin: false,
        scrollback: scrollback,
        fontSize: fontSize,
        fontFamily: "'SF Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
        minimumContrastRatio: 4.5,
        allowProposedApi: true,
        theme: _getXtermTheme(),
    });

    fitAddon = new FitAddon.FitAddon();
    terminal.loadAddon(fitAddon);

    if (typeof WebLinksAddon !== 'undefined') {
        const webLinksAddon = new WebLinksAddon.WebLinksAddon();
        terminal.loadAddon(webLinksAddon);
    }

    if (typeof UnicodeGraphemesAddon !== 'undefined') {
        terminal.loadAddon(new UnicodeGraphemesAddon.UnicodeGraphemesAddon());
        terminal.unicode.activeVersion = '15-graphemes';
    }

    _selectionDisposable = terminal.onSelectionChange(() => {
        state.isSelecting = terminal.hasSelection();
    });
}

/** What is typed goes to the socket: text batched over 12 ms, controls and escapes at once, xterm's own answers to queries dropped. */
function _wireTerminalInput() {
    // Forward all keyboard input via xterm.js onData → WebSocket → tmux.
    // Keystrokes are batched over a short window (12ms) so rapid typing
    // produces fewer WebSocket messages and tmux subprocess calls.
    let _inputBuf = "";
    let _inputTimer = null;
    const INPUT_BATCH_MS = 12;

    function _flushInput() {
        _inputTimer = null;
        const batch = _inputBuf;
        _inputBuf = "";
        if (!batch) return;

        if (terminalWs && terminalWs.readyState === WebSocket.OPEN) {
            terminalWs.send(JSON.stringify({
                type: "terminal_input",
                data: batch,
            }));
        } else {
            // Queue input for delivery when WebSocket reconnects
            if (_inputQueue.length < MAX_INPUT_QUEUE) {
                _inputQueue.push(batch);
            }
        }
    }

    // xterm answers some queries in the output itself (device attributes, cursor and status
    // reports, mode and colour queries, focus reports) and emits those answers through onData as
    // if typed (#3686, terminal audit finding 3). tmux has already answered the pane's program, so
    // they are not forwarded, and they never take the pane or resize it. xterm marks real input
    // (keys, paste, IME, mouse reports): its core service fires onUserInput just before onData for
    // those. That is xterm's internal API (the vendored 5.5.0); without it, input is forwarded as
    // before and terminal_escape_browser's query check fails, so an upgrade can't drop this quietly.
    let _userInput = false;
    const _marksInput = onUserInput(terminal, () => {
        _userInput = true;
        queueMicrotask(() => { _userInput = false; }); // onData follows in the same call
    });
    if (!_marksInput) {
        console.warn('[coral] xterm does not mark user input: its query replies reach the pane');
    }

    _onDataDisposable = terminal.onData((data) => {
        _scrolledAway = false; revealCursor(); // typing brings the prompt back into view
        if (_marksInput && !_userInput) return; // xterm's own reply to a query in the output
        _userInput = false;
        if (!state.currentSession || state.currentSession.type !== "live") return;
        // A View-only server (Cloud Hub) would drop it anyway: say so instead (#3068).
        if (hubBlocksKeystrokes()) return;
        // A keystroke in a viewer window takes interactive control (multi-window ownership).
        claimOwnership();
        // The window being typed in sets the pane's size again (#3422).
        if (_following && mayResizePane()) _sizeLocal();

        // Control chars / escape sequences flush immediately (no batching delay)
        const isControl = data.length === 1 && data.charCodeAt(0) < 32;
        const isEscSeq = data.startsWith("\x1b");
        if (isControl || isEscSeq) {
            // Flush any pending literal text first, then send the control
            if (_inputBuf) {
                clearTimeout(_inputTimer);
                _flushInput();
            }
            _inputBuf = data;
            _flushInput();
            return;
        }

        // Literal text: accumulate and debounce
        _inputBuf += data;
        if (!_inputTimer) {
            _inputTimer = setTimeout(_flushInput, INPUT_BATCH_MS);
        }
    });
}

/** Opens the terminal in the container: the fit button, the canvas renderer, ownership on click and the paste handler. */
function _openTerminal(containerEl) {
    terminal.open(containerEl);
    _containerEl = containerEl;
    // A person on the keyboard or a screen reader meets the terminal's input with Tab, which the shell keeps: the status bar's hint says how to leave it, so the input is described by it (#6188).
    if (terminal.textarea && document.getElementById('xterm-focus-hint')) terminal.textarea.setAttribute('aria-describedby', 'xterm-focus-hint');
    const fitBtn = _fitButton();
    if (fitBtn && !fitBtn.dataset.wired) {
        fitBtn.dataset.wired = '1';
        fitBtn.addEventListener('click', () => toggleTerminalFitWidth());
    }
    // xterm's default DOM renderer builds its font and size rules in <style> elements at runtime, which the Cloud
    // Hub's `style-src 'self'` policy blocks (the terminal then renders in a fallback font with wrong cell sizes).
    // The canvas renderer paints into a canvas and needs no injected style. It must load after open().
    if (typeof CanvasAddon !== 'undefined') {
        try { terminal.loadAddon(new CanvasAddon.CanvasAddon()); } catch (e) { console.warn('xterm canvas renderer unavailable, using the DOM renderer:', e.message); }
    }
    // Clicking into a viewer window's terminal takes interactive control.
    containerEl.addEventListener('mousedown', () => claimOwnership());
    // Paste goes through pasteText (#3686): taken before xterm's own handler sees it.
    containerEl.addEventListener('paste', _onPaste, true);
    dbg('terminal.open() done, container:', containerEl.offsetWidth, 'x', containerEl.offsetHeight,
        'display:', containerEl.style.display, 'cols:', terminal.cols, 'rows:', terminal.rows);
}

/** Fits once the layout has settled (the first fit right after open() reads 0 columns in a webview). */
function _fitWhenLaidOut(containerEl) {
    // Defer fit() to allow the layout engine to settle. In macOS WebKit
    // webview (used by coral-app), synchronous fit() right after open()
    // computes 0 cols/rows because the container hasn't been laid out yet.
    // Using rAF + a small fallback timeout ensures the terminal gets sized
    // correctly in both browsers and embedded webviews.
    requestAnimationFrame(() => {
        if (fitAddon) {
            _sizeLocal();
            dbg('rAF fit() done, cols:', terminal?.cols, 'rows:', terminal?.rows,
                'container:', containerEl.offsetWidth, 'x', containerEl.offsetHeight);
        }
    });
}

/** Watches the container's size: a terminal at the bottom goes back there when shown again, and the prompt stays in view. */
function _watchContainerSize(containerEl) {
    // ResizeObserver catches layout changes that rAF misses (e.g. when the
    // container transitions from display:none to flex, or sidebar resizes).
    if (typeof ResizeObserver !== 'undefined') {
        if (_resizeObserver) _resizeObserver.disconnect();
        _resizeObserver = new ResizeObserver(() => {
            const shown = containerEl.offsetWidth > 0 && containerEl.offsetHeight > 0;
            // Hiding the container (the chat view, another agent) resets its scroll position: a
            // terminal that was at the bottom goes back there when shown again, not to the top of
            // its scrollback (#3464).
            if (!shown) {
                if (terminal && _bottomWhenHidden === null) {
                    const b = terminal.buffer.active;
                    _bottomWhenHidden = b.viewportY >= b.baseY;
                }
                return;
            }
            const wasBottom = _bottomWhenHidden;
            _bottomWhenHidden = null;
            if (fitAddon && !_panelDragging) _sizeLocal(true);
            if (wasBottom && terminal) requestAnimationFrame(() => { if (terminal) terminal.scrollToBottom(); });
            requestAnimationFrame(revealCursor); // the window changed size: the prompt stays in view while following
        });
        _resizeObserver.observe(containerEl);
    }
}

/** The container scrolls while following a taller pane; the cursor line (the prompt) is kept in view. */
function _wireRevealScroll(containerEl) {
    // Following a taller pane: the container scrolls, and the cursor line (the prompt) is kept in view (revealCursor).
    _scrolledAway = false;
    if (_revealScroll) containerEl.removeEventListener('scroll', _revealScroll);
    _revealScroll = () => {
        if (_revealing || !_following) return;
        const box = _cursorBox(containerEl);
        _scrolledAway = !!box && (box.bottom < containerEl.scrollTop || box.top > containerEl.scrollTop + containerEl.clientHeight);
    };
    containerEl.addEventListener('scroll', _revealScroll, { passive: true });
    if (_cursorDisposable) _cursorDisposable.dispose();
    _cursorDisposable = typeof terminal.onCursorMove === 'function' ? terminal.onCursorMove(() => revealCursor()) : null;
}

/** The terminal's focus state, and what xterm's resize tells the pane. */
function _wireFocusAndResize(containerEl) {
    // Focus management: track terminal focus state
    // (terminal.textarea is only available after open())
    if (terminal.textarea) {
        terminal.textarea.addEventListener('focus', () => {
            containerEl.classList.add('xterm-focused');
            setTerminalFocused(true);
        });
        terminal.textarea.addEventListener('blur', () => {
            containerEl.classList.remove('xterm-focused');
            setTerminalFocused(false);
        });
    }

    // Sync tmux pane dimensions when xterm resizes (e.g. after fitAddon.fit())
    setTerminalSize(terminal.cols, terminal.rows);
    _onResizeDisposable = terminal.onResize(({ cols, rows }) => {
        setTerminalSize(cols, rows);
        // Only a window that may set the pane's size resizes it, and never while it is only
        // following the pane's own size (#3422).
        if (!_following && mayResizePane()) _sendResize(cols, rows);
    });
}

export function createTerminal(containerEl) {
    dbg('createTerminal called, existing terminal:', !!terminal);

    if (typeof Terminal === 'undefined') {
        console.warn('xterm.js not loaded, falling back to semantic renderer');
        return null;
    }

    // Reuse existing terminal. xterm processes write() calls asynchronously, so
    // clear()/reset() here can be overtaken by output that was already queued by
    // the previous session. Queue an ANSI full reset instead: it is parsed after
    // all old writes and before any replay from the new socket.
    if (terminal) {
        return _reuseTerminal();
    }

    _buildTerminal();

    _wireTerminalInput();

    // Escape hatch: Ctrl+Shift+Escape unfocuses terminal
    // (attachCustomKeyEventHandler can be called before open)
    terminal.attachCustomKeyEventHandler((ev) => {
        if (ev.type === 'keydown' && ev.key === 'Escape' && ev.ctrlKey && ev.shiftKey) {
            terminal.blur();
            return false;
        }
        return true;
    });

    _openTerminal(containerEl);

    _fitWhenLaidOut(containerEl);

    _watchContainerSize(containerEl);

    _wireRevealScroll(containerEl);

    _wireFocusAndResize(containerEl);

    return terminal;
}

export function connectTerminalWs(name, agentType, sessionId, server) {
    if (isSignedOut()) return; // the sign-in panel is up (#3939)
    // `server` routes the socket through the hub proxy for agents on a remote
    // (/api/remote/{server}/ws/terminal/...). Default: the hub itself.
    const srv = normServer(server !== undefined ? server : (state.currentSession && state.currentSession.server));
    dbg('connectTerminalWs', { name, agentType, sessionId, server: srv, currentConnected: _connectedSessionId });

    // Skip if already connected to this exact session
    if (_connectedSessionId === sessionId && _connectedServer === srv && terminalWs && terminalWs.readyState === WebSocket.OPEN) {
        dbg('connectTerminalWs: already connected, skipping');
        return;
    }

    disconnectTerminalWs();

    // Bump generation so any pending onclose from the old WS is suppressed
    const myGeneration = ++_wsGeneration;
    _connectedSessionId = sessionId;
    _connectedServer = srv;
    _paneClosed = false;
    _pane = null;       // until this server reports the pane's size (terminal_size, #3422)
    _seq = 0;
    _needsScrollToBottom = true;
    _setSessionEndedOverlay(false);
    setTerminalState('connecting');

    const params = new URLSearchParams();
    if (agentType) params.set("agent_type", agentType);
    if (sessionId) params.set("session_id", sessionId);
    const qs = params.toString() ? `?${params}` : "";

    terminalWs = new WebSocket(
        `${serverWsBase(srv)}/ws/terminal/${encodeURIComponent(name)}${qs}`
    );
    terminalWs.binaryType = 'arraybuffer';

    terminalWs.onopen = () => {
        // A socket can finish opening after the user has already selected a
        // different agent. Never resize it or flush input into that old agent.
        if (myGeneration !== _wsGeneration) return;
        dbg('terminalWs OPEN', { sessionId, url: terminalWs.url });
        _termFailures = 0;
        _setDisconnectedBadge(false);
        setTerminalState('connected');
        // The server answers a connect with the replay and a hello at once: an open socket that has sent nothing after
        // SILENT_SOCKET_MS is stuck (a hub path that dropped the stream, a stalled attach), so it is closed and the
        // reconnect (with its backoff) takes over, rather than leaving the pane blank (#4518).
        terminalHooks.schedule(() => {
            if (myGeneration === _wsGeneration && thisWs === terminalWs && _msgCount === 0 && thisWs.readyState === WebSocket.OPEN) {
                dbg('terminalWs silent after open: closing to reconnect', { sessionId });
                thisWs.close();
            }
        }, SILENT_SOCKET_MS);
        document.dispatchEvent(new CustomEvent('coral:terminal-reconnected'));
        if (terminal && !_following && mayResizePane()) _sendResize(terminal.cols, terminal.rows);
        // Flush any input queued while disconnected
        if (_inputQueue.length > 0) {
            const queued = _inputQueue.join("");
            _inputQueue = [];
            terminalWs.send(JSON.stringify({
                type: "terminal_input",
                data: queued,
            }));
        }
    };

    let _msgCount = 0;
    const thisWs = terminalWs;
    terminalWs.onmessage = (event) => {
        // close() does not guarantee that already-dispatched message events are
        // cancelled. Ignoring the old generation prevents stale terminal data
        // from being appended to the newly selected agent's xterm instance.
        if (myGeneration !== _wsGeneration) return;
        _msgCount++;

        if (event.data instanceof ArrayBuffer) {
            if (_msgCount <= 3 || _msgCount % 50 === 0) {
                dbg('terminalWs msg #' + _msgCount, { type: 'binary', hasTerminal: !!terminal,
                    byteLen: event.data.byteLength,
                    cols: terminal?.cols, rows: terminal?.rows });
            }
            if (terminal) {
                _paneClosed = false;
                markRestarting(sessionId, false);
                _setSessionEndedOverlay(false);
                // xterm parses writes later: scroll once this chunk is in the buffer (#3464).
                terminal.write(new Uint8Array(event.data), () => { if (_needsScrollToBottom && terminal) terminal.scrollToBottom(); });
                if (_needsScrollToBottom) {
                    terminal.scrollToBottom();
                    // Keep scrolling to bottom for 500ms after connect
                    // to cover replay seed + any follow-up chunks
                    clearTimeout(_scrollSettleTimer);
                    _scrollSettleTimer = setTimeout(() => {
                        _needsScrollToBottom = false;
                    }, 500);
                }
            }
            return;
        }

        // Text frames are JSON control messages; one that does not parse is ignored.
        let data;
        try { data = JSON.parse(event.data); } catch { return; }
        if (!data || typeof data !== 'object') return;
        if (_msgCount <= 3 || _msgCount % 50 === 0) {
            dbg('terminalWs msg #' + _msgCount, { type: data.type, hasTerminal: !!terminal,
                cols: terminal?.cols, rows: terminal?.rows });
        }
        if (data.type === "terminal_closed") {
            _paneClosed = true;
            _setDisconnectedBadge(false);
            _setSessionEndedOverlay(true);
            document.dispatchEvent(new CustomEvent('coral:terminal-closed', { detail: { sessionId } }));
        } else if (data.type === "terminal_size") {
            _onPaneSize(Number(data.cols), Number(data.rows), Number(data.seq) || 0, data.initial === true);
            // A size change while the replay settles relays the viewport out, which can take the DOM
            // scroll position (0 after the container was hidden) as the buffer's (#3464).
            if (_needsScrollToBottom) requestAnimationFrame(() => { if (terminal) terminal.scrollToBottom(); });
        } else if (data.type === "terminal_hello") {
            // Nothing to do: every server answers input that carries an id (#3276).
        } else if (data.type === "terminal_input_ack") {
            document.dispatchEvent(new CustomEvent('coral:terminal-input-ack', { detail: { sessionId, id: data.id } }));
        } else if (data.type === "terminal_gone") {
            // Input for this agent found no terminal (#3218): the dashboard says so, as for a 409.
            // The id, when there is one, names the refused input (#3276).
            document.dispatchEvent(new CustomEvent('coral:terminal-gone', { detail: { sessionId, id: data.id } }));
        } else if (data.type === "terminal_input_failed") {
            document.dispatchEvent(new CustomEvent('coral:terminal-input-refused', { detail: { sessionId, id: data.id, error: data.error } }));
        } else if (data.type === "hub_read_only") {
            // The Cloud Hub dropped our input for this server's access mode (#3068): a refusal
            // of that input (#3276), named by its id when the hub echoes one.
            applyHubReadOnlyFrame(data);
            // View only: render at the pane's size from now on (#3422).
            _sizeLocal();
            document.dispatchEvent(new CustomEvent('coral:terminal-input-refused', { detail: { sessionId, id: data.id } }));
        }
        // Other types are ignored.
    };

    terminalWs.onclose = (ev) => {
        dbg('terminalWs CLOSE', { code: ev.code, reason: ev.reason, sessionId, generation: myGeneration, current: _wsGeneration, paneClosed: _paneClosed });
        // Don't reconnect if the server told us the pane is gone.
        if (_paneClosed) return;

        // Only reconnect if this WS is still the current generation.
        // If disconnectTerminalWs() was called (intentional close) or
        // connectTerminalWs() was called for a different session, the
        // generation will have been bumped and we should NOT reconnect.
        if (myGeneration !== _wsGeneration) return;

        if (state.currentSession && state.currentSession.type === "live"
            && state.currentSession.session_id === sessionId) {
            // The machine refused the terminal: too many are open through Coral Hub (#3663). Say
            // so, with its reason, and wait longer before trying again.
            const busy = ev.code === TERMINAL_BUSY_CLOSE;
            if (busy) {
                showToast(ev.reason || "Too many terminals are open through Coral Hub on this computer; close one, or try again shortly.", true, 8000);
            }
            _setDisconnectedBadge(true);
            document.dispatchEvent(new CustomEvent('coral:terminal-disconnected'));
            if (isSignedOut()) return; // the sign-in panel is up (#3939): no reconnecting until the page is signed in
            if (_termFailureSession !== sessionId) { _termFailureSession = sessionId; _termFailures = 0; }
            _termFailures++;
            const delay = busy ? TERMINAL_BUSY_RETRY_MS : feedRetryDelay(_termFailures);
            terminalHooks.schedule(() => {
                // Re-check: generation still current AND session still matches
                if (myGeneration === _wsGeneration
                    && !isSignedOut()
                    && state.currentSession
                    && state.currentSession.session_id === sessionId) {
                    connectTerminalWs(
                        state.currentSession.tmux_session || state.currentSession.name,
                        state.currentSession.agent_type,
                        state.currentSession.session_id,
                        sessionServer(state.currentSession),
                    );
                }
            }, delay);
        }
    };
}

// TERMINAL_BUSY_CLOSE is the close code Coral Hub sends when the machine refused a terminal
// because too many are open through the Hub (hubproxy.StatusTooManyStreams, #3663).
 const TERMINAL_BUSY_CLOSE = 4429;
const TERMINAL_BUSY_RETRY_MS = 15000;

export function disconnectTerminalWs() {
    dbg('disconnectTerminalWs', { hadWs: !!terminalWs, connectedSession: _connectedSessionId });
    // Bump generation BEFORE closing so the old onclose handler is suppressed
    _wsGeneration++;
    _connectedSessionId = null;
    _connectedServer = 'local';
    _inputQueue = [];
    _setDisconnectedBadge(false);
    if (terminalWs) {
        terminalWs.close();
        terminalWs = null;
    }
}

// The page is signed out (#3939): close the terminal socket; its reconnect is not scheduled while the sign-in panel is up.
document.addEventListener('coral:signed-out', () => disconnectTerminalWs());

export function disposeTerminal() {
    dbg('disposeTerminal', { hadTerminal: !!terminal });
    disconnectTerminalWs();
    if (_selectionDisposable) {
        _selectionDisposable.dispose();
        _selectionDisposable = null;
    }
    if (_onDataDisposable) {
        _onDataDisposable.dispose();
        _onDataDisposable = null;
    }
    if (_onResizeDisposable) {
        _onResizeDisposable.dispose();
        _onResizeDisposable = null;
    }
    _inputQueue = [];
    _pane = null;
    _following = false;
    _seq = 0;
    if (_containerEl) _containerEl.classList.remove('xterm-follow-pane');
    if (_containerEl && _revealScroll) _containerEl.removeEventListener('scroll', _revealScroll);
    _revealScroll = null;
    if (_cursorDisposable) { _cursorDisposable.dispose(); _cursorDisposable = null; }
    _scrolledAway = false;
    _containerEl = null;
    _updateFitButton(false);
    if (_resizeObserver) {
        _resizeObserver.disconnect();
        _resizeObserver = null;
    }
    _bottomWhenHidden = null;
    if (terminal) {
        terminal.dispose();
        terminal = null;
        fitAddon = null;
    }
}

export function fitTerminal() {
    _sizeLocal();
}

/** Repaint the whole screen from the buffer: the container was hidden (Chat view) and the renderer may not have drawn since it
 *  came back, so the screen can stay blank until something is written (#4518). */
export function refreshTerminal() {
    if (terminal && terminal.rows > 0) terminal.refresh(0, terminal.rows - 1);
}

export function setPanelDragging(dragging) {
    _panelDragging = dragging;
    document.body.classList.toggle('panel-dragging', dragging);
}

// True while the terminal socket is open: the terminal is then streamed live, and the polled capture is only needed
// for the panels that ride on it (tasks, events, the Mode label).
export function isTerminalWsOpen() {
    return !!terminalWs && terminalWs.readyState === WebSocket.OPEN;
}

export function getTerminalCols() {
    return terminal ? terminal.cols : null;
}

export function getTerminal() {
    return terminal;
}

export function focusTerminal() {
    if (terminal) {
        terminal.focus();
    }
}

/** Whether the terminal socket takes input now; the server answers each frame carrying an id (#3276). */
export function terminalInputAcks() {
    return !!(terminalWs && terminalWs.readyState === WebSocket.OPEN && !_paneClosed);
}

/** Send raw terminal input data over the WebSocket (used by textarea integration). With an
 *  id, the server acknowledges or refuses it by that id (#3276). */
/** sendComposerWs sends a message from Chat's composer to an agent as one composer_send frame: the server types it
 * and submits it (waiting for a starting agent's input box, pressing Enter again while the text is still in the box)
 * and answers by id only once it was submitted (#4587). False when the socket can't take it (callers fall back to
 * POST /send, which does the same). */
export function sendComposerWs(data, id) {
    if (terminalWs && terminalWs.readyState === WebSocket.OPEN && !_paneClosed) {
        terminalWs.send(JSON.stringify({ type: "composer_send", data, id }));
        return true;
    }
    return false;
}

export function sendTerminalInputWs(data, id) {
    // After terminal_closed the server reads nothing more from this socket, though it may
    // still be open: callers fall back to HTTP, which says when the terminal is gone (#3218).
    if (terminalWs && terminalWs.readyState === WebSocket.OPEN && !_paneClosed) {
        const frame = { type: "terminal_input", data };
        if (id !== undefined) frame.id = id;
        terminalWs.send(JSON.stringify(frame));
        return true;
    }
    return false;
}

// When this window becomes the interactive owner, push its current geometry
// once so the shared pane matches the window that is actually typing.
// A viewer that loses control follows the pane's size (#3422).
document.addEventListener('coral:ownership-changed', (e) => {
    if (!terminal) return;
    if (!(e.detail && e.detail.owner)) { if (_pane) _followPane(); return; }
    _sizeLocal();
    if (terminal && !_following && mayResizePane()) _sendResize(terminal.cols, terminal.rows);
});

// The hub's or the machine's access for this agent changed (View <-> Interact).
document.addEventListener('coral:hub-access-changed', () => _sizeLocal());
