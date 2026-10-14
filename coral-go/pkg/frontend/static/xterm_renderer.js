/* xterm.js terminal renderer — streams raw ANSI output via WebSocket */

import { state } from './state.js';
import { isInteractiveOwner, claimOwnership, ownershipSessionId } from './ownership.js';
import { dbg } from './utils.js';
import { normServer, serverWsBase, sessionServer } from './server_base.js';
import { hubBlocksKeystrokes, applyHubReadOnlyFrame } from './hub_access.js';

let terminal = null;
let fitAddon = null;
let terminalWs = null;
let _selectionDisposable = null;
let _onDataDisposable = null;
let _onResizeDisposable = null;
let _resizeObserver = null;
let _terminalFocused = false;
let _panelDragging = false;

// ── The pane's one size (#3422) ──────────────────────────────────────────
// The tmux pane has one size, which the server reports on the socket (terminal_size). Only a
// window that may set it (the interactive owner, with Interact, wider than a phone) fits its
// container and resizes the pane. Every other window (a viewer tab, View mode through the hub,
// a phone) renders the xterm at the pane's own size and scrolls, instead of wrapping lines drawn
// for a wider pane at its own width. A window that sets the size follows a change made elsewhere
// (another browser or device) instead of fighting it, until it is resized or typed in again.
// An older server sends no size: then every window fits its container, as before.
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

// Render at the pane's size (horizontal scroll when it is wider than the window).
function _followPane() {
    if (!terminal || !_pane) return;
    if (!_following) _followBox = _box();
    _following = true;
    if (_containerEl) _containerEl.classList.add('xterm-follow-pane');
    if (terminal.cols !== _pane.cols || terminal.rows !== _pane.rows) terminal.resize(_pane.cols, _pane.rows);
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
    const before = terminal.cols + 'x' + terminal.rows;
    fitAddon.fit();
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
let _paneClosed = false;  // true when server reports pane is gone
let _inputAcks = false;   // the server answers input carrying an id (terminal_hello, #3276)
let _restarting = false;  // true when a restart is in progress
let _needsScrollToBottom = false;  // set on connect, cleared after settling
let _scrollSettleTimer = null;

export function setRestarting(value) {
    _restarting = value;
    if (value) {
        // Show restarting overlay immediately
        _setSessionEndedOverlay(true);
    }
}

function _setSessionEndedOverlay(visible) {
    const overlay = document.getElementById("session-ended-overlay");
    if (!overlay) return;

    if (visible && !_restarting) {
        // Before showing "Session ended", check if we can reach the server.
        // If we can't, show "Lost connection" instead of "Restart Agent".
        fetch("/api/sessions", { method: "GET", signal: AbortSignal.timeout(3000) })
            .then(() => {
                overlay.style.display = "";
                const defaultContent = document.getElementById("session-ended-default");
                const lostConn = document.getElementById("session-lost-connection");
                if (defaultContent) defaultContent.style.display = "";
                if (lostConn) lostConn.style.display = "none";
            })
            .catch(() => {
                overlay.style.display = "";
                const defaultContent = document.getElementById("session-ended-default");
                const lostConn = document.getElementById("session-lost-connection");
                if (defaultContent) defaultContent.style.display = "none";
                if (lostConn) lostConn.style.display = "";
            });
    } else if (visible && _restarting) {
        overlay.style.display = "";
        const defaultContent = document.getElementById("session-ended-default");
        const restartingContent = document.getElementById("session-restarting");
        const lostConn = document.getElementById("session-lost-connection");
        if (defaultContent) defaultContent.style.display = "none";
        if (restartingContent) restartingContent.style.display = "";
        if (lostConn) lostConn.style.display = "none";
    } else {
        overlay.style.display = "none";
    }
}

function _setDisconnectedBadge(visible) {
    const badge = document.getElementById("xterm-disconnected-badge");
    if (badge) {
        badge.style.display = visible ? "" : "none";
    }
}

/** Reuse the existing terminal if possible — just disconnect WS and clear buffer.
 *  Destroying and recreating the xterm canvas causes blank renders in macOS
 *  WebKit webview (webview_go). Only create a new Terminal when none exists. */
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
        disconnectTerminalWs();
        terminal.write("\x1bc");
        dbg('createTerminal: reusing existing terminal, queued buffer reset');
        if (fitAddon) {
            requestAnimationFrame(() => _sizeLocal());
        }
        return terminal;
    }

    const scrollback = parseInt((state.settings || {}).terminal_scrollback, 10) || 20000;
    const fontSize = parseInt((state.settings || {}).terminal_font_size, 10) || 13;
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

    _onDataDisposable = terminal.onData((data) => {
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

    // Escape hatch: Ctrl+Shift+Escape unfocuses terminal
    // (attachCustomKeyEventHandler can be called before open)
    terminal.attachCustomKeyEventHandler((ev) => {
        if (ev.type === 'keydown' && ev.key === 'Escape' && ev.ctrlKey && ev.shiftKey) {
            terminal.blur();
            return false;
        }
        return true;
    });

    terminal.open(containerEl);
    _containerEl = containerEl;
    // xterm's default DOM renderer builds its font and size rules in <style> elements at runtime, which the Cloud
    // Hub's `style-src 'self'` policy blocks (the terminal then renders in a fallback font with wrong cell sizes).
    // The canvas renderer paints into a canvas and needs no injected style. It must load after open().
    if (typeof CanvasAddon !== 'undefined') {
        try { terminal.loadAddon(new CanvasAddon.CanvasAddon()); } catch (e) { console.warn('xterm canvas renderer unavailable, using the DOM renderer:', e.message); }
    }
    // Clicking into a viewer window's terminal takes interactive control.
    containerEl.addEventListener('mousedown', () => claimOwnership());
    dbg('terminal.open() done, container:', containerEl.offsetWidth, 'x', containerEl.offsetHeight,
        'display:', containerEl.style.display, 'cols:', terminal.cols, 'rows:', terminal.rows);

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

    // ResizeObserver catches layout changes that rAF misses (e.g. when the
    // container transitions from display:none to flex, or sidebar resizes).
    if (typeof ResizeObserver !== 'undefined') {
        if (_resizeObserver) _resizeObserver.disconnect();
        _resizeObserver = new ResizeObserver(() => {
            if (fitAddon && !_panelDragging && containerEl.offsetWidth > 0 && containerEl.offsetHeight > 0) {
                _sizeLocal(true);
            }
        });
        _resizeObserver.observe(containerEl);
    }

    // Focus management: track terminal focus state
    // (terminal.textarea is only available after open())
    if (terminal.textarea) {
        terminal.textarea.addEventListener('focus', () => {
            _terminalFocused = true;
            containerEl.classList.add('xterm-focused');
        });
        terminal.textarea.addEventListener('blur', () => {
            _terminalFocused = false;
            containerEl.classList.remove('xterm-focused');
        });
    }

    // Sync tmux pane dimensions when xterm resizes (e.g. after fitAddon.fit())
    _onResizeDisposable = terminal.onResize(({ cols, rows }) => {
        // Only a window that may set the pane's size resizes it, and never while it is only
        // following the pane's own size (#3422).
        if (!_following && mayResizePane()) _sendResize(cols, rows);
    });

    return terminal;
}

export function connectTerminalWs(name, agentType, sessionId, server) {
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
    _inputAcks = false; // until this server says it answers input (terminal_hello)
    _pane = null;       // until this server reports the pane's size (terminal_size, #3422)
    _seq = 0;
    _needsScrollToBottom = true;
    _setSessionEndedOverlay(false);

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
        _setDisconnectedBadge(false);
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
                _restarting = false;
                _setSessionEndedOverlay(false);
                terminal.write(new Uint8Array(event.data));
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
        } else if (data.type === "terminal_hello") {
            // The server answers input that carries an id (#3276).
            _inputAcks = data.input_acks === true;
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
            _setDisconnectedBadge(true);
            document.dispatchEvent(new CustomEvent('coral:terminal-disconnected'));
            setTimeout(() => {
                // Re-check: generation still current AND session still matches
                if (myGeneration === _wsGeneration
                    && state.currentSession
                    && state.currentSession.session_id === sessionId) {
                    connectTerminalWs(
                        state.currentSession.tmux_session || state.currentSession.name,
                        state.currentSession.agent_type,
                        state.currentSession.session_id,
                        sessionServer(state.currentSession),
                    );
                }
            }, 3000);
        }
    };
}

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
    _terminalFocused = false;
    _inputQueue = [];
    _pane = null;
    _following = false;
    _seq = 0;
    if (_containerEl) _containerEl.classList.remove('xterm-follow-pane');
    _containerEl = null;
    if (_resizeObserver) {
        _resizeObserver.disconnect();
        _resizeObserver = null;
    }
    if (terminal) {
        terminal.dispose();
        terminal = null;
        fitAddon = null;
    }
}

export function fitTerminal() {
    _sizeLocal();
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

export function isTerminalFocused() {
    return _terminalFocused;
}

export function focusTerminal() {
    if (terminal) {
        terminal.focus();
    }
}

/** Whether the open terminal socket's server answers input carrying an id (#3276). */
export function terminalInputAcks() {
    return !!(terminalWs && terminalWs.readyState === WebSocket.OPEN && !_paneClosed && _inputAcks);
}

/** Send raw terminal input data over the WebSocket (used by textarea integration). With an
 *  id, a server that answers input acknowledges or refuses it by that id (#3276). */
export function sendTerminalInputWs(data, id) {
    // After terminal_closed the server reads nothing more from this socket, though it may
    // still be open: callers fall back to HTTP, which says when the terminal is gone (#3218).
    if (terminalWs && terminalWs.readyState === WebSocket.OPEN && !_paneClosed) {
        const frame = { type: "terminal_input", data };
        if (id !== undefined && _inputAcks) frame.id = id;
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
