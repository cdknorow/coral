/* Optional, lazy team artifact source for the existing Files viewer. */
import { state } from './state.js';

let source = 'files';
let team = null;
let items = [];
let loaded = false;
let busy = false;
let error = '';
let hasMore = false;
let truncated = false;
let controller = null;
let generation = 0;
let previewArtifact = null;

function currentTeam() {
    return state.currentSession?.type === 'live' ? state.currentSession.board_project || null : null;
}

function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
}

function readableName(item) {
    const name = String(item.name || '').trim();
    if (name && !/^(?:coral:\/\/artifacts\/)?[a-f0-9]{64}(?:\.[a-z0-9]+)?$/i.test(name)) return name;
    return item.task_title ? `${item.task_title} — artifact` : `Task #${item.task_id} artifact`;
}

function artifactLink(item) {
    if (item.available === false) return null;
    const managed = /^(?:coral:\/\/artifacts\/|\/api\/artifacts\/)([a-f0-9]{64})$/i.exec(item.uri || '');
    if (managed) return { url: `/api/artifacts/${managed[1]}`, preview: true };
    const contentPath = `/api/board/${encodeURIComponent(team)}/tasks/${item.task_id}/artifact-content`;
    if (item.inline && item.content_url?.startsWith(contentPath + '?')) {
        return { url: item.content_url, preview: true };
    }
    try {
        const url = new URL(item.uri);
        if (url.protocol === 'https:' || url.protocol === 'http:') return { url: url.href, preview: false };
    } catch { /* unavailable or unsupported reference */ }
    return null;
}

function sizeLabel(size) {
    if (size == null || !Number.isFinite(Number(size))) return '';
    if (size < 1024) return `${size} B`;
    if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
    return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function render() {
    const root = document.getElementById('team-artifacts-view');
    if (!root) return;
    root.replaceChildren();
    root.setAttribute('aria-busy', String(busy));
    const heading = el('div', 'team-artifacts-heading');
    heading.append(el('span', '', team ? `Artifacts · ${team}` : 'Team artifacts'));
    const refresh = el('button', 'team-artifacts-action', 'Refresh');
    refresh.type = 'button'; refresh.disabled = busy || !team;
    refresh.addEventListener('click', () => load(false));
    heading.append(refresh); root.append(heading);
    const status = el('div', 'team-artifacts-status');
    status.setAttribute('role', error ? 'alert' : 'status');
    status.textContent = !team ? 'Select an agent on a team to browse its artifacts.'
        : error ? error : busy ? 'Loading team artifacts…'
        : !items.length ? 'No artifacts shared by this team yet.' : `${items.length} artifacts`;
    root.append(status);
    if (error) {
        const retry = el('button', 'team-artifacts-action', 'Retry');
        retry.type = 'button'; retry.addEventListener('click', () => load(items.length > 0));
        root.append(retry);
    }
    const list = el('ul', 'team-artifacts-list');
    for (const item of items) {
        const row = el('li', 'team-artifact-row');
        const info = el('div', 'team-artifact-info');
        const name = readableName(item);
        const link = artifactLink(item);
        const title = el(link?.preview ? 'button' : 'span', 'team-artifact-name', name);
        if (link?.preview) {
            title.type = 'button';
            title.addEventListener('click', () => previewArtifact?.({ ...item, name, uri: link.url.startsWith('/api/artifacts/') ? `coral://artifacts/${link.url.split('/').pop()}` : item.uri, content_url: item.inline ? link.url : null }));
        }
        info.append(title);
        const context = [item.media_type || 'File', sizeLabel(item.size)];
        if (item.task_id) context.push(`Task #${item.task_id}${item.task_title ? ` · ${item.task_title}` : ''}`);
        if (item.source === 'review') context.push('Review');
        if (item.created_at) {
            const date = new Date(item.created_at);
            if (!Number.isNaN(date.getTime())) context.push(date.toLocaleString());
        }
        if (item.reference_count > 1) context.push(`${item.reference_count} references`);
        info.append(el('div', 'team-artifact-meta', context.filter(Boolean).join(' · ')));
        row.append(info);
        if (link) {
            const download = el('a', 'team-artifacts-action', link.preview ? 'Download' : 'Open link');
            download.href = link.url;
            if (link.preview) download.download = name;
            else { download.target = '_blank'; download.rel = 'noopener noreferrer'; }
            download.setAttribute('aria-label', `${link.preview ? 'Download' : 'Open'} ${name}`);
            row.append(download);
        } else row.append(el('span', 'team-artifact-missing', item.available === false ? 'Missing' : 'Unavailable'));
        list.append(row);
    }
    root.append(list);
    if (hasMore) {
        const more = el('button', 'team-artifacts-action team-artifacts-more', busy ? 'Loading…' : 'Load more');
        more.type = 'button'; more.disabled = busy;
        more.addEventListener('click', () => load(true)); root.append(more);
    }
    if (truncated) root.append(el('div', 'team-artifacts-status', 'Some older artifacts are not included in this list.'));
}

async function load(append = false) {
    controller?.abort();
    const gen = ++generation;
    if (!team || source !== 'artifacts') return;
    controller = new AbortController();
    const requestedTeam = team;
    if (!append) { items = []; hasMore = false; truncated = false; loaded = false; }
    busy = true; error = ''; render();
    try {
        const response = await fetch(`/api/board/${encodeURIComponent(team)}/artifacts?limit=100&offset=${items.length}`, { signal: controller.signal });
        if (!response.ok) throw new Error(`Unable to load team artifacts (${response.status}).`);
        const data = await response.json();
        if (gen !== generation || requestedTeam !== currentTeam() || source !== 'artifacts') return;
        if (data.project !== requestedTeam || !Array.isArray(data.artifacts)) throw new Error('Unable to load team artifacts: unexpected response.');
        items = append ? [...items, ...data.artifacts] : data.artifacts;
        hasMore = !!data.has_more; truncated = !!data.truncated; loaded = true;
    } catch (err) {
        if (gen !== generation || err.name === 'AbortError') return;
        error = err.message || 'Unable to load team artifacts.';
    } finally {
        if (gen === generation) { busy = false; render(); }
    }
}

function updateSource() {
    const files = document.getElementById('repository-files-view');
    const artifacts = document.getElementById('team-artifacts-view');
    if (files) files.hidden = source !== 'files';
    if (artifacts) artifacts.hidden = source !== 'artifacts';
    document.querySelectorAll('[data-files-source]').forEach(button => {
        button.setAttribute('aria-pressed', String(button.dataset.filesSource === source));
    });
}

function selectSource(next) {
    if (source === next) return;
    source = next;
    controller?.abort(); ++generation; busy = false;
    updateSource();
    if (source === 'artifacts') { render(); if (!loaded) load(); }
}

// Re-mount after closing a preview: preserve the selected source and loaded list.
export function initFilesSourcePicker(onPreview) {
    previewArtifact = onPreview;
    const panel = document.getElementById('agentic-panel-files');
    if (!panel || panel.querySelector('.inline-preview-header')) return;
    if (!document.getElementById('files-source-picker')) {
        const files = el('div', 'repository-files-view'); files.id = 'repository-files-view';
        while (panel.firstChild) files.append(panel.firstChild);
        const picker = el('div', 'files-source-picker'); picker.id = 'files-source-picker';
        picker.setAttribute('role', 'group'); picker.setAttribute('aria-label', 'File source');
        for (const [value, label] of [['files', 'Files'], ['artifacts', 'Team artifacts']]) {
            const button = el('button', 'files-source-button', label);
            button.type = 'button'; button.dataset.filesSource = value;
            button.setAttribute('aria-controls', value === 'files' ? 'repository-files-view' : 'team-artifacts-view');
            button.addEventListener('click', () => selectSource(value)); picker.append(button);
        }
        const artifacts = el('div', 'team-artifacts-view'); artifacts.id = 'team-artifacts-view';
        panel.append(picker, files, artifacts);
    }
    syncFilesSourceTeam(); updateSource(); render();
}

export function syncFilesSourceTeam() {
    const next = currentTeam();
    if (next === team) return;
    controller?.abort(); ++generation;
    team = next; items = []; loaded = false; busy = false; error = ''; hasMore = false; truncated = false;
    render();
    if (source === 'artifacts') load();
}
