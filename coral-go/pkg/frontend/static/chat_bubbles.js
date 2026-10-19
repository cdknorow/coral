/* The chat's bubble builder and file-reference links (#3837). Moved out of live_chat.js as it was; no behaviour change.
 * makeBubble(className, html) is how every feature builds a row: it returns a div of that class with the html in it, code blocks
 * labelled and copy buttons and file references wired. Pair it with appendContent (chat_content.js) to put the row in the chat. */
import { escapeHtml, labelCodeBlocks, showToast, copyText } from './utils.js';
import { serverFetch, sessionServer, serverUrl, currentServer } from './server_base.js';
import { state } from './state.js';

export function makeBubble(className, html) {
    const div = document.createElement("div");
    div.className = className;
    div.innerHTML = html;
    labelCodeBlocks(div);
    addCodeCopyButtons(div);
    linkifyRefs(div);
    return div;
}

// Each code block gets its own copy button. The block scrolls sideways, so
// the button sits on a wrapper around it rather than inside it.
function addCodeCopyButtons(root) {
    for (const pre of root.querySelectorAll(".message-text pre")) {
        if (pre.parentElement.classList.contains("code-block")) continue;
        const wrap = document.createElement("div");
        wrap.className = "code-block";
        pre.replaceWith(wrap);
        wrap.appendChild(pre);
        wrap.insertAdjacentHTML("beforeend", `<button type="button" class="code-copy-btn" title="Copy code" aria-label="Copy code"><span class="material-icons" aria-hidden="true">content_copy</span></button>`);
    }
}

// Swap a copy button's icon to a check for a moment after copying.
export function flashCopied(btn) {
    const icon = btn.querySelector(".material-icons");
    icon.textContent = "check";
    btn.classList.add("copied");
    clearTimeout(btn._copiedTimer);
    btn._copiedTimer = setTimeout(() => {
        icon.textContent = "content_copy";
        btn.classList.remove("copied");
    }, 1500);
}

document.addEventListener("click", async (e) => {
    const btn = e.target.closest && e.target.closest(".code-copy-btn");
    if (!btn) return;
    const code = btn.parentElement.querySelector("pre");
    if (!(await copyText(code ? code.textContent.replace(/\n$/, "") : ""))) {
        showToast("Could not copy", true);
        return;
    }
    flashCopied(btn);
});

// A path-looking inline code span: has a directory, a :line suffix, or a
// common source extension. Keeps `window.fetch` or `v1.2.3` from matching.
const FILE_REF_RE = /^(?:~?\.{0,2}\/)?(?:[\w@.+-]+\/)*[\w@.+-]+\.[A-Za-z][\w]{0,7}(?::\d+(?:[-:]\d+)?)?$/;
const FILE_REF_EXTS = new Set(["md", "js", "mjs", "cjs", "ts", "tsx", "jsx", "go", "py", "rb", "rs", "java", "kt", "swift", "c", "h", "cc", "cpp", "hpp", "cs", "php", "sh", "zsh", "bash", "json", "yaml", "yml", "toml", "ini", "css", "scss", "html", "sql", "txt", "mod", "sum", "lock", "xml", "svg", "vue", "svelte"]);

function normalizeFileRef(raw) {
    if (!raw) return "";
    let s = String(raw).trim();
    if (/^file:\/\//i.test(s)) {
        s = s.replace(/^file:\/\/(?:localhost)?/i, "");
        if (!s.startsWith("/")) s = "/" + s;
    }
    // Convert markdown hash line anchor #L123, #L123-L145, #123 to :123
    s = s.replace(/#(?:L)?(\d+)(?:[-:]L?\d+)?$/i, ":$1");
    // Strip any remaining hash
    s = s.replace(/#.*$/, "");
    return s;
}

function looksLikeFileRef(text) {
    const s = normalizeFileRef(text);
    if (!s || !FILE_REF_RE.test(s) || /^https?:/i.test(s)) return false;
    const path = s.replace(/:\d+(?:[-:]\d+)?$/, "");
    const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
    return path.includes("/") || path !== s || FILE_REF_EXTS.has(ext);
}

// A path in plain prose (not in backticks). Needs a directory so words like
// "e.g." or "README.md" in a sentence stay text; the lookbehind keeps it
// from matching inside a URL or a longer token.
const BARE_FILE_REF_RE = /(?<![\w\/:.@~-])(?:~?\.{0,2}\/)?(?:[\w@.+-]+\/)+[\w@.+-]*\w\.[A-Za-z]\w{0,7}(?::\d+(?:[-:]\d+)?)?(?![\w\/])/g;

// Web links open in a new tab; links to relative paths, path-looking inline
// code and bare paths in prose become file references (in the live Chat
// view, clicking one offers to open it in the Files panel).
export function linkifyRefs(root) {
    // A relative link in chat Markdown arrives with no href (utils.applyLinkPolicy, #3542): it
    // becomes a file reference when it looks like one, else plain text. It never navigates.
    for (const a of root.querySelectorAll("a[data-file-ref-raw]")) {
        const raw0 = a.getAttribute("data-file-ref-raw");
        a.removeAttribute("data-file-ref-raw");
        let raw = raw0;
        try { raw = decodeURIComponent(raw0); } catch {}
        if (/^file:/i.test(raw) || looksLikeFileRef(raw)) {
            a.dataset.fileRef = normalizeFileRef(raw);
            a.classList.add("chat-file-ref");
            a.setAttribute("role", "link");
            a.tabIndex = 0;
        } else {
            const span = document.createElement("span");
            span.className = "blocked-link";
            span.title = `Link not opened: ${raw0}`;
            span.textContent = a.textContent === raw0 ? raw0 : `${a.textContent} (${raw0})`;
            a.replaceWith(span);
        }
    }
    for (const a of root.querySelectorAll("a[href]")) {
        if (a.hasAttribute("data-route")) continue; // a dashboard page link the router accepted
        const href = a.getAttribute("href");
        const artifactHref = /^\/api\/artifacts\/([a-f0-9]{64})$/i.exec(href || '');
        if (/^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(href || '') || artifactHref) {
            a.dataset.fileRef = artifactHref ? `coral://artifacts/${artifactHref[1]}` : href;
            a.classList.add("chat-file-ref");
            continue;
        }
        if (/^(https?:|mailto:)/i.test(href)) {
            a.target = "_blank";
            a.rel = "noopener noreferrer";
        } else if (!href.startsWith("#")) {
            let raw = href;
            try { raw = decodeURIComponent(href); } catch {}
            if (/^file:/i.test(raw) || looksLikeFileRef(raw)) {
                a.dataset.fileRef = normalizeFileRef(raw);
                a.classList.add("chat-file-ref");
            }
        }
    }
    for (const code of root.querySelectorAll(":not(pre) > code")) {
        if (code.closest("a")) continue;
        const text = code.textContent.trim();
        if (/^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(text)) {
            code.dataset.fileRef = text;
            code.classList.add("chat-file-ref");
        } else if (looksLikeFileRef(text)) {
            code.dataset.fileRef = normalizeFileRef(text);
            code.classList.add("chat-file-ref");
        }
    }
    // Artifact URIs are often returned in fenced code blocks so they can be
    // copied reliably. Keep those exact values actionable as well.
    for (const code of root.querySelectorAll("pre code")) {
        const text = code.textContent.trim();
        if (/^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(text)) {
            code.dataset.fileRef = text;
            code.classList.add("chat-file-ref");
        }
    }
    // Plain-text artifact URIs (for example, a final response without
    // Markdown link syntax) should use the same interaction menu.
    const artifactWalker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
        acceptNode: n => n.parentElement.closest('a, code, pre, [data-file-ref]')
            ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
    });
    const artifactNodes = [];
    while (artifactWalker.nextNode()) {
        if (/coral:\/\/artifacts\/[a-f0-9]{64}/i.test(artifactWalker.currentNode.nodeValue)) artifactNodes.push(artifactWalker.currentNode);
    }
    for (const node of artifactNodes) {
        const frag = document.createDocumentFragment();
        let last = 0;
        for (const match of node.nodeValue.matchAll(/coral:\/\/artifacts\/[a-f0-9]{64}/gi)) {
            frag.append(node.nodeValue.slice(last, match.index));
            const span = document.createElement('span');
            span.className = 'chat-file-ref chat-file-ref-bare';
            span.dataset.fileRef = match[0];
            span.textContent = match[0];
            frag.append(span);
            last = match.index + match[0].length;
        }
        frag.append(node.nodeValue.slice(last));
        node.replaceWith(frag);
    }
    linkifyBarePaths(root);
}

function linkifyBarePaths(root) {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
        acceptNode: (n) => n.parentElement.closest("a, code, pre, .role-label, .pending-label")
            ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
    });
    const nodes = [];
    while (walker.nextNode()) {
        if (/\/[^/\s]*\.\w/.test(walker.currentNode.nodeValue)) nodes.push(walker.currentNode);
    }
    for (const node of nodes) {
        const text = node.nodeValue;
        const frag = document.createDocumentFragment();
        let last = 0;
        for (const m of text.matchAll(BARE_FILE_REF_RE)) {
            if (!looksLikeFileRef(m[0])) continue;
            frag.append(text.slice(last, m.index));
            const span = document.createElement("span");
            span.className = "chat-file-ref chat-file-ref-bare";
            span.dataset.fileRef = normalizeFileRef(m[0]);
            span.textContent = m[0];
            frag.append(span);
            last = m.index + m[0].length;
        }
        if (last === 0) continue;
        frag.append(text.slice(last));
        node.replaceWith(frag);
    }
}

async function resolveFileRef(ref) {
    const s = state.currentSession;
    if (!s || s.type !== "live") return null;
    try {
        const qs = new URLSearchParams({ filepath: ref, session_id: s.session_id || "" });
        const resp = await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/resolve-path?${qs}`);
        if (!resp.ok) {
            const localArtifact = /^(?:file:\/\/)?(?:\/private\/tmp\/|\/tmp\/|~\/)/i.test(ref);
            showToast(localArtifact
                ? `Artifact isn't reachable: ${ref}. Ask the agent for inline content or a durable URL.`
                : `File not found: ${ref}`, true);
            return null;
        }
        const data = await resp.json();
        return { filepath: data.filepath, line: data.line || 0 };
    } catch {
        showToast(`Could not open ${ref}`, true);
        return null;
    }
}

// Desktop editors installed on the Coral host. Empty when this browser is on
// another machine (the server only offers them to a local client).
let editorsPromise = null;
function installedEditors() {
    if (!editorsPromise) {
        editorsPromise = fetch(serverUrl("/api/system/editors", currentServer()))
            .then(r => (r.ok ? r.json() : { editors: [] }))
            .then(d => d.editors || [])
            .catch(() => { editorsPromise = null; return []; });
    }
    return editorsPromise;
}

async function openRefInEditor(ref, editor) {
    const s = state.currentSession;
    if (!s || s.type !== "live") return;
    try {
        const resp = await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/open-in-editor`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ filepath: ref, session_id: s.session_id || "", editor: editor.id }),
        });
        if (!resp.ok) {
            const err = await resp.json().catch(() => ({}));
            showToast(err.error || `Could not open in ${editor.name}`, true);
        }
    } catch {
        showToast(`Could not open in ${editor.name}`, true);
    }
}

// The open file menu (at most one): looked up by reference, not by a selector over the whole page on every key and every scroll of anything (#4985).
let openFileMenu = null;
function closeFileRefMenu() {
    if (!openFileMenu) return;
    openFileMenu.remove();
    openFileMenu = null;
}

// The file options menu, anchored under the clicked reference. The path is
// resolved first so the menu only appears for files that exist.
async function showFileRefMenu(el) {
    closeFileRefMenu();
    const ref = el.dataset.fileRef;
    const artifactRef = /^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(ref || '');
    el.classList.add("chat-file-ref-loading");
    let res, editors;
    try {
        [res, editors] = artifactRef
            ? [{ filepath: ref, line: 0 }, []]
            : await Promise.all([resolveFileRef(ref), installedEditors()]);
    } finally {
        el.classList.remove("chat-file-ref-loading");
    }
    if (!res || !el.isConnected) return;
    const filepath = typeof res === "string" ? res : res.filepath;
    const line = typeof res === "object" && res.line ? res.line : 0;
    if (!filepath) return;

    const displayPath = line > 0 ? `${filepath}:${line}` : filepath;
    const menu = document.createElement("div");
    menu.className = "chat-file-menu";
    menu.setAttribute("role", "menu");
    menu.innerHTML = `<div class="chat-file-menu-path" title="${escapeHtml(displayPath)}">${escapeHtml(displayPath)}</div>
        <button type="button" role="menuitem" data-action="preview"><span class="material-icons" aria-hidden="true">visibility</span>Preview in Coral${line > 0 ? ` (line ${line})` : ""}</button>
        ${artifactRef ? '' : `<button type="button" role="menuitem" data-action="edit"><span class="material-icons" aria-hidden="true">edit</span>Edit in Coral${line > 0 ? ` (line ${line})` : ""}</button>
        ${editors.map((ed, i) => `<button type="button" role="menuitem" data-action="editor" data-editor="${Number(i)}"><span class="material-icons" aria-hidden="true">open_in_new</span>Open in ${escapeHtml(ed.name)}</button>`).join("")}`}
        <button type="button" role="menuitem" data-action="copy"><span class="material-icons" aria-hidden="true">content_copy</span>${artifactRef ? 'Copy artifact URI' : 'Copy path'}</button>`;
    document.body.appendChild(menu);
    openFileMenu = menu;

    const r = el.getBoundingClientRect();
    const w = menu.offsetWidth, h = menu.offsetHeight;
    const left = Math.max(8, Math.min(r.left, window.innerWidth - w - 8));
    const top = r.bottom + 4 + h > window.innerHeight - 8 ? r.top - h - 4 : r.bottom + 4;
    menu.style.left = `${left}px`;
    menu.style.top = `${Math.max(8, top)}px`;

    menu.addEventListener("click", (e) => {
        const btn = e.target.closest("button[data-action]");
        if (!btn) return;
        closeFileRefMenu();
        if (btn.dataset.action === "preview") window.openFilePreview?.(filepath, line);
        else if (btn.dataset.action === "edit") window.openFileEdit?.(filepath, line);
        else if (btn.dataset.action === "editor") openRefInEditor(ref, editors[btn.dataset.editor]);
        else if (btn.dataset.action === "copy") artifactRef ? copyText(ref) : window.copyFilePath?.(filepath);
    });
    menu.querySelector("button").focus();
}

// File references are only actionable in the live Chat view (it has an agent
// and a Files panel to open them in).
document.addEventListener("click", (e) => {
    if (!e.target.closest) return;
    if (!e.target.closest(".chat-file-menu")) closeFileRefMenu();
    const el = e.target.closest("#live-history-messages [data-file-ref]");
    if (!el) return;
    e.preventDefault();
    showFileRefMenu(el);
});

document.addEventListener("keydown", (e) => {
    const menu = openFileMenu;
    if (!menu || !menu.isConnected) return;
    if (e.key === "Escape") {
        closeFileRefMenu();
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const items = Array.from(menu.querySelectorAll("button"));
        const i = items.indexOf(document.activeElement);
        const next = e.key === "ArrowDown" ? i + 1 : i - 1;
        items[(next + items.length) % items.length].focus();
    }
});

// The menu is placed against the viewport; scrolling the chat would leave it
// behind.
document.addEventListener("scroll", closeFileRefMenu, true);

// Relative links in the history view have nowhere to go; keep them from
// navigating the dashboard away.
document.addEventListener("click", (e) => {
    const el = e.target.closest && e.target.closest("#history-messages a[data-file-ref]");
    if (el) e.preventDefault();
});
