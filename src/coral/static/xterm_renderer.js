/* xterm.js terminal renderer — streams raw ANSI output via WebSocket */

import { state } from './state.js';
import { sendRawKeys } from './controls.js';

let terminal = null;
let fitAddon = null;
let terminalWs = null;
let _selectionDisposable = null;
let _onDataDisposable = null;
let _terminalFocused = false;

function _getXtermTheme() {
    // Read xterm colors from CSS custom properties (set by theme configurator or variables.css)
    const s = getComputedStyle(document.documentElement);
    const v = (name) => s.getPropertyValue(name).trim();
    return {
        background:          v('--xterm-background')           || '#0d1117',
        foreground:          v('--xterm-foreground')           || '#e6edf3',
        cursor:              v('--xterm-cursor')               || '#e6edf3',
        selectionBackground: v('--xterm-selection-background') || '#264f78',
        black:               v('--xterm-black')                || '#484f58',
        red:                 v('--xterm-red')                  || '#f85149',
        green:               v('--xterm-green')                || '#3fb950',
        yellow:              v('--xterm-yellow')               || '#d29922',
        blue:                v('--xterm-blue')                 || '#58a6ff',
        magenta:             v('--xterm-magenta')              || '#bc8cff',
        cyan:                v('--xterm-cyan')                 || '#39d2c0',
        white:               v('--xterm-white')                || '#e6edf3',
        brightBlack:         v('--xterm-bright-black')         || '#6e7681',
        brightRed:           v('--xterm-bright-red')           || '#ffa198',
        brightGreen:         v('--xterm-bright-green')         || '#56d364',
        brightYellow:        v('--xterm-bright-yellow')        || '#e3b341',
        brightBlue:          v('--xterm-bright-blue')          || '#79c0ff',
        brightMagenta:       v('--xterm-bright-magenta')       || '#d2a8ff',
        brightCyan:          v('--xterm-bright-cyan')          || '#56d4dd',
        brightWhite:         v('--xterm-bright-white')         || '#f0f6fc',
    };
}

/** Update the live terminal theme (called when user switches theme). */
export function updateTerminalTheme() {
    if (terminal) {
        terminal.options.theme = _getXtermTheme();
    }
}

// Buffered content for when updates are paused due to text selection or scroll
let _pendingContent = null;
let _xtermSelecting = false;
let _userScrolledUp = false;

// Track which session_id the terminal WS is currently connected to,
// and a generation counter to suppress stale onclose reconnects.
let _connectedSessionId = null;
let _wsGeneration = 0;

function _setPauseBadge(visible) {
    const badge = document.getElementById("selection-pause-badge");
    if (badge) {
        badge.style.display = visible ? "" : "none";
        // Allow clicking the badge to resume
        badge.onclick = () => resumeScroll();
    }
}

/** Resume auto-scroll and flush any buffered content. */
export function resumeScroll() {
    _userScrolledUp = false;
    state.autoScroll = true;
    _setPauseBadge(false);
    _flushPending();
}

/** Write buffered content to the terminal (called when selection/scroll clears). */
function _flushPending() {
    if (_pendingContent !== null && terminal) {
        const converted = _pendingContent.replace(/\n/g, '\r\n');
        terminal.write('\x1b[2J\x1b[H' + converted);
        terminal.scrollToBottom();
        _pendingContent = null;
    }
}

export function createTerminal(containerEl) {
    if (terminal) {
        disposeTerminal();
    }

    if (typeof Terminal === 'undefined') {
        console.warn('xterm.js not loaded, falling back to semantic renderer');
        return null;
    }

    const scrollback = parseInt((state.settings || {}).terminal_scrollback, 10) || 1000;
    terminal = new Terminal({
        cursorBlink: true,
        cursorStyle: 'block',
        disableStdin: false,
        scrollback: scrollback,
        fontSize: 13,
        fontFamily: "'SF Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
        theme: _getXtermTheme(),
    });

    fitAddon = new FitAddon.FitAddon();
    terminal.loadAddon(fitAddon);

    if (typeof WebLinksAddon !== 'undefined') {
        const webLinksAddon = new WebLinksAddon.WebLinksAddon();
        terminal.loadAddon(webLinksAddon);
    }

    // Track selection state: pause updates while user has text selected
    _selectionDisposable = terminal.onSelectionChange(() => {
        const hasSelection = terminal.hasSelection();
        _xtermSelecting = hasSelection;
        state.isSelecting = hasSelection;
        _setPauseBadge(hasSelection);
        if (!hasSelection) {
            _flushPending();
        }
    });

    // Track user scroll: mouse wheel up pauses updates, scroll down resumes.
    const xtermEl = containerEl;
    let _scrollUpCount = 0;
    xtermEl.addEventListener("wheel", (e) => {
        if (e.deltaY < 0) {
            // Scrolling up — pause after a couple of ticks to avoid accidental triggers
            _scrollUpCount++;
            if (_scrollUpCount >= 2 && !_userScrolledUp) {
                _userScrolledUp = true;
                state.autoScroll = false;
                _setPauseBadge(true);
            }
        } else if (e.deltaY > 0 && _userScrolledUp && terminal) {
            // Scrolling down — only resume when user reaches the bottom of the buffer
            _scrollUpCount = 0;
            const viewport = terminal.buffer.active;
            const atBottom = viewport.baseY <= terminal.buffer.active.viewportY;
            if (atBottom) {
                resumeScroll();
            }
        }
    }, { passive: true });

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
            // Fallback: map common sequences to raw key names for POST
            const _fallbackMap = {
                "\r": ["Enter"],
                "\x1b": ["Escape"],
                "\x1b[A": ["Up"],
                "\x1b[B": ["Down"],
                "\x1b[C": ["Right"],
                "\x1b[D": ["Left"],
            };
            const mapped = _fallbackMap[batch];
            if (mapped) {
                sendRawKeys(mapped);
            }
        }
    }

    _onDataDisposable = terminal.onData((data) => {
        if (!state.currentSession || state.currentSession.type !== "live") return;

        // Clear selection state on any keypress
        if (_xtermSelecting) {
            terminal.clearSelection();
            _xtermSelecting = false;
            state.isSelecting = false;
            _setPauseBadge(false);
            _flushPending();
        }

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
    fitAddon.fit();

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

    return terminal;
}

export function connectTerminalWs(name, agentType, sessionId) {
    // Always reset scroll-pause state when switching to a session so the
    // terminal renders immediately instead of buffering into the void.
    resumeScroll();

    // Skip if already connected to this exact session
    if (_connectedSessionId === sessionId && terminalWs && terminalWs.readyState === WebSocket.OPEN) {
        return;
    }

    disconnectTerminalWs();

    // Bump generation so any pending onclose from the old WS is suppressed
    const myGeneration = ++_wsGeneration;
    _connectedSessionId = sessionId;

    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const params = new URLSearchParams();
    if (agentType) params.set("agent_type", agentType);
    if (sessionId) params.set("session_id", sessionId);
    const qs = params.toString() ? `?${params}` : "";

    terminalWs = new WebSocket(
        `${proto}//${location.host}/ws/terminal/${encodeURIComponent(name)}${qs}`
    );

    terminalWs.onmessage = (event) => {
        const data = JSON.parse(event.data);
        if (data.type === "terminal_update" && terminal) {
            // Buffer the update if user has text selected or scrolled up
            if (_xtermSelecting || _userScrolledUp) {
                _pendingContent = data.content;
                return;
            }
            // Overwrite in-place: cursor home + content + clear remainder.
            // This avoids the flicker caused by reset() which visibly
            // clears the screen before the new content is drawn.
            const converted = data.content.replace(/\n/g, '\r\n');
            terminal.write('\x1b[2J\x1b[H' + converted);
            terminal.scrollToBottom();
        }
    };

    terminalWs.onclose = () => {
        // Only reconnect if this WS is still the current generation.
        // If disconnectTerminalWs() was called (intentional close) or
        // connectTerminalWs() was called for a different session, the
        // generation will have been bumped and we should NOT reconnect.
        if (myGeneration !== _wsGeneration) return;

        if (state.currentSession && state.currentSession.type === "live"
            && state.currentSession.session_id === sessionId) {
            setTimeout(() => {
                // Re-check: generation still current AND session still matches
                if (myGeneration === _wsGeneration
                    && state.currentSession
                    && state.currentSession.session_id === sessionId) {
                    connectTerminalWs(
                        state.currentSession.name,
                        state.currentSession.agent_type,
                        state.currentSession.session_id,
                    );
                }
            }, 3000);
        }
    };
}

export function disconnectTerminalWs() {
    // Bump generation BEFORE closing so the old onclose handler is suppressed
    _wsGeneration++;
    _connectedSessionId = null;
    if (terminalWs) {
        terminalWs.close();
        terminalWs = null;
    }
}

export function disposeTerminal() {
    disconnectTerminalWs();
    if (_selectionDisposable) {
        _selectionDisposable.dispose();
        _selectionDisposable = null;
    }
    if (_onDataDisposable) {
        _onDataDisposable.dispose();
        _onDataDisposable = null;
    }
    _xtermSelecting = false;
    _terminalFocused = false;
    _pendingContent = null;
    if (terminal) {
        terminal.dispose();
        terminal = null;
        fitAddon = null;
    }
}

export function fitTerminal() {
    if (fitAddon) {
        fitAddon.fit();
    }
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

/** Send raw terminal input data over the WebSocket (used by textarea integration). */
export function sendTerminalInputWs(data) {
    if (terminalWs && terminalWs.readyState === WebSocket.OPEN) {
        terminalWs.send(JSON.stringify({
            type: "terminal_input",
            data: data,
        }));
        return true;
    }
    return false;
}
