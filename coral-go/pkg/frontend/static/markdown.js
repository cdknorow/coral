/* Markdown and HTML for the dashboard: the sanitizer, links, code highlighting and the renderers (moved out of utils.js, #5065; no behaviour change). */
import { parse, normalize, build as buildRoute } from './routes.js';
import { loadHighlight } from './vendor_loader.js';
import { escapeHtml, escapeAttr } from './html_escape.js';

// options is passed through to marked.parse for this call only, e.g.
export const DOMPURIFY_CONFIG = {
    // Agent/file/board content is attacker-influenced: never let it carry styling or form controls.
    FORBID_ATTR: ['style', 'srcset', 'formaction'],
    FORBID_TAGS: ['style', 'form', 'textarea', 'select', 'iframe', 'object', 'embed', 'link', 'meta', 'base'],
    ALLOWED_URI_REGEXP: /^(?:(?:(?:f|ht)tps?|mailto|tel|callto|sms|cid|xmpp|matrix|file|coral):|[^a-z]|[a-z+.\-]+(?:[^a-z+.\-:]|$))/i,
    ADD_ATTR: ['target', 'rel'],
};

/* Content an agent, a board post, a chat, a note or a file wrote is never live UI (#3622 F1):
 * sanitizeHtml applies these rules whatever config its caller passes. No form controls or
 * disclosure widgets (a lookalike button could act on Coral's own handlers, a fake password field
 * could collect a secret); a task-list checkbox survives as a disabled checkbox Coral makes itself.
 * Attributes come from an allowlist: no id, name, for, role, data-* or event wiring that could
 * shadow the dashboard's own elements or pick up its handlers. A class is kept only for code
 * highlighting (language-*, hljs*). Ids are kept only for a file shown in the viewer ('file'
 * links), where #fragments need them, and are prefixed there (user-content-<id>).
 * Media and embedded documents are dropped too (#3623, Security's SHOULD on #3622): video, audio,
 * source, track and picture would fetch a remote address (src, poster, srcset), and svg and math
 * are separate documents with their own links and images. The page's media-src is the second layer. */
const CONTENT_FORBID_TAGS = ['button', 'label', 'fieldset', 'legend', 'dialog', 'details', 'summary', 'progress', 'meter', 'option', 'optgroup', 'datalist', 'output', 'menu',
    'video', 'audio', 'source', 'track', 'picture', 'svg', 'math'];

const CONTENT_ATTRS = new Set(['href', 'title', 'alt', 'src', 'width', 'height', 'colspan', 'rowspan', 'align', 'start', 'reversed',
    'type', 'value', 'lang', 'dir', 'datetime', 'cite', 'abbr', 'scope', 'target', 'rel', 'referrerpolicy',
    // Set by Coral's own sanitizer hook on images (artifact and blocked-image markers).
    'data-artifact', 'data-blocked-src']);

const CODE_CLASS = /^(?:language-[\w+#.-]+|hljs(?:-[\w-]+)?)$/;

const KNOWLEDGE_CLASSES = new Set(['knowledge-link', 'knowledge-link-dead']);

const CONTENT_ID_PREFIX = 'user-content-';

function contentConfig(config, knowledgeLinks) {
    const c = { ...(config || {}) };
    c.FORBID_TAGS = [...new Set([...(c.FORBID_TAGS || []), ...DOMPURIFY_CONFIG.FORBID_TAGS, ...CONTENT_FORBID_TAGS])];
    c.FORBID_ATTR = [...new Set([...(c.FORBID_ATTR || []), ...DOMPURIFY_CONFIG.FORBID_ATTR])];
    c.ALLOW_DATA_ATTR = false;
    c.ADD_ATTR = (c.ADD_ATTR || []).filter(a => !/^data-/i.test(a) || (knowledgeLinks && a === 'data-knowledge-link'));
    if (!c.ALLOWED_URI_REGEXP) c.ALLOWED_URI_REGEXP = DOMPURIFY_CONFIG.ALLOWED_URI_REGEXP;
    return c;
}

/** The attribute allowlist and the form-control rule, on DOMPurify's own DOM (see above). */
function scrubContent(frag, linkMode, knowledgeLinks) {
    for (const el of [...frag.querySelectorAll('*')]) {
        if (el.nodeName === 'INPUT') {
            // Only a Markdown task-list checkbox survives, as a fresh disabled one.
            if ((el.getAttribute('type') || '').toLowerCase() === 'checkbox') {
                const box = document.createElement('input');
                box.setAttribute('type', 'checkbox');
                box.setAttribute('disabled', '');
                if (el.hasAttribute('checked')) box.setAttribute('checked', '');
                el.replaceWith(box);
            } else {
                el.remove();
            }
            continue;
        }
        for (const { name } of [...el.attributes]) {
            const n = name.toLowerCase();
            if (n === 'class') {
                const keep = [...el.classList].filter(t => CODE_CLASS.test(t) || (knowledgeLinks && KNOWLEDGE_CLASSES.has(t)));
                if (keep.length) el.setAttribute('class', keep.join(' ')); else el.removeAttribute('class');
            } else if (n === 'id') {
                const id = el.getAttribute('id');
                if (linkMode === 'file' && id) el.setAttribute('id', CONTENT_ID_PREFIX + id); else el.removeAttribute('id');
            } else if (!(CONTENT_ATTRS.has(n) || (knowledgeLinks && n === 'data-knowledge-link'))) {
                el.removeAttribute(name);
            }
        }
    }
}

/** A Coral artifact reference (coral://artifacts/<hex> or /api/artifacts/<hex>[?v=2]): its hex, else ''. */
export function artifactRefHex(url) {
    const m = /^(?:coral:\/\/artifacts\/|\/api\/artifacts\/)([a-f0-9]{64})(?:\?v=2)?$/i.exec(String(url || '').trim());
    return m ? m[1].toLowerCase() : '';
}

// A Docs page may show its own screenshots (#5038): ![alt](docs-img/<name>.png) names a file in static/docs-img, which renderDocsMarkdown turns into this server's own
// versioned URL for it. That exact shape (a lower-case name, .png, no query or fragment, under this server's /static) is the one image source besides an artifact, data: or blob: that
// survives; it is a file embedded in Coral itself, never one an author names.
const DOC_IMAGE = /^docs-img\/([a-z0-9][a-z0-9-]*\.png)$/;

const DOC_IMAGE_SRC = /^\/static\/(?:(?:b|bb)\/[A-Za-z0-9_-]+\/)?docs-img\/[a-z0-9][a-z0-9-]*\.png$/;

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
            } else if (!/^(?:data:image\/|blob:)/i.test(src) && !DOC_IMAGE_SRC.test(src)) {
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
export function routeHref(href) {
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
        // The file's own ids carry a prefix (#3622), so a fragment never reaches a dashboard element.
        const id = CONTENT_ID_PREFIX + a.getAttribute('data-fragment');
        const target = [...root.querySelectorAll('[id]')].find(el => el.id === id);
        target?.scrollIntoView({ block: 'start' });
    });
}

/**
 * Sanitize untrusted HTML. Fails CLOSED: if DOMPurify did not load, the markup is shown as
 * text instead of being inserted as HTML (it used to fall back to the raw string).
 */
// The marker a fenced block of a known language carries from marked to the sanitizer: unguessable per page load, so text an agent wrote cannot ask for
// highlighting by naming the class itself (it passes DOMPurify's code-class rule, which allows language-*).
const HL_NONCE = Array.from(crypto.getRandomValues(new Uint8Array(8)), b => b.toString(16).padStart(2, '0')).join(''); // from the browser's CSPRNG (Security's INFO on #4985), not Math.random

// from the browser's CSPRNG (Security's INFO on #4985), not Math.random
const HL_MARK = `--h${HL_NONCE}`;

const HL_CLASS = new RegExp(`^language-(.+)${HL_MARK}$`);

/** Colours the fenced blocks marked for it, on the sanitized fragment: highlight.js's output is escaped text and spans of its own, so it is
 *  safe to put in after DOMPurify has looked at the block's text. The result is what highlighting before sanitizing gave. */
function highlightMarked(frag) {
    if (typeof hljs === 'undefined') return;
    for (const code of frag.querySelectorAll('pre > code[class]')) {
        const m = HL_CLASS.exec(code.getAttribute('class'));
        if (!m) continue;
        const language = m[1];
        if (!hljs.getLanguage(language)) { code.className = `language-${language}`; continue; }
        code.className = `hljs language-${language}`;
        code.innerHTML = hljs.highlight(code.textContent, { language, ignoreIllegals: true }).value;
    }
}

export function sanitizeHtml(html, config, opts) {
    if (typeof DOMPurify === 'undefined') return escapeHtml(html);
    installSanitizerHooks();
    // opts.links: 'file', 'board' or 'chat' (applyLinkPolicy); strictLinks is the 'file' mode (#3540).
    const linkMode = opts && (opts.links || (opts.strictLinks && 'file'));
    const knowledgeLinks = !!(opts && opts.knowledgeLinks);
    const frag = DOMPurify.sanitize(html, { ...contentConfig(config, knowledgeLinks), RETURN_DOM_FRAGMENT: true });
    highlightMarked(frag);
    scrubContent(frag, linkMode, knowledgeLinks);
    unblockAsLinks(frag);
    if (linkMode && LINK_MODES.has(linkMode)) applyLinkPolicy(frag, linkMode, knowledgeLinks);
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

// Rendered markdown by (link policy, options, text) (#3866). The same message is rendered again on every agent switch, board panel
// redraw and chat page re-render (DOMPurify was 15% of the main thread over five agent switches in the dashboard profile). The
// result is a pure function of those three: the sanitizer reads no page state. A block with a language is not remembered until
// highlight.js has loaded, so the plain version is not kept once the highlighted one is possible.
const MD_CACHE_MAX = 400, MD_CACHE_MAX_LENGTH = 20000;

const _mdCache = new Map();

// { breaks: true } to keep single newlines as line breaks. links: the link policy for
// agent-written text, 'board' or 'chat' (applyLinkPolicy, #3542); omit it only for Coral's own text.
// A chat bubble's options (#297): single line breaks kept, as the board keeps them (board_message_text.js BOARD_MARKDOWN), so a
// message typed over several lines reads as typed.
export const CHAT_MARKDOWN = { gfm: true, breaks: true };

export function renderMarkdown(content, options, links) {
    if (!content) return '';
    if (typeof marked !== 'undefined' && typeof DOMPurify !== 'undefined' && content.length <= MD_CACHE_MAX_LENGTH
        && (typeof hljs !== 'undefined' || !FENCED_WITH_LANG.test(content))) {
        const key = (links || '') + '\u0001' + (options ? JSON.stringify(options) : '') + '\u0001' + content;
        let html = _mdCache.get(key);
        if (html === undefined) {
            html = _renderMarkdownUncached(content, options, links);
            _mdCache.set(key, html);
            if (_mdCache.size > MD_CACHE_MAX) _mdCache.delete(_mdCache.keys().next().value);
        } else {
            _mdCache.delete(key); _mdCache.set(key, html); // most recently used last
        }
        return html;
    }
    return _renderMarkdownUncached(content, options, links);
}

export function _renderMarkdownUncached(content, options, links) {
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
                            // The block goes to the sanitizer as plain text with a marker class, and is coloured once it is clean (highlightMarked in
                            // sanitizeHtml): the coloured spans made DOMPurify walk several times the nodes (#4985).
                            return `<pre><code class="language-${escapeAttr(language)}${HL_MARK}">${escapeHtml(text)}</code></pre>\n`;
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

// The Docs tab (#3622 F4): Coral's bundled docs through the same sanitizer and link policy as
// everything else. A link from one doc to another ("auth.md", "board.md#tasks") becomes that doc's
// dashboard page (/docs/<name>), which the 'board' link policy keeps as a router link. Its own
// marked instance, so chat and board rendering keep their options. null when marked is missing.
const DOC_LINK = /^([A-Za-z0-9][A-Za-z0-9_-]*)\.md(?:#\S*)?$/;

let _docsMarked = null;

export function renderDocsMarkdown(md) {
    if (!_docsMarked && typeof marked !== 'undefined' && typeof marked.Marked === 'function') {
        _docsMarked = new marked.Marked({ gfm: true });
        _docsMarked.use({
            walkTokens(token) {
                if (token.type === 'image') {
                    const img = DOC_IMAGE.exec(token.href || '');
                    if (img) token.href = new URL(`./docs-img/${img[1]}`, import.meta.url).pathname; // inherits a versioned static prefix, as cm_util.js's stylesheet does
                    return;
                }
                if (token.type !== 'link') return;
                const m = DOC_LINK.exec(token.href || '');
                if (m) token.href = `/docs/${m[1]}`;
            },
        });
    }
    if (!_docsMarked) return null;
    return sanitizeHtml(_docsMarked.parse(String(md || '')), DOMPURIFY_CONFIG, { links: 'board' });
}

// A fenced code block that names a language (```js, ~~~ python).
export const FENCED_WITH_LANG = /^ {0,3}(?:```|~~~)[ \t]*[^\s`~]/m;

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
