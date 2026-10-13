import { importOnce } from './import_once.js';
/* Utility functions: HTML escaping and toast notifications */

// Debug logging — enabled via ?debug=1 URL param or localStorage coral-debug=1
const _debugEnabled = new URLSearchParams(location.search).has('debug') ||
    localStorage.getItem('coral-debug') === '1';

export function dbg(...args) {
    if (_debugEnabled) console.log('[coral]', ...args);
}

/** Escape for HTML text AND for double- or single-quoted attribute values: & < > " ' */
export function escapeHtml(str) {
    if (str == null) return '';
    return String(str)
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#39;");
}

/** Escape for use as the value of an HTML attribute (data-*, title, value, id, ...). */
export function escapeAttr(str) {
    return escapeHtml(str);
}

/**
 * Escape for a JS string literal INSIDE an on*= attribute (the literal is single-quoted:
 * onclick="f('${jsStr(x)}')").
 *
 * The browser HTML-decodes an attribute before the JS parser sees it, so entity escaping
 * (&#39;) does NOT keep a quote inside the string: it decodes back to ' and ends the literal.
 * Every character that could end the literal, the attribute, or the element is emitted as a
 * \uXXXX escape instead; JS decodes those after HTML parsing, so the string value is exact
 * and no raw quote, ampersand or angle bracket reaches the page.
 */
export function jsStr(v) {
    return String(v == null ? '' : v).replace(/[\\'"&<>`$\u0000-\u001f\u007f\u2028\u2029]/g,
        c => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
}

/**
 * Show a short message at the bottom right. Errors are announced at once
 * (role=alert) and informational toasts politely (role=status); every toast can
 * be dismissed (close button, or Escape while focused).
 *
 * opts.blocked: prefix the text with "Blocked: " (once) so a refusal is clear
 *   without relying on colour.
 * opts.action: {label, onClick} adds a button; clicking it dismisses the toast
 *   and runs onClick. A toast with an action stays at least 10s and does not
 *   fade, so the button stays usable.
 *
 * Returns the toast element. Text is set with textContent only.
 */
export function showToast(message, isError = false, duration = 4000, opts = {}) {
    const error = !!isError;
    const toast = document.createElement("div");
    toast.className = `toast ${error ? "error" : ""}`;
    toast.setAttribute("role", error ? "alert" : "status");
    toast.setAttribute("aria-live", error ? "assertive" : "polite");
    toast.setAttribute("aria-atomic", "true");

    let text = String(message ?? "");
    if (opts.blocked && !/^Blocked:/.test(text)) text = `Blocked: ${text}`;
    const body = document.createElement("span");
    body.className = "toast-message";
    body.textContent = text;
    toast.appendChild(body);

    let timer = 0;
    const dismiss = () => {
        clearTimeout(timer);
        toast.remove();
    };
    const action = opts.action;
    if (action && action.label && typeof action.onClick === "function") {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "toast-action";
        button.textContent = action.label;
        button.addEventListener("click", () => {
            dismiss();
            try { action.onClick(); } catch (e) { console.error("toast action failed:", e); }
        });
        toast.appendChild(button);
        toast.classList.add("has-action");
        duration = Math.max(duration, 10000);
    }
    const close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "Dismiss");
    close.textContent = "×";
    close.addEventListener("click", dismiss);
    toast.appendChild(close);
    toast.addEventListener("keydown", (e) => { if (e.key === "Escape") dismiss(); });

    document.body.appendChild(toast);
    timer = setTimeout(dismiss, duration);
    return toast;
}

export function showWorkflowNotification(title, message, level = 'info', duration = 8000) {
    const icons = { success: '✅', warning: '⚠️', error: '❌', info: '💬' };
    const icon = icons[level] || icons.info;
    const toast = document.createElement("div");
    toast.className = "notification-toast";
    toast.innerHTML = `<div class="notification-toast-body">
            <div class="notification-toast-header">
                <span class="notification-toast-title">${icon} ${title ? `<strong>${escapeHtml(title)}</strong>` : 'Notification'}</span>
                <button class="notification-toast-close">✕</button>
            </div>
            ${message ? `<span class="notification-toast-detail">${escapeHtml(message)}</span>` : ''}
        </div>`;
    toast.querySelector(".notification-toast-close").addEventListener("click", () => toast.remove());
    document.body.appendChild(toast);
    setTimeout(() => toast.remove(), duration);
}

export function showAlertNotification(title, message, level = 'info', link = null) {
    const icons = { success: '✅', warning: '⚠️', error: '❌', info: '💬' };
    const icon = icons[level] || icons.info;
    const overlay = document.createElement("div");
    overlay.className = "alert-notification-overlay";
    const linkHtml = link
        ? `<a href="#" class="alert-notification-link">${escapeHtml(link.label)}</a>`
        : '';
    overlay.innerHTML = `<div class="alert-notification">
            <div class="alert-notification-icon">${icon}</div>
            <div class="alert-notification-title">${title ? escapeHtml(title) : 'Notification'}</div>
            ${message ? `<div class="alert-notification-message">${escapeHtml(message)}</div>` : ''}
            ${linkHtml}
            <button class="alert-notification-ok btn btn-primary">OK</button>
        </div>`;
    overlay.querySelector(".alert-notification-ok").addEventListener("click", () => overlay.remove());
    overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
    if (link) {
        overlay.querySelector(".alert-notification-link").addEventListener("click", (e) => {
            e.preventDefault();
            overlay.remove();
            // docs:// links navigate to the Docs tab (e.g. "docs://workflow-quickstart")
            if (link.url.startsWith('docs://')) {
                const docName = link.url.replace('docs://', '');
                if (window.switchNavTab) window.switchNavTab('docs');
                importOnce('./lazy_panels.js').then(l => l.loadDocs()).then(m => m.selectDoc(docName)).catch(() => {});
            } else if (link.url.startsWith('http')) {
                window.open(link.url, '_blank');
            } else {
                window.location.href = link.url;
            }
        });
    }
    document.body.appendChild(overlay);
}

export function showNotificationToast(agentLabel, detail, onClick) {
    const toast = document.createElement("div");
    toast.className = "notification-toast";
    const detailHtml = detail
        ? `<span class="notification-toast-detail">${detail}</span>`
        : "";
    toast.innerHTML = `<div class="notification-toast-body">
            <div class="notification-toast-header">
                <span class="notification-toast-title">⏳ <strong>${agentLabel}</strong> needs input</span>
                <button class="notification-toast-close">✕</button>
            </div>
            ${detailHtml}
            <a href="#" class="notification-toast-link">Switch to session →</a>
        </div>`;
    toast.querySelector(".notification-toast-link").addEventListener("click", (e) => {
        e.preventDefault();
        if (onClick) onClick();
        toast.remove();
    });
    toast.querySelector(".notification-toast-close").addEventListener("click", () => toast.remove());
    document.body.appendChild(toast);
    setTimeout(() => toast.remove(), 10000);
}

const VIEW_IDS = [
    "mobile-agent-list",
    "welcome-screen",
    "live-session-view",
    "history-session-view",
    "scheduler-view",
    "messageboard-view",
    "workflows-view",
    "cost-dashboard-view",
    "timeline-view",
    "kanban-view",
    "docs-view",
    "servers-view",
];

const VIEW_DISPLAY = {
    "mobile-agent-list": "flex",
    "welcome-screen": "flex",
    "live-session-view": "flex",
    "history-session-view": "flex",
    "scheduler-view": "block",
    "messageboard-view": "flex",
    "workflows-view": "block",
    "cost-dashboard-view": "flex",
    "timeline-view": "flex",
    "kanban-view": "flex",
    "docs-view": "block",
    "servers-view": "block",
};

const FULL_WIDTH_VIEWS = new Set([
    "cost-dashboard-view",
    "timeline-view",
    "kanban-view",
    "workflows-view",
    "servers-view",
]);

let _viewChanges = 0;
/** How many times showView has run: a view that loads asynchronously compares it to tell whether another view was shown meanwhile. */
export function viewChanges() { return _viewChanges; }

export function showView(activeId) {
    _viewChanges++;
    for (const id of VIEW_IDS) {
        const el = document.getElementById(id);
        if (el) el.style.display = id === activeId ? VIEW_DISPLAY[id] : "none";
    }
    const layout = document.querySelector('.layout');
    if (layout) layout.classList.toggle('sidebar-hidden', FULL_WIDTH_VIEWS.has(activeId));
    document.body.classList.toggle('live-session-active', activeId === 'live-session-view');
}

// options is passed through to marked.parse for this call only, e.g.
export const DOMPURIFY_CONFIG = {
    // Agent/file/board content is attacker-influenced: never let it carry styling or form controls.
    FORBID_ATTR: ['style', 'srcset', 'formaction'],
    FORBID_TAGS: ['style', 'form', 'textarea', 'select', 'iframe', 'object', 'embed', 'link', 'meta', 'base'],
    ALLOWED_URI_REGEXP: /^(?:(?:(?:f|ht)tps?|mailto|tel|callto|sms|cid|xmpp|matrix|file|coral):|[^a-z]|[a-z+.\-]+(?:[^a-z+.\-:]|$))/i,
    ADD_ATTR: ['target', 'rel', 'data-file-ref', 'data-action', 'data-pending-id'],
};

/**
 * Sanitize untrusted HTML. Fails CLOSED: if DOMPurify did not load, the markup is shown as
 * text instead of being inserted as HTML (it used to fall back to the raw string).
 */
export function sanitizeHtml(html, config) {
    if (typeof DOMPurify === 'undefined') return escapeHtml(html);
    return config ? DOMPurify.sanitize(html, config) : DOMPurify.sanitize(html);
}

/** Markdown to sanitized HTML; text when marked or DOMPurify is missing. */
export function sanitizeMarkdown(md, config) {
    if (typeof marked === 'undefined') return escapeHtml(md);
    return sanitizeHtml(marked.parse(md), config);
}

// { breaks: true } to keep single newlines as line breaks.
export function renderMarkdown(content, options) {
    if (!content) return '';
    if (typeof marked !== 'undefined') {
        try {
            // marked v5+ dropped the `highlight` option, so highlight fenced
            // code through a renderer. Only blocks with a known language are
            // colored; guessing (highlightAuto) is slow and often wrong.
            if (typeof hljs !== 'undefined' && !marked._hljsConfigured) {
                marked.use({
                    renderer: {
                        code({ text, lang }) {
                            const language = (lang || '').trim().split(/\s+/)[0];
                            if (!language || !hljs.getLanguage(language)) return false;
                            const html = hljs.highlight(text, { language, ignoreIllegals: true }).value;
                            return `<pre><code class="hljs language-${escapeAttr(language)}">${html}</code></pre>\n`;
                        },
                    },
                });
                marked._hljsConfigured = true;
            }
            const html = options ? marked.parse(content, options) : marked.parse(content);
            return sanitizeHtml(html, DOMPURIFY_CONFIG);
        } catch { /* fall through */ }
    }
    return escapeHtml(content);
}

/** Label fenced code blocks with their language (shown by .chat-prose CSS). */
/** Copy text to the clipboard. navigator.clipboard needs a secure context
 *  (https or localhost), so a dashboard opened over plain http from another
 *  machine falls back to a hidden textarea. Resolves to whether it worked. */
export async function copyText(text) {
    try {
        if (navigator.clipboard && window.isSecureContext) {
            await navigator.clipboard.writeText(text);
            return true;
        }
    } catch { /* fall through to the fallback */ }
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0;pointer-events:none';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch { ok = false; }
    ta.remove();
    return ok;
}

// File types supported by the raw preview endpoints (keep in sync with the
// preview type maps in routes/sessions.go).
const IMAGE_EXT_RE = /\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i;
const MEDIA_EXT_RE = /\.(mp3|wav|ogg|oga|m4a|aac|flac|mp4|webm|ogv|mov|m4v)$/i;

export function isImagePath(filepath) {
    return IMAGE_EXT_RE.test(filepath || '');
}

export function isMediaPath(filepath) {
    return MEDIA_EXT_RE.test(filepath || '');
}

/** Render images into container. `panes` is a list of { label, url, missing } (or `load` instead of `url`, an async
 *  function resolving to { src, revoke }):
 *  one pane for a preview, Before/After for a diff. A pane whose image fails
 *  to load shows its `missing` text instead (e.g. a file new since the base). */
export function renderImagePanes(container, panes) {
    container.innerHTML = '';
    const wrap = document.createElement('div');
    wrap.className = 'image-panes' + (panes.length > 1 ? ' image-panes-compare' : '');
    const frames = [];
    for (const pane of panes) {
        const fig = document.createElement('figure');
        fig.className = 'image-pane';
        if (pane.label) {
            const label = document.createElement('div');
            label.className = 'image-pane-label';
            label.textContent = pane.label;
            fig.appendChild(label);
        }
        const frame = document.createElement('div');
        frame.className = 'image-pane-frame';
        const img = document.createElement('img');
        img.alt = pane.label || '';
        img.decoding = 'async';
        const meta = document.createElement('figcaption');
        meta.className = 'image-pane-meta';
        img.addEventListener('load', () => {
            meta.textContent = `${img.naturalWidth} × ${img.naturalHeight}`;
        });
        img.addEventListener('error', () => {
            frame.innerHTML = '';
            const note = document.createElement('div');
            note.className = 'image-pane-missing';
            note.textContent = pane.missing || 'Image could not be loaded';
            frame.appendChild(note);
            meta.textContent = '';
        });
        if (pane.load) {
            // Async source (the Cloud Hub): { src, revoke } from image_source.fetchImageSrc.
            pane.load().then(({ src, revoke }) => {
                if (revoke) img.addEventListener('load', () => URL.revokeObjectURL(src), { once: true });
                img.src = src;
            }).catch(() => img.dispatchEvent(new Event('error')));
        } else {
            img.src = pane.url;
        }
        frame.appendChild(img);
        fig.append(frame, meta);
        wrap.appendChild(fig);
        frames.push({ frame, img });
    }
    container.appendChild(wrap);
    wrap.appendChild(_imageZoomControls(frames));
    _enableImagePanZoom(frames);
}

const ZOOM_MIN = 0.1;
const ZOOM_MAX = 32;

/** Wheel-zoom (toward the cursor), drag-pan and double-click-reset, shared by
 *  every frame so Before/After comparisons stay aligned. */
function _enableImagePanZoom(frames) {
    const view = { k: 1, x: 0, y: 0 };
    const listeners = new Set();
    const apply = () => {
        for (const { img } of frames) img.style.transform = `translate(${view.x}px, ${view.y}px) scale(${view.k})`;
        listeners.forEach(fn => fn(view.k));
    };
    const zoomAt = (factor, px, py) => {
        const k = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, view.k * factor));
        const r = k / view.k;
        // The image is centred in its frame, so px/py are measured from that centre.
        view.x = px - (px - view.x) * r;
        view.y = py - (py - view.y) * r;
        view.k = k;
        apply();
    };
    const reset = () => { view.k = 1; view.x = 0; view.y = 0; apply(); };
    frames._view = { zoomAt, reset, onChange: fn => listeners.add(fn), get k() { return view.k; } };

    for (const { frame, img } of frames) {
        frame.classList.add('image-pane-zoomable');
        const fromCentre = (e) => {
            const r = frame.getBoundingClientRect();
            return [e.clientX - (r.left + r.width / 2), e.clientY - (r.top + r.height / 2)];
        };
        frame.addEventListener('wheel', (e) => {
            e.preventDefault();
            const [px, py] = fromCentre(e);
            zoomAt(Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.002)), px, py);
        }, { passive: false });
        let drag = null;
        frame.addEventListener('pointerdown', (e) => {
            if (e.button !== 0) return;
            drag = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y };
            frame.setPointerCapture(e.pointerId);
            frame.classList.add('panning');
        });
        frame.addEventListener('pointermove', (e) => {
            if (!drag) return;
            view.x = drag.vx + e.clientX - drag.x;
            view.y = drag.vy + e.clientY - drag.y;
            apply();
        });
        const end = () => { drag = null; frame.classList.remove('panning'); };
        frame.addEventListener('pointerup', end);
        frame.addEventListener('pointercancel', end);
        frame.addEventListener('dblclick', reset);
        img.draggable = false;
    }
}

function _imageZoomControls(frames) {
    const bar = document.createElement('div');
    bar.className = 'image-zoom-controls';
    const mk = (text, title, fn) => {
        const b = document.createElement('button');
        b.type = 'button'; b.textContent = text; b.title = title;
        b.addEventListener('click', fn);
        return b;
    };
    const level = document.createElement('span');
    level.className = 'image-zoom-level';
    level.textContent = '100%';
    const centre = () => [0, 0];
    bar.append(
        mk('−', 'Zoom out', () => frames._view.zoomAt(1 / 1.5, ...centre())),
        level,
        mk('+', 'Zoom in', () => frames._view.zoomAt(1.5, ...centre())),
        mk('Fit', 'Reset zoom and position (or double-click the image)', () => frames._view.reset()),
    );
    // frames._view is created by _enableImagePanZoom, which runs right after this.
    queueMicrotask(() => frames._view?.onChange(k => { level.textContent = `${Math.round(k * 100)}%`; }));
    return bar;
}

/** Render playable audio/video files into container. `panes` accepts the same
 * shape as renderImagePanes so media diffs can show Before/After players. */
export function renderMediaPanes(container, panes) {
    container.innerHTML = '';
    const wrap = document.createElement('div');
    wrap.className = 'media-panes' + (panes.length > 1 ? ' media-panes-compare' : '');
    for (const pane of panes) {
        const fig = document.createElement('figure');
        fig.className = 'media-pane';
        if (pane.label) {
            const label = document.createElement('div');
            label.className = 'image-pane-label';
            label.textContent = pane.label;
            fig.appendChild(label);
        }
        const frame = document.createElement('div');
        frame.className = 'media-pane-frame';
        // The media URL is an API endpoint, so inspect its filepath query
        // parameter rather than the endpoint path itself.
        let mediaPath = pane.url;
        try { mediaPath = new URL(pane.url, window.location.href).searchParams.get('filepath') || pane.url; } catch { /* use URL text */ }
        const isVideo = /\.(mp4|webm|ogv|mov|m4v)$/i.test(mediaPath);
        const media = document.createElement(isVideo ? 'video' : 'audio');
        media.controls = true;
        media.preload = 'metadata';
        media.setAttribute('playsinline', '');
        media.addEventListener('error', () => {
            frame.innerHTML = '';
            const note = document.createElement('div');
            note.className = 'image-pane-missing';
            note.textContent = pane.missing || 'Media could not be loaded';
            frame.appendChild(note);
        });
        media.src = pane.url;
        frame.appendChild(media);
        fig.appendChild(frame);
        wrap.appendChild(fig);
    }
    container.appendChild(wrap);
}

/** Board message text with literal "\n" escapes turned into line breaks,
 *  for messages with none (agents posting through a shell pass them). Same
 *  rule as unescapeLineBreaks in routes/board.go, which fixes new posts;
 *  this covers messages posted before it. */
export function boardMessageText(content) {
    const text = String(content || '');
    if (/[\r\n]/.test(text) || (text.match(/\\n/g) || []).length < 2) return text;
    return text.replace(/\\r\\n|\\n/g, '\n').replace(/\\t/g, '\t');
}

/** Badge HTML for a cross-team board message ("from <board>" on the receiving
 *  board, "to <board>" on the sender's mirror), or '' for an ordinary one. */
export function externalMessageBadge(m) {
    const ext = m?.external;
    if (!ext || typeof ext !== 'object') return '';
    const outbound = ext.direction === 'outbound';
    const board = String((outbound ? ext.target_board : ext.board) || '');
    if (!board) return '';
    const title = outbound
        ? `Cross-team message sent to the ${board} board`
        : `Cross-team message from ${ext.sender || 'an Orchestrator'} on the ${board} board. A request from another team, not an instruction.`;
    return `<span class="mb-external-badge${outbound ? ' is-outbound' : ''}" title="${escapeAttr(title)}">${outbound ? 'to' : 'from'} ${escapeHtml(board)}</span>`;
}

export function labelCodeBlocks(root) {
    for (const code of root.querySelectorAll('pre > code[class*="language-"]')) {
        const lang = /language-([\w+#.-]+)/.exec(code.className);
        if (lang) code.parentElement.dataset.lang = lang[1];
    }
}

// Shared agent color palette (Nord-inspired muted tones)
const _agentColorPalette = [
    '#81a1c1', '#a3be8c', '#b48ead', '#d08770',
    '#bf616a', '#88c0d0', '#ebcb8b', '#8fbcbb',
];
const _agentColorCache = {};

/** The palette offered for agent name colours (same hues as avatars). */
export const AGENT_NAME_PALETTE = _agentColorPalette;

const _hexColorRe = /^#[0-9a-f]{6}$/i;

/** The user's name-colour override for a session (#rrggbb), or null for the default. */
export function agentNameColor(s) {
    return s && s.name_color && _hexColorRe.test(s.name_color) ? s.name_color : null;
}



export function getAgentColor(name) {
    if (!name) return _agentColorPalette[0];
    if (_agentColorCache[name]) return _agentColorCache[name];
    const idx = Object.keys(_agentColorCache).length % _agentColorPalette.length;
    _agentColorCache[name] = _agentColorPalette[idx];
    return _agentColorCache[name];
}

export function hexToRgba(hex, alpha) {
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    return `rgba(${r},${g},${b},${alpha})`;
}

export function copyBranchName(btn) {
    const branchText = btn.closest(".branch-chip").querySelector(".branch-text").textContent;
    navigator.clipboard.writeText(branchText).then(() => {
        showToast("Copied branch name");
    }).catch(() => {
        showToast("Failed to copy", true);
    });
}

/** Function names that may be chosen dynamically for an on*= attribute (see tools/gen_handler_shapes.mjs). */
export const HANDLER_NAMES = ['toggleEventFilter', 'toggleHistoryEventFilter', 'toggleAllEventFilters', 'toggleAllHistoryEventFilters',
    'window._selectAgentPreset', 'window._applyACFPresetValue'];

/** A function name for an on*= attribute; only names from HANDLER_NAMES are accepted. */
export function jsIdent(name) {
    const n = String(name == null ? '' : name);
    if (!HANDLER_NAMES.includes(n)) throw new Error('unsafe handler name');
    return n;
}
