/* Optional, lazy team artifact source for the existing Files viewer. */
import { state } from './state.js';

import { mountExplorer, showExplorer, syncExplorerSession } from './file_explorer.js';

let source = 'files';
let team = null;
let sessionID = null;
let previewArtifact = null;
let previewFile = null;
const fresh = () => ({items:[], loaded:false, busy:false, error:'', hasMore:false, truncated:false, controller:null, generation:0});
const caches = {artifacts:fresh(), 'team-artifacts':fresh()};
const isArtifactSource = () => source === 'artifacts' || source === 'team-artifacts';
const panels = {files:'repository-files-view', browse:'file-explorer-view', artifacts:'team-artifacts-view', 'team-artifacts':'team-artifacts-view'};
function cancel(cache) { cache.controller?.abort(); ++cache.generation; cache.busy=false; }
function clear(scope) { cancel(caches[scope]); caches[scope]=fresh(); }

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

function artifactType(item) {
    const mime = String(item.media_type || '').split(';')[0].trim().toLowerCase();
    const named = {'text/markdown':'Markdown', 'text/x-markdown':'Markdown', 'text/html':'HTML', 'application/xhtml+xml':'HTML', 'application/json':'JSON', 'application/pdf':'PDF', 'text/plain':'Text', 'text/csv':'CSV'};
    if (named[mime]) return named[mime];
    for (const [prefix, label] of [['image/', 'Image'], ['audio/', 'Audio'], ['video/', 'Video']]) if (mime.startsWith(prefix)) return label;
    if (/\.(md|markdown|mdown)$/i.test(item.name || '')) return 'Markdown';
    return mime.startsWith('text/') ? 'Text' : 'File';
}

function render() {
    const root = document.getElementById('team-artifacts-view');
    if (!root || !isArtifactSource()) return;
    const {items, busy, error, hasMore, truncated} = caches[source];
    const personal = source === 'artifacts';
    const scopeName = personal ? state.currentSession?.display_name || state.currentSession?.name || 'Selected agent' : team;
    root.replaceChildren();
    root.setAttribute('aria-busy', String(busy));
    const heading = el('div', 'team-artifacts-heading');
    heading.append(el('span', '', team ? `${personal ? 'Artifacts' : 'Team Artifacts'} · ${scopeName}` : personal ? 'Artifacts' : 'Team Artifacts'));
    const refresh = el('button', 'team-artifacts-action', 'Refresh');
    refresh.type = 'button'; refresh.disabled = busy || !team || (personal && !sessionID);
    refresh.addEventListener('click', () => load(false));
    heading.append(refresh); root.append(heading);
    const status = el('div', 'team-artifacts-status');
    status.setAttribute('role', error ? 'alert' : 'status');
    status.textContent = !team ? 'Select an agent on a team to browse its artifacts.'
        : error ? error : busy ? (personal ? 'Loading agent artifacts…' : 'Loading team artifacts…')
        : !items.length ? (personal ? 'No artifacts attributed to this agent yet.' : 'No artifacts shared by this team yet.') : `${items.length} artifacts`;
    root.append(status);
    if (error) {
        const retry = el('button', 'team-artifacts-action', 'Retry');
        retry.type = 'button'; retry.addEventListener('click', () => load(items.length > 0));
        root.append(retry);
    }
    const scroll = el('div', 'team-artifacts-scroll');
    scroll.tabIndex = 0;
    scroll.setAttribute('role', 'region');
    scroll.setAttribute('aria-label', 'Artifact table, scroll horizontally for all columns');
    const hint = el('p', 'team-artifacts-scroll-hint', 'Scroll to view all columns.');
    root.append(hint);
    const list = el('table', 'team-artifacts-list');
    const caption = el('caption', 'team-artifacts-caption', personal ? 'Agent artifacts' : 'Team artifacts');
    list.append(caption);
    const head = el('thead');
    const headers = el('tr');
    for (const label of ['Name', 'Type', 'Size', 'Task', 'Created', 'Actions']) {
        const cell = el('th', `team-artifact-col-${label.toLowerCase()}`, label);
        cell.scope = 'col';headers.append(cell);
    }
    head.append(headers);list.append(head);
    const rows = el('tbody');list.append(rows);
    for (const item of items) {
        const row = el('tr', 'team-artifact-row');
        const info = el('td', 'team-artifact-info team-artifact-col-name');
        const name = readableName(item);
        const link = artifactLink(item);
        const openPreview = () => previewArtifact?.({
            ...item, name,
            uri: link.url.startsWith('/api/artifacts/') ? `coral://artifacts/${link.url.split('/').pop()}` : item.uri,
            content_url: item.inline ? link.url : null,
            external_url: link.preview ? null : link.url,
        });
        const title = el(link ? 'button' : 'span', 'team-artifact-name', name);
        if (link) {
            title.type = 'button';
            title.addEventListener('click', openPreview);
        }
        title.title = name;
        info.append(title);
        const date = item.created_at ? new Date(item.created_at) : null;
        const created = date && !Number.isNaN(date.getTime()) ? date.toLocaleString() : '—';
        const task = item.task_id ? `#${item.task_id}` : '—';
        const type = artifactType(item);
        const size = sizeLabel(item.size) || '—';
        const details = el('details', 'team-artifact-details');
        const summary = el('summary', '', 'Details');
        summary.setAttribute('aria-label', `Details for ${name}`);
        const fields = el('dl');
        for (const [label, value] of [
            ['Name', name], ['Type', type], ['MIME', item.media_type || 'Unknown'],
            ['Size', size], ['Task', task], ['Task title', item.task_title],
            ['Created', created], ['Producer', item.subscriber_id],
            ['Source', item.source], ['References', item.reference_count],
        ]) {
            if (value != null && value !== '') fields.append(el('dt', '', label), el('dd', '', String(value)));
        }
        details.append(summary, fields);info.append(details);row.append(info);
        row.append(el('td', 'team-artifact-col-type', type), el('td', 'team-artifact-col-size', size));
        const taskCell = el('td', 'team-artifact-col-task', task);
        if (item.task_title) {
            const taskTitle = el('div', 'team-artifact-task-title', item.task_title);
            taskTitle.title = item.task_title;taskCell.append(taskTitle);
        }
        row.append(taskCell, el('td', 'team-artifact-col-created', created));
        const actionCell = el('td', 'team-artifact-col-actions');row.append(actionCell);
        if (link) {
            const actions = el('div', 'team-artifact-actions');
            const preview = el('button', 'team-artifacts-action team-artifact-preview', 'Preview');
            preview.type = 'button';
            preview.setAttribute('aria-label', `Preview ${name}`);
            preview.addEventListener('click', openPreview);
            actions.append(preview);
            const download = el('a', 'team-artifacts-action', link.preview ? 'Download' : 'Open link');
            download.href = link.url;
            if (link.preview) download.download = name;
            else { download.target = '_blank'; download.rel = 'noopener noreferrer'; }
            download.setAttribute('aria-label', `${link.preview ? 'Download' : 'Open'} ${name}`);
            actions.append(download);
            actionCell.append(actions);
        } else actionCell.append(el('span', 'team-artifact-missing', item.available === false ? 'Missing' : 'Unavailable'));
        rows.append(row);
    }
    scroll.append(list);root.append(scroll);
    if (hasMore) {
        const more = el('button', 'team-artifacts-action team-artifacts-more', busy ? 'Loading…' : 'Load more');
        more.type = 'button'; more.disabled = busy;
        more.addEventListener('click', () => load(true)); root.append(more);
    }
    if (truncated) root.append(el('div', 'team-artifacts-status', 'Some older artifacts are not included in this list.'));
}

async function load(append = false) {
    if (!team || !isArtifactSource() || (source === 'artifacts' && !sessionID)) return;
    const scope = source;
    const cache = caches[scope];
    cancel(cache);
    const gen = cache.generation;
    cache.controller = new AbortController();
    const requestedTeam = team;
    const requestedSession = sessionID;
    if (!append) { cache.items=[]; cache.hasMore=false; cache.truncated=false; cache.loaded=false; }
    cache.busy=true; cache.error=''; render();
    try {
        const params = new URLSearchParams({limit:'100',offset:String(cache.items.length)});
        if (scope === 'artifacts') params.set('session_id',requestedSession);
        const response = await fetch(`/api/board/${encodeURIComponent(team)}/artifacts?${params}`, {signal:cache.controller.signal});
        if (!response.ok) throw new Error(`Unable to load ${scope === 'artifacts' ? 'agent' : 'team'} artifacts (${response.status}).`);
        const data = await response.json();
        if (gen !== cache.generation || caches[scope] !== cache || requestedTeam !== currentTeam() || source !== scope || (scope === 'artifacts' && requestedSession !== state.currentSession?.session_id)) return;
        if (data.project !== requestedTeam || !Array.isArray(data.artifacts) || (scope === 'artifacts' && data.session_id !== requestedSession)) throw new Error('Unable to load artifacts: unexpected response scope.');
        cache.items = append ? [...cache.items,...data.artifacts] : data.artifacts;
        cache.hasMore=!!data.has_more; cache.truncated=!!data.truncated; cache.loaded=true;
    } catch (error) {
        if (gen !== cache.generation || caches[scope] !== cache || error.name === 'AbortError') return;
        cache.error=error.message || 'Unable to load artifacts.';
    } finally {
        if (gen === cache.generation && caches[scope] === cache) { cache.busy=false; render(); }
    }
}

function updateSource() {
    for (const id of new Set(Object.values(panels))) {
        const panel=document.getElementById(id); if(panel) panel.hidden=id!==panels[source];
    }
    document.querySelectorAll('[data-files-source]').forEach(button=>{
        const selected=button.dataset.filesSource===source;
        button.setAttribute('aria-selected',String(selected));
        button.tabIndex=selected?0:-1;
    });
    const panel=document.getElementById(panels[source]);
    if(panel)panel.setAttribute('aria-labelledby',`files-source-${source}`);
}

function activate() {
    if(source==='browse') showExplorer();
    else if(isArtifactSource()) {render(); if(!caches[source].loaded && !caches[source].busy)load();}
}

function selectSource(next) {
    if(source===next)return;
    if(isArtifactSource())cancel(caches[source]);
    source=next; updateSource(); activate();
}

// Both list and preview use the same source navigation and keyboard behavior.
function createSourcePicker(onSelect, preview = false) {
    const picker=el('div','files-source-picker');
    picker.id=preview?'preview-files-source-picker':'files-source-picker';
    picker.setAttribute('role','tablist');picker.setAttribute('aria-label','Viewer source');
    const tabs=[['files','Files'],['browse','Browse'],['artifacts','Artifacts'],['team-artifacts','Team Artifacts']];
    for(const [value,label] of tabs) {
        const button=el('button','files-source-button',label);
        const selected=value===source;
        button.type='button';button.dataset.filesSource=value;
        button.id=`${preview?'preview-':''}files-source-${value}`;
        button.setAttribute('role','tab');
        button.setAttribute('aria-controls',preview?'inline-preview-body':panels[value]);
        button.setAttribute('aria-selected',String(selected));
        button.tabIndex=selected?0:-1;
        button.addEventListener('click',()=>onSelect(value));picker.append(button);
    }
    picker.addEventListener('keydown',event=>{
        const buttons=[...picker.querySelectorAll('button')];const i=buttons.indexOf(event.target);if(i<0)return;
        let target;
        if(event.key==='ArrowRight')target=buttons[(i+1)%buttons.length];
        else if(event.key==='ArrowLeft')target=buttons[(i+buttons.length-1)%buttons.length];
        else if(event.key==='Home')target=buttons[0];else if(event.key==='End')target=buttons.at(-1);else return;
        event.preventDefault();target.click();
        // Leaving preview rebuilds the list; focus the replacement tab.
        (target.isConnected?target:document.getElementById(`files-source-${target.dataset.filesSource}`))?.focus();
    });
    return picker;
}

export function mountPreviewSourcePicker(panel, closePreview) {
    const picker=createSourcePicker(value=>{
        closePreview();
        if (window.innerWidth <= 767) {
            window.switchAgenticTab?.('files', 'top');
            window.toggleAgenticPanel?.(true);
        }
        selectSource(value);
        document.getElementById(`files-source-${value}`)?.focus();
    },true);
    panel.prepend(picker);
    const body=panel.querySelector('#inline-preview-body');
    body?.setAttribute('role','tabpanel');
    body?.setAttribute('aria-labelledby',`preview-files-source-${source}`);
}

// Re-mount after closing a preview: keep source, scoped list and expanded tree.
export function initFilesSourcePicker(onPreview, onFilePreview) {
    previewArtifact=onPreview; previewFile=onFilePreview;
    const panel=document.getElementById('agentic-panel-files');
    if(!panel || panel.querySelector('.inline-preview-header'))return;
    if(!document.getElementById('files-source-picker')) {
        const files=el('div','repository-files-view');files.id=panels.files;
        while(panel.firstChild)files.append(panel.firstChild);
        const picker=createSourcePicker(selectSource);
        const artifacts=el('div','team-artifacts-view');artifacts.id=panels.artifacts;
        const browse=el('div','file-explorer-view');browse.id=panels.browse;
        for(const view of [files,artifacts,browse])view.setAttribute('role','tabpanel');
        panel.append(picker,files,browse,artifacts);
    }
    mountExplorer(previewFile);
    syncFilesSourceTeam();updateSource();render();activate();
}

export function syncFilesSourceTeam() {
    const next=currentTeam();
    const nextSession=state.currentSession?.type==='live'?state.currentSession.session_id:null;
    const teamChanged=next!==team;
    const agentChanged=nextSession!==sessionID;
    syncExplorerSession();
    if(!teamChanged&&!agentChanged)return;
    if(teamChanged)clear('team-artifacts');
    if(teamChanged||agentChanged)clear('artifacts');
    team=next;sessionID=nextSession;
    render();activate();
}
