/* Changed files panel — load and render per-agent file diffs */

import { state } from './state.js';
import { escapeHtml } from './utils.js';

let _currentFiles = [];

export async function loadChangedFiles(agentName, sessionId) {
    if (!agentName) return;
    try {
        const params = new URLSearchParams();
        const sid = sessionId || (state.currentSession && state.currentSession.session_id);
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/files${qs}`);
        const data = await resp.json();
        _currentFiles = data.files || [];
    } catch (e) {
        _currentFiles = [];
    }
    renderChangedFiles();
}

export function updateChangedFileCount(count) {
    const el = document.getElementById('files-bar-count');
    if (el) {
        el.textContent = count > 0 ? String(count) : '';
    }
}

function getStatusLabel(status) {
    const map = {
        'M': 'Modified',
        'A': 'Added',
        'D': 'Deleted',
        'R': 'Renamed',
        'C': 'Copied',
        '??': 'Untracked',
        'AM': 'Added',
        'MM': 'Modified',
    };
    return map[status] || status;
}

function getStatusClass(status) {
    if (status === 'A' || status === 'AM' || status === '??') return 'file-added';
    if (status === 'D') return 'file-deleted';
    if (status === 'R') return 'file-renamed';
    return 'file-modified';
}

function splitPath(filepath) {
    const lastSlash = filepath.lastIndexOf('/');
    if (lastSlash === -1) return { dir: '', name: filepath };
    return { dir: filepath.substring(0, lastSlash + 1), name: filepath.substring(lastSlash + 1) };
}

export async function openFilePreview(filepath) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const agentName = state.currentSession.name;
    const sessionId = state.currentSession.session_id;

    const qs = new URLSearchParams({ filepath });
    if (sessionId) qs.set('session_id', sessionId);

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/file-content?${qs}`);
        const data = await resp.json();
        if (data.error) {
            showToast(data.error, true);
            return;
        }

        const content = data.content || '';
        const width = Math.min(900, Math.round(window.screen.width * 0.6));
        const height = Math.min(800, Math.round(window.screen.height * 0.75));
        const left = Math.round((window.screen.width - width) / 2);
        const top = Math.round((window.screen.height - height) / 2);

        const win = window.open('', 'coral-preview',
            `width=${width},height=${height},left=${left},top=${top},menubar=no,toolbar=no,status=no`);
        if (!win) { showToast('Popup blocked', true); return; }

        // Render markdown-like content with basic HTML conversion
        const rendered = _renderMarkdownBasic(content);

        win.document.write(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>${escapeHtml(filepath)}</title>
<style>
    :root { color-scheme: dark; }
    body {
        font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
        max-width: 800px; margin: 0 auto; padding: 24px 32px;
        background: #0d1117; color: #e6edf3; line-height: 1.6;
    }
    .preview-header {
        display: flex; justify-content: space-between; align-items: center;
        padding: 8px 0 16px; border-bottom: 1px solid #30363d; margin-bottom: 20px;
    }
    .preview-header h3 { margin: 0; font-size: 14px; color: #8b949e; font-weight: 400; }
    .preview-header button {
        padding: 6px 14px; background: #238636; color: #fff; border: none;
        border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: 600;
    }
    .preview-header button:hover { background: #2ea043; }
    .preview-header button.secondary { background: #30363d; }
    .preview-header button.secondary:hover { background: #484f58; }
    #editor {
        width: 100%; min-height: 400px; padding: 12px; background: #161b22;
        border: 1px solid #30363d; border-radius: 6px; color: #e6edf3;
        font-family: 'SF Mono', Consolas, monospace; font-size: 13px;
        line-height: 1.5; resize: vertical; display: none;
    }
    #editor:focus { outline: none; border-color: #58a6ff; }
    h1, h2, h3, h4, h5, h6 { color: #e6edf3; border-bottom: 1px solid #21262d; padding-bottom: 6px; }
    h1 { font-size: 24px; } h2 { font-size: 20px; } h3 { font-size: 16px; }
    a { color: #58a6ff; } code { background: #161b22; padding: 2px 6px; border-radius: 4px; font-size: 90%; }
    pre { background: #161b22; padding: 12px; border-radius: 6px; overflow-x: auto; }
    pre code { background: none; padding: 0; }
    blockquote { border-left: 3px solid #30363d; margin: 8px 0; padding: 4px 16px; color: #8b949e; }
    table { border-collapse: collapse; width: 100%; margin: 12px 0; }
    th, td { border: 1px solid #30363d; padding: 6px 12px; text-align: left; }
    th { background: #161b22; }
    ul, ol { padding-left: 24px; }
    li { margin: 4px 0; }
    hr { border: none; border-top: 1px solid #30363d; margin: 16px 0; }
    img { max-width: 100%; }
    .task-list-item { list-style: none; margin-left: -20px; }
    .task-list-item input { margin-right: 6px; }
</style>
</head>
<body>
    <div class="preview-header">
        <h3>${escapeHtml(filepath)}</h3>
        <div>
            <button class="secondary" onclick="toggleEdit()">Edit</button>
            <button onclick="saveFile()">Save</button>
        </div>
    </div>
    <div id="rendered">${rendered}</div>
    <textarea id="editor">${escapeHtml(content)}</textarea>
    <script>
        const filepath = ${JSON.stringify(filepath)};
        const agentName = ${JSON.stringify(agentName)};
        const sessionId = ${JSON.stringify(sessionId || '')};
        let editing = false;

        function toggleEdit() {
            editing = !editing;
            document.getElementById('rendered').style.display = editing ? 'none' : 'block';
            document.getElementById('editor').style.display = editing ? 'block' : 'none';
            if (!editing) {
                // Re-render preview from editor content
                document.getElementById('rendered').innerHTML = window.opener?._renderMarkdownBasic?.(document.getElementById('editor').value) || document.getElementById('editor').value;
            }
        }

        async function saveFile() {
            const content = editing ? document.getElementById('editor').value : null;
            if (!content && !editing) { alert('Switch to Edit mode to make changes'); return; }
            try {
                const qs = new URLSearchParams({ filepath });
                if (sessionId) qs.set('session_id', sessionId);
                const resp = await fetch(window.opener?.location?.origin + '/api/sessions/live/' + encodeURIComponent(agentName) + '/file-content?' + qs, {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ content }),
                });
                const data = await resp.json();
                if (data.error) { alert('Error: ' + data.error); return; }
                document.title = filepath + ' (saved)';
                setTimeout(() => { document.title = filepath; }, 2000);
            } catch (e) {
                alert('Failed to save: ' + e.message);
            }
        }
    </script>
</body>
</html>`);
        win.document.close();
    } catch (e) {
        showToast('Failed to load file', true);
    }
}

// Basic markdown to HTML renderer (no dependencies)
function _renderMarkdownBasic(md) {
    let html = md
        // Code blocks (``` ... ```)
        .replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) =>
            `<pre><code class="language-${lang}">${code.replace(/</g, '&lt;').replace(/>/g, '&gt;')}</code></pre>`)
        // Headings
        .replace(/^######\s+(.*)$/gm, '<h6>$1</h6>')
        .replace(/^#####\s+(.*)$/gm, '<h5>$1</h5>')
        .replace(/^####\s+(.*)$/gm, '<h4>$1</h4>')
        .replace(/^###\s+(.*)$/gm, '<h3>$1</h3>')
        .replace(/^##\s+(.*)$/gm, '<h2>$1</h2>')
        .replace(/^#\s+(.*)$/gm, '<h1>$1</h1>')
        // Horizontal rules
        .replace(/^---+$/gm, '<hr>')
        // Bold + italic
        .replace(/\*\*\*(.*?)\*\*\*/g, '<strong><em>$1</em></strong>')
        .replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
        .replace(/\*(.*?)\*/g, '<em>$1</em>')
        // Inline code
        .replace(/`([^`]+)`/g, '<code>$1</code>')
        // Links
        .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank">$1</a>')
        // Images
        .replace(/!\[([^\]]*)\]\(([^)]+)\)/g, '<img alt="$1" src="$2">')
        // Blockquotes
        .replace(/^>\s+(.*)$/gm, '<blockquote>$1</blockquote>')
        // Task lists
        .replace(/^- \[x\]\s+(.*)$/gm, '<li class="task-list-item"><input type="checkbox" checked disabled> $1</li>')
        .replace(/^- \[ \]\s+(.*)$/gm, '<li class="task-list-item"><input type="checkbox" disabled> $1</li>')
        // Unordered lists
        .replace(/^[-*]\s+(.*)$/gm, '<li>$1</li>')
        // Paragraphs (double newline)
        .replace(/\n\n/g, '</p><p>')
        // Single newlines to <br>
        .replace(/\n/g, '<br>');

    // Wrap loose <li> in <ul>
    html = html.replace(/(<li.*?<\/li>(?:<br>)?)+/g, '<ul>$&</ul>');
    // Clean up <br> inside <ul>
    html = html.replace(/<ul>(.*?)<\/ul>/gs, (_, inner) => '<ul>' + inner.replace(/<br>/g, '') + '</ul>');

    return '<p>' + html + '</p>';
}

// Expose for the preview window to use
window._renderMarkdownBasic = _renderMarkdownBasic;

export function openFileDiff(filepath) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const agentName = state.currentSession.name;
    const sessionId = state.currentSession.session_id;

    // Build the file list for prev/next navigation
    const fileList = _currentFiles.map(f => f.filepath);

    const qs = new URLSearchParams({
        agent: agentName,
        file: filepath,
        files: fileList.join('\n'),
    });
    if (sessionId) qs.set('session_id', sessionId);

    const width = Math.min(1200, Math.round(window.screen.width * 0.7));
    const height = Math.min(900, Math.round(window.screen.height * 0.8));
    const left = Math.round((window.screen.width - width) / 2);
    const top = Math.round((window.screen.height - height) / 2);

    window.open(
        `/diff?${qs}`,
        'coral-diff',
        `width=${width},height=${height},left=${left},top=${top},menubar=no,toolbar=no,status=no`,
    );
}

export async function refreshChangedFiles() {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const btn = document.querySelector('.refresh-files-btn');
    if (btn) btn.classList.add('refreshing');

    const agentName = state.currentSession.name;
    const sessionId = state.currentSession.session_id;

    try {
        const body = {};
        if (sessionId) body.session_id = sessionId;
        const resp = await fetch(
            `/api/sessions/live/${encodeURIComponent(agentName)}/files/refresh`,
            {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body),
            }
        );

        if (resp.ok) {
            const data = await resp.json();
            _currentFiles = data.files || [];
            renderChangedFiles();
        } else {
            // Fall back to cached data
            await loadChangedFiles(agentName, sessionId);
        }
    } catch (e) {
        await loadChangedFiles(agentName, sessionId);
    }

    if (btn) setTimeout(() => btn.classList.remove('refreshing'), 300);
}

export function renderChangedFiles() {
    const list = document.getElementById('changed-files-list');
    const titleEl = document.getElementById('changed-files-title');
    const countEl = document.getElementById('files-bar-count');
    if (!list) return;

    const files = _currentFiles;

    if (titleEl) {
        titleEl.textContent = `${files.length} file${files.length !== 1 ? 's' : ''} changed`;
    }
    if (countEl) {
        countEl.textContent = files.length > 0 ? String(files.length) : '';
    }

    if (files.length === 0) {
        list.innerHTML = '<div class="file-empty">No changed files</div>';
        return;
    }

    list.innerHTML = files.map((f, idx) => {
        const { dir, name } = splitPath(f.filepath);
        const statusCls = getStatusClass(f.status);
        const statusLabel = getStatusLabel(f.status);
        const adds = f.additions > 0 ? `<span class="file-adds">+${f.additions}</span>` : '';
        const dels = f.deletions > 0 ? `<span class="file-dels">-${f.deletions}</span>` : '';
        const stats = (adds || dels) ? `<span class="file-stats">${adds}${dels}</span>` : '';
        const statusIcon = f.status === '??' ? '?' : f.status === 'A' || f.status === 'AM' ? '+' : f.status === 'D' ? '-' : '~';
        const escapedPath = escapeHtml(f.filepath).replace(/'/g, "\\'");

        const isPreviewable = /\.(md|mdx|txt|rst|html)$/i.test(name);
        const previewBtn = isPreviewable ? `<button class="file-preview-btn" onclick="event.stopPropagation(); openFilePreview('${escapedPath}')" title="Preview file">&#x1F4C4;</button>` : '';

        return `<div class="file-item ${statusCls}" title="${escapeHtml(f.filepath)} (${statusLabel})"
                     onclick="openFileDiff('${escapedPath}')">
            <span class="file-status-icon">${statusIcon}</span>
            <div class="file-path-wrap">
                <span class="file-name">${escapeHtml(name)}</span>
                ${dir ? `<span class="file-dir">${escapeHtml(dir)}</span>` : ''}
            </div>
            ${stats}
            ${previewBtn}
        </div>`;
    }).join('');
}
