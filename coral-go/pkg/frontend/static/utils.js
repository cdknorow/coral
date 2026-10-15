import { parse, normalize, build as buildRoute } from './routes.js';
import { importOnce } from './import_once.js';
import { loadHighlight } from './vendor_loader.js';
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
    "chats-empty-view",
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
    "chats-empty-view": "flex",
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

/** A Coral artifact reference (coral://artifacts/<hex> or /api/artifacts/<hex>[?v=2]): its hex, else ''. */
export function artifactRefHex(url) {
    const m = /^(?:coral:\/\/artifacts\/|\/api\/artifacts\/)([a-f0-9]{64})(?:\?v=2)?$/i.exec(String(url || '').trim());
    return m ? m[1].toLowerCase() : '';
}

// Images in agent, board and chat content never load from elsewhere (#3515): a remote image
// in a post is a tracking or exfiltration beacon the moment the post is shown. An <img> keeps
// its source only for a Coral artifact (pointed at this server's /api/artifacts) or a data:
// or blob: image; any other (remote or relative) becomes a plain link to it. Every link gets
// rel="noopener noreferrer", and web links open in a new tab.
let _sanitizerHooks = false;
function installSanitizerHooks() {
    if (_sanitizerHooks || typeof DOMPurify === 'undefined' || typeof DOMPurify.addHook !== 'function') return;
    _sanitizerHooks = true;
    DOMPurify.addHook('afterSanitizeAttributes', (node) => {
        if (node.nodeName === 'IMG') {
            const src = node.getAttribute('src') || '';
            const hex = artifactRefHex(src);
            if (hex) {
                node.setAttribute('src', `/api/artifacts/${hex}?v=2`);
                node.setAttribute('data-artifact', hex);
            } else if (!/^(?:data:image\/|blob:)/i.test(src)) {
                node.removeAttribute('src');
                node.setAttribute('data-blocked-src', src);
            }
            node.setAttribute('referrerpolicy', 'no-referrer');
        } else if (node.nodeName === 'A' && node.hasAttribute('href')) {
            node.setAttribute('rel', 'noopener noreferrer');
            if (/^https?:/i.test(node.getAttribute('href'))) node.setAttribute('target', '_blank');
        }
    });
}
installSanitizerHooks();

/** Replace, inside the sanitized fragment, each <img> whose source was refused with a plain
 *  link to that source (text when it isn't a web address). Done on DOMPurify's own DOM, so the
 *  sanitized markup is never parsed a second time (no sanitize, reparse, reserialize). */
function unblockAsLinks(frag) {
    for (const img of frag.querySelectorAll('img[data-blocked-src]')) {
        const src = img.getAttribute('data-blocked-src') || '';
        const label = img.getAttribute('alt') || src || 'image';
        let node;
        if (/^https?:\/\//i.test(src)) {
            node = document.createElement('a');
            node.href = src;
            node.target = '_blank';
            node.rel = 'noopener noreferrer';
            node.className = 'blocked-remote-image';
            node.title = `Image not loaded: ${src}`;
            node.textContent = `🖼 ${label}`;
        } else {
            node = document.createElement('span');
            node.className = 'blocked-remote-image';
            node.textContent = `🖼 ${label}${src && label !== src ? ` (${src})` : ''}`;
        }
        img.replaceWith(node);
    }
}

/* Links in agent-written Markdown (#3540 files, #3542 board posts, chat, tasks and notes).
 * A link may never move the dashboard tab somewhere the Operator didn't choose: an agent's text
 * is steerable (prompt injection), and a same-tab link to /api/system/api-key or to a hash route
 * would act as if the Operator had gone there. One pass on DOMPurify's own DOM (no second
 * parse), by mode:
 *   'file'  (a file shown in the viewer or the pane): web links (a new tab), mailto, Coral
 *           artifact links; a #fragment stays and scrolls inside the file (keepFragmentsInside).
 *   'board' (board posts, tasks, notes): the same, plus a root-relative link the dashboard's
 *           router accepts (routes.parse), rebuilt from the parsed route (only its own query
 *           keys, no hash), so [team](/team/qa) works through the same check as a typed URL.
 *   'chat'  (live chat): as 'board', and a relative path or file: link becomes a file-reference
 *           CANDIDATE with no href (live_chat's linkifyRefs turns it into a file reference, or
 *           text), so no click, middle click or "open in new tab" can navigate to the raw path.
 * Everything else becomes text with its target shown: /api/..., ../x, a #fragment or hash
 * route on a board or in chat, file:, data:, javascript:, vbscript:, a coral: link that isn't an
 * exact artifact link, and any other scheme. */
const LINK_MODES = new Set(['file', 'board', 'chat']);

function linkAsText(a, href) {
    const span = document.createElement('span');
    span.className = 'blocked-link';
    span.title = `Link not opened: ${href}`;
    span.textContent = a.textContent === href ? href : `${a.textContent} (${href})`;
    a.replaceWith(span);
}

/** A root-relative link's dashboard route, rebuilt from the parse (null when it isn't one). */
function routeHref(href) {
    if (!href.startsWith('/') || href.startsWith('//') || href.includes('\\')) return null;
    let url;
    try { url = new URL(href, 'http://x'); } catch { return null; }
    if (url.origin !== 'http://x') return null;
    const route = normalize(parse(url.pathname, url.search));
    return route ? buildRoute(route) : null;
}

// Markers only Coral's own code may set (Security's review of #3542): live_chat's file references,
// the policy's own file-reference candidates, in-file fragments and router links. DOMPurify keeps
// data-* attributes, so text an agent wrote could carry them; they are stripped before the policy
// adds its own. data-knowledge-link too, unless the caller is the team knowledge view, which puts
// its own links into the Markdown before it is sanitized (and only acts on a team member's name).
const OWN_MARKERS = ['data-file-ref', 'data-file-ref-raw', 'data-fragment', 'data-route'];

function stripOwnMarkers(frag, keepKnowledgeLinks) {
    const names = keepKnowledgeLinks ? OWN_MARKERS : [...OWN_MARKERS, 'data-knowledge-link'];
    for (const el of frag.querySelectorAll(names.map(n => `[${n}]`).join(','))) {
        for (const n of names) el.removeAttribute(n);
    }
}

function applyLinkPolicy(frag, mode, keepKnowledgeLinks) {
    stripOwnMarkers(frag, keepKnowledgeLinks);
    for (const a of frag.querySelectorAll('a')) {
        // The team knowledge view's own links between agents (data-knowledge-link, href "#")
        // are handled by its click listener, which only opens a member's knowledge; leave them be.
        if (keepKnowledgeLinks && a.hasAttribute('data-knowledge-link') && (a.getAttribute('href') || '#') === '#') continue;
        if (!a.hasAttribute('href')) {
            // DOMPurify dropped the target (javascript:, data:, vbscript:): plain text, nothing link-like left.
            if (!a.hasAttribute('data-file-ref-raw')) { const span = document.createElement('span'); span.className = 'blocked-link'; span.textContent = a.textContent; a.replaceWith(span); }
            continue;
        }
        const href = (a.getAttribute('href') || '').trim();
        if (/^https?:\/\//i.test(href)) {
            a.target = '_blank';
            a.rel = 'noopener noreferrer';
            continue;
        }
        if (/^mailto:/i.test(href) || /^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(href)) continue;
        if (mode === 'file') {
            // A #fragment stays a link inside the shown file; keepFragmentsInside scrolls to it there.
            if (/^#[^#\s]+$/.test(href)) { a.setAttribute('data-fragment', href.slice(1)); continue; }
            linkAsText(a, href);
            continue;
        }
        const route = routeHref(href);
        if (route !== null) {
            a.setAttribute('href', route);
            a.setAttribute('data-route', '');
            a.removeAttribute('target');
            continue;
        }
        const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(href);
        if (mode === 'chat' && !href.startsWith('#') && (!scheme || scheme[1].toLowerCase() === 'file')) {
            // A file reference candidate: no href, so nothing can navigate to the raw path.
            a.removeAttribute('href');
            a.setAttribute('data-file-ref-raw', href);
            continue;
        }
        linkAsText(a, href);
    }
}

/** A #fragment link in a file shown by the strict renderer scrolls inside `root` and never
 *  changes the dashboard's URL (which the router reads, #3540). Bound once per root. */
export function keepFragmentsInside(root) {
    if (!root || root.dataset.fragmentsBound) return;
    root.dataset.fragmentsBound = '1';
    root.addEventListener('click', (e) => {
        const a = e.target.closest && e.target.closest('a[data-fragment]');
        if (!a || !root.contains(a)) return;
        e.preventDefault();
        const id = a.getAttribute('data-fragment');
        const target = [...root.querySelectorAll('[id], [name]')].find(el => el.id === id || el.getAttribute('name') === id);
        target?.scrollIntoView({ block: 'start' });
    });
}

/**
 * Sanitize untrusted HTML. Fails CLOSED: if DOMPurify did not load, the markup is shown as
 * text instead of being inserted as HTML (it used to fall back to the raw string).
 */
export function sanitizeHtml(html, config, opts) {
    if (typeof DOMPurify === 'undefined') return escapeHtml(html);
    installSanitizerHooks();
    const frag = DOMPurify.sanitize(html, { ...(config || {}), RETURN_DOM_FRAGMENT: true });
    unblockAsLinks(frag);
    // opts.links: 'file', 'board' or 'chat' (applyLinkPolicy); strictLinks is the 'file' mode (#3540).
    const linkMode = opts && (opts.links || (opts.strictLinks && 'file'));
    if (linkMode && LINK_MODES.has(linkMode)) applyLinkPolicy(frag, linkMode, !!(opts && opts.knowledgeLinks));
    const box = document.createElement('div');
    box.appendChild(frag);
    return box.innerHTML;
}

/** Markdown to sanitized HTML; text when marked or DOMPurify is missing. links: the link
 *  policy for agent-written text ('board', 'chat' or 'file'; see applyLinkPolicy). */
export function sanitizeMarkdown(md, config, links) {
    if (typeof marked === 'undefined') return escapeHtml(md);
    return sanitizeHtml(marked.parse(md), config, links ? { links } : undefined);
}

// { breaks: true } to keep single newlines as line breaks. links: the link policy for
// agent-written text, 'board' or 'chat' (applyLinkPolicy, #3542); omit it only for Coral's own text.
export function renderMarkdown(content, options, links) {
    if (!content) return '';
    if (typeof marked !== 'undefined') {
        try {
            // marked v5+ dropped the `highlight` option, so highlight fenced
            // code through a renderer. Only blocks with a known language are
            // colored; guessing (highlightAuto) is slow and often wrong.
            if (typeof hljs === 'undefined') {
                // highlight.js loads on the first fenced block with a language;
                // until then the block renders as plain code.
                if (FENCED_WITH_LANG.test(content)) requestHighlight();
            } else if (!marked._hljsConfigured) {
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
            return sanitizeHtml(html, DOMPURIFY_CONFIG, links ? { links } : undefined);
        } catch { /* fall through */ }
    }
    return escapeHtml(content);
}

// The strict renderer for a whole Markdown file an agent wrote (#3540): raw HTML in it is
// shown as text, never parsed as markup, and what marked produces then goes through the
// same sanitizer (one DOMPurify parse, the image and link hooks above). A separate marked
// instance, so the dashboard's chat and board rendering keep their own options.
let _strictMarked = null;
function strictMarked() {
    if (_strictMarked || typeof marked === 'undefined' || typeof marked.Marked !== 'function') return _strictMarked;
    _strictMarked = new marked.Marked({ gfm: true });
    _strictMarked.use({ renderer: { html({ text }) { return escapeHtml(text); } } });
    return _strictMarked;
}

/** Markdown to sanitized HTML with raw HTML escaped; text when marked or DOMPurify is missing. */
export function renderMarkdownStrict(md) {
    const m = strictMarked();
    if (!md) return '';
    if (!m) return escapeHtml(md);
    try {
        return sanitizeHtml(m.parse(String(md)), DOMPURIFY_CONFIG, { links: 'file' });
    } catch {
        return escapeHtml(md);
    }
}

// A fenced code block that names a language (```js, ~~~ python).
const FENCED_WITH_LANG = /^ {0,3}(?:```|~~~)[ \t]*[^\s`~]/m;
let _highlightLoad = null;
let _highlightRetryAt = 0;

/** Load highlight.js (once), then colour the plain blocks already on the page.
 *  A failed load leaves the code plain; a later render tries again after 30s. */
function requestHighlight() {
    if (_highlightLoad || Date.now() < _highlightRetryAt) return;
    _highlightLoad = loadHighlight().then(() => {
        highlightCodeIn(document);
        // Blocks rendered before the load and inserted just after it.
        requestAnimationFrame(() => highlightCodeIn(document));
    }, () => {
        _highlightLoad = null;
        _highlightRetryAt = Date.now() + 30000;
    });
}

/** Colour the not-yet-highlighted `pre > code.language-*` blocks under root, as
 *  renderMarkdown's renderer would have: known languages only, and the output
 *  goes through the same sanitizer. A no-op until highlight.js has loaded. */
export function highlightCodeIn(root) {
    if (typeof hljs === 'undefined') return;
    for (const code of root.querySelectorAll('pre > code[class*="language-"]:not(.hljs)')) {
        const language = (/language-([\w+#.-]+)/.exec(code.className) || [])[1];
        if (!language || !hljs.getLanguage(language)) continue;
        code.innerHTML = sanitizeHtml(hljs.highlight(code.textContent, { language, ignoreIllegals: true }).value, DOMPURIFY_CONFIG);
        code.classList.add('hljs');
    }
}

/** A duration in seconds as "45s", "12m", "1h" or "69h 59m". Minutes are
 *  rounded on the total, so they carry into the hour: 3599s is "1h", never
 *  "60m", and nothing reads "69h 60m" (#3152). Empty for none or zero. */
export function formatDurationSec(sec) {
    const n = Number(sec);
    if (!Number.isFinite(n) || n <= 0) return '';
    if (n < 60) return `${Math.floor(n)}s`;
    const mins = Math.round(n / 60);
    if (mins < 60) return `${mins}m`;
    const h = Math.floor(mins / 60);
    const m = mins % 60;
    return m > 0 ? `${h}h ${m}m` : `${h}h`;
}

/** The dashboard's board identity: the reserved Operator subscriber that task
 *  actions already use (agent_docs/board.md). Team chat posted as "dashboard"
 *  before #3171; those messages still render as the Operator's. */
export const OPERATOR_ID = 'Operator';
export function isOperatorId(id) { return id === OPERATOR_ID || id === 'dashboard'; }

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

/** A board chat message a Coral Hub user sent through the Hub (#3377). origin is set by the
 *  server on tunnel chat posts, never taken from a client and never inferred from the name. */
export function hubUserBadge(m) {
    // The Operator, acting through Coral Hub (#3404): which Hub user did it is recorded by the
    // server from the validated header (hub_user), never taken from the name.
    if (m && m.origin === 'hub_operator') {
        const who = /^hub-user-[0-9a-f]{8}$/.test(String(m.hub_user || '')) ? m.hub_user : 'unknown Hub user';
        const title = `Posted as the Operator by ${who} through Coral Hub, not by you at this computer.`;
        return `<span class="mb-external-badge mb-hub-user-badge is-operator" title="${escapeAttr(title)}">Operator via Coral Hub · ${escapeHtml(who)}</span>`;
    }
    // A Coral Hub that doesn't say which of its users sent it (an older hub): sent as the
    // reserved 'Coral Hub', origin hub_user_unknown. Never shown as a known user.
    if (m && m.origin === 'hub_user_unknown') {
        const title = 'Sent through Coral Hub, which did not say which of its users sent it. Not an agent, and not you at this computer.';
        return `<span class="mb-external-badge mb-hub-user-badge is-unknown" title="${escapeAttr(title)}">Coral Hub, user not identified</span>`;
    }
    if (!m || m.origin !== 'hub_user') return '';
    const who = String(m.subscriber_id || '');
    const title = `Sent through Coral Hub by a Hub user${who ? ` (${who})` : ''}, not by an agent or by you at this computer.`;
    return `<span class="mb-external-badge mb-hub-user-badge" title="${escapeAttr(title)}">Coral Hub user</span>`;
}

/** A board message's text for display. The server prefixes a post the Hub made as the
 *  Operator with '[Operator via Coral Hub, hub-user-…] ' so agents can tell (#3404); the
 *  dashboard shows that as a badge instead, so the prefix is dropped there. */
export function boardMessageDisplayText(m) {
    const text = String((m && m.content) || '');
    if (m && m.origin === 'hub_operator') return text.replace(/^\[Operator via Coral Hub, hub-user-[0-9a-f]{8}\] /, '');
    return text;
}

/** Markdown options for board posts: GFM, and a single newline is a line break (agents write
 *  one point per line, which plain Markdown runs together into one paragraph; #3590). */
export const BOARD_MARKDOWN = { gfm: true, breaks: true };

/** A board message's body as sanitized HTML (board link policy). */
export function renderBoardMessageBody(m) {
    return renderMarkdown(boardMessageText(boardMessageDisplayText(m)), BOARD_MARKDOWN, 'board');
}

/** A board sender's avatar (#3590): their icon (at most two characters), else up to two initials
 *  ("QA Engineer" is QE, "Orchestrator (coral-hub)" is OR), escaped as text. Coloured by the
 *  message's --mb-color. */
export function boardAvatar(m, name) {
    const icon = m && m.icon ? Array.from(String(m.icon)).slice(0, 2).join('') : '';
    const words = String(name || '?').replace(/\(.*?\)/g, ' ').trim().split(/\s+/).filter(Boolean);
    const initials = icon || (words.length > 1 ? words[0][0] + words[1][0] : (words[0] || '?').slice(0, 2)).toUpperCase();
    return `<span class="mb-avatar" aria-hidden="true">${escapeHtml(initials)}</span>`;
}

// A sender's name and avatar initials on the board in shades of the palette that meet WCAG AA
// (#3590): the palette is pastel, so the light theme takes darker shades and a few hues take
// lighter ones in the dark theme. Each holds at least 4.6:1 against the default themes' page,
// their hover highlight, and its own 18% tint (the avatar square). Same hue, HSL lightness only.
const BOARD_TEXT_SHADES = new Map([
    ['#81a1c1', { dark: '#99b3cd', light: '#344f6a' }],
    ['#a3be8c', { dark: '#a3be8c', light: '#40542f' }],
    ['#b48ead', { dark: '#c5a8c0', light: '#66435f' }],
    ['#d08770', { dark: '#dba493', light: '#7a3c28' }],
    ['#bf616a', { dark: '#d9a2a8', light: '#81343b' }],
    ['#88c0d0', { dark: '#88c0d0', light: '#265461' }],
    ['#ebcb8b', { dark: '#ebcb8b', light: '#654911' }],
    ['#8fbcbb', { dark: '#8fbcbb', light: '#325453' }],
]);

/** The board text shade of a palette colour for a theme ('dark' or 'light'). */
export function boardTextColor(color, theme) {
    const shades = BOARD_TEXT_SHADES.get(color);
    return shades ? shades[theme === 'light' ? 'light' : 'dark'] : color;
}

/** A board message's colours as CSS variables, both themes' shades; the stylesheet picks one by
 *  data-theme for the sender's name and avatar (#3590). color is a getAgentColor palette hex. */
export function boardMessageColorStyle(color) {
    const dark = boardTextColor(color, 'dark'), light = boardTextColor(color, 'light');
    return `--mb-color-dark:${dark};--mb-tint-dark:${hexToRgba(dark, 0.18)};--mb-color-light:${light};--mb-tint-light:${hexToRgba(light, 0.18)}`;
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
// A Map, not an object: a sender named 'constructor' or '__proto__' must get a palette colour,
// not Object.prototype's (#3590, Security's NIT).
const _agentColorCache = new Map();

/** The palette offered for agent name colours (same hues as avatars). */
export const AGENT_NAME_PALETTE = _agentColorPalette;

const _hexColorRe = /^#[0-9a-f]{6}$/i;

/** The user's name-colour override for a session (#rrggbb), or null for the default. */
export function agentNameColor(s) {
    return s && s.name_color && _hexColorRe.test(s.name_color) ? s.name_color : null;
}



export function getAgentColor(name) {
    if (!name) return _agentColorPalette[0];
    const known = _agentColorCache.get(name);
    if (known) return known;
    const color = _agentColorPalette[_agentColorCache.size % _agentColorPalette.length];
    _agentColorCache.set(name, color);
    return color;
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

// The rule for a NEW board or team name (#3534; the server's board.NewNamePattern): letters,
// digits, '-' and '_', starting with a letter or digit, at most 64. Existing boards keep
// any name. Returns '' when the name is fine, else a sentence saying why not.
const NEW_BOARD_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
const RESERVED_BOARD_NAMES = new Set(['projects', 'remotes', 'tasks']);
export function boardNameProblem(name) {
    const n = String(name || '');
    if (!n) return 'A board name is required.';
    if (n.length > 64) return 'A board name is at most 64 characters.';
    if (!NEW_BOARD_NAME_RE.test(n)) return "Use only letters, digits, '-' and '_' (no spaces, dots, '@' or '/'), starting with a letter or digit.";
    if (RESERVED_BOARD_NAMES.has(n.toLowerCase())) return `"${n}" is reserved; choose another name.`;
    return '';
}

/** A valid new board name made from free text ("Full Stack Team" → "full-stack-team"). */
export function boardNameSlug(text) {
    const slug = String(text || '').toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^[-_]+|[-_]+$/g, '').slice(0, 64).replace(/[-_]+$/, '');
    return slug && !RESERVED_BOARD_NAMES.has(slug) ? slug : 'team';
}
