/* Optional, lazy team artifact source for the existing Files viewer. */
import { state } from './state.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { mountExplorer, showExplorer, syncExplorerSession } from './file_explorer.js';
import { boardFetch, teamFetch, serverUrl, splitKey, sessionTeamKey, sessionServer } from './server_base.js';

let source = 'files';
let team = null;
let sessionID = null;
let previewArtifact = null;
let previewFile = null;
const fresh = () => ({items:[], loaded:false, busy:false, error:'', hasMore:false, truncated:false, controller:null, generation:0});
const caches = {artifacts:fresh(), 'team-artifacts':fresh()};
const isArtifactSource = () => source === 'artifacts' || source === 'team-artifacts';
const panels = {files:'agentic-panel-files', browse:'agentic-panel-browse', artifacts:'agentic-panel-artifacts', 'team-artifacts':'agentic-panel-team-artifacts', knowledge:'agentic-panel-knowledge'};
function cancel(cache) { cache.controller?.abort(); ++cache.generation; cache.busy=false; }
function clear(scope) { cancel(caches[scope]); caches[scope]=fresh(); }

function currentTeam() {
    // A selected team (team context) wins over the selected agent's team.
    if (state.selectedTeam) return state.selectedTeam;
    return state.currentSession?.type === 'live' ? sessionTeamKey(state.currentSession) || null : null;
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
    if (managed) return { url: serverUrl(`/api/artifacts/${managed[1]}`, splitKey(team).server), preview: true };
    const contentPath = `/api/board/${encodeURIComponent(splitKey(team).name)}/tasks/${item.task_id}/artifact-content`;
    if (item.inline && item.content_url?.startsWith(contentPath + '?')) {
        return { url: serverUrl(item.content_url, splitKey(team).server), preview: true };
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

// Paste into an agent's chat when it does not know how to publish an artifact.
const AGENT_ARTIFACT_PROMPT = `To publish an artifact in Coral (so it shows in the Artifacts tab):
1. Upload the file: coral-agent artifact upload <file>   (prints a coral://artifacts/<digest> URI)
2. Write a manifest, e.g. artifacts.json: [{"name": "<file name>", "uri": "coral://artifacts/<digest>"}]
3. Attach it to a task result: coral-agent task complete <task-id> --artifacts artifacts.json --message "<summary>"
   (no task yet? create and claim one first: coral-agent task add "<title>")
Local paths like /tmp/<file> are not reachable by the user, so always upload first. Small text reports can go inline as "content" in the manifest. Full docs: artifacts.md in the Coral agent docs.`;

function render() {
    const root = document.getElementById(`${panels[source]}-view`);
    if (!root || !isArtifactSource()) return;
    const {items, busy, error, hasMore, truncated} = caches[source];
    const personal = source === 'artifacts';
    const scopeName = personal ? state.currentSession?.display_name || state.currentSession?.name || 'Selected agent' : team;
    root.replaceChildren();
    root.setAttribute('aria-busy', String(busy));
    const heading = el('div', 'team-artifacts-heading');
    heading.append(el('span', '', (personal || team) ? `${personal ? 'Artifacts' : 'Team Artifacts'} · ${scopeName}` : personal ? 'Artifacts' : 'Team Artifacts'));
    const refresh = el('button', 'team-artifacts-action', 'Refresh');
    refresh.type = 'button'; refresh.disabled = busy || (personal ? !sessionID : !team);
    refresh.addEventListener('click', () => load(false));
    const help = el('button', 'team-artifacts-action team-artifacts-help');
    help.type = 'button';
    help.title = 'Copy instructions for agents on how to publish an artifact';
    help.setAttribute('aria-label', help.title);
    const helpIcon = el('span', 'material-icons', 'content_copy');
    helpIcon.setAttribute('aria-hidden', 'true');
    help.append(helpIcon);
    help.addEventListener('click', async () => {
        try {
            await navigator.clipboard.writeText(AGENT_ARTIFACT_PROMPT);
            helpIcon.textContent = 'check';
        } catch { helpIcon.textContent = 'error_outline'; }
        setTimeout(() => { helpIcon.textContent = 'content_copy'; }, 1500);
    });
    heading.append(refresh, help); root.append(heading);
    const status = el('div', 'team-artifacts-status');
    status.setAttribute('role', error ? 'alert' : 'status');
    status.textContent = (personal ? !sessionID : !team) ? (personal ? 'Select an agent to browse its artifacts.' : 'Select an agent on a team to browse its artifacts.')
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
        const doPreview = () => {
            if (!link || !previewArtifact) return;
            const managed = /^(?:coral:\/\/artifacts\/|\/api\/artifacts\/)([a-f0-9]{64})$/i.exec(item.uri || '');
            previewArtifact({
                ...item, name,
                uri: managed ? `coral://artifacts/${managed[1].toLowerCase()}` : (item.uri || ''),
                external_url: link.preview ? null : link.url,
            });
        };
        const title = el(link ? 'button' : 'span', 'team-artifact-name', name);
        if (link) {
            title.type = 'button';
            title.addEventListener('click', doPreview);
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
            const preview = el('button', 'team-artifacts-action team-artifact-icon-btn team-artifact-preview');
            preview.type = 'button';
            preview.innerHTML = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 8s2.5-5 7-5 7 5 7 5-2.5 5-7 5-7-5-7-5z"/><circle cx="8" cy="8" r="2.5"/></svg>';
            preview.title = 'Preview';
            preview.setAttribute('aria-label', `Preview ${name}`);
            preview.addEventListener('click', doPreview);
            actions.append(preview);
            const download = el('a', 'team-artifacts-action team-artifact-icon-btn');
            download.href = link.url;
            if (link.preview) {
                download.download = name;
                download.innerHTML = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 10v3h12v-3"/><path d="M8 2v8m-3-3 3 3 3-3"/></svg>';
                download.title = 'Download';
            } else {
                download.target = '_blank'; download.rel = 'noopener noreferrer';
                download.innerHTML = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M10 2h4v4"/><path d="M14 2 7 9"/><path d="M12 9v4a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h4"/></svg>';
                download.title = 'Open link';
            }
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
    if (!isArtifactSource() || (source === 'artifacts' ? !sessionID : !team)) return;
    const scope = source;
    const cache = caches[scope];
    cancel(cache);
    const gen = cache.generation;
    cache.controller = new AbortController();
    const requestedTeam = team;
    // A standalone agent has no team board: its artifacts are the files it uploaded.
    const standalone = scope === 'artifacts' && !team;
    const requestedSession = sessionID;
    if (!append) { cache.items=[]; cache.hasMore=false; cache.truncated=false; cache.loaded=false; }
    cache.busy=true; cache.error=''; render();
    try {
        const params = new URLSearchParams({limit:'100',offset:String(cache.items.length)});
        if (scope === 'artifacts') params.set('session_id',requestedSession);
        const response = await (standalone
            ? fetch(serverUrl(`/api/session-artifacts?${params}`, sessionServer(state.currentSession)), {signal:cache.controller.signal})
            : boardFetch(team, `/artifacts?${params}`, {signal:cache.controller.signal}));
        if (!response.ok) throw new Error(`Unable to load ${scope === 'artifacts' ? 'agent' : 'team'} artifacts (${response.status}).`);
        const data = await response.json();
        if (gen !== cache.generation || caches[scope] !== cache || requestedTeam !== currentTeam() || source !== scope || (scope === 'artifacts' && requestedSession !== state.currentSession?.session_id)) return;
        if ((!standalone && data.project !== splitKey(requestedTeam).name) || !Array.isArray(data.artifacts) || (scope === 'artifacts' && data.session_id !== requestedSession)) throw new Error('Unable to load artifacts: unexpected response scope.');
        cache.items = append ? [...cache.items,...data.artifacts] : data.artifacts;
        cache.hasMore=!!data.has_more; cache.truncated=!!data.truncated; cache.loaded=true;
    } catch (error) {
        if (gen !== cache.generation || caches[scope] !== cache || error.name === 'AbortError') return;
        cache.error=error.message || 'Unable to load artifacts.';
    } finally {
        if (gen === cache.generation && caches[scope] === cache) { cache.busy=false; render(); }
    }
}

function activate() {
    if(source==='browse') showExplorer();
    else if(source==='knowledge') loadKnowledgeTab();
    else if(isArtifactSource()) {render(); if(!caches[source].loaded && !caches[source].busy)load();}
}

/** Called by switchAgenticTab: each source is a top-level sidebar tab. */
export function selectFilesSource(next) {
    if(!(next in panels) || source===next) return;
    if(isArtifactSource())cancel(caches[source]);
    source=next; activate();
}

export function initFilesSourcePicker(onPreview, onFilePreview) {
    previewArtifact=onPreview; previewFile=onFilePreview;
    mountExplorer(previewFile);
    syncFilesSourceTeam();render();activate();
}

export function syncFilesSourceTeam() {
    const next=currentTeam();
    const nextSession=state.currentSession?.type==='live'?state.currentSession.session_id:null;
    const teamChanged=next!==team;
    const agentChanged=nextSession!==sessionID;
    syncExplorerSession();
    if(!teamChanged&&!agentChanged)return;
    if(teamChanged) { clear('team-artifacts'); knowledgeLoaded=false; knowledgeLoading=false; }
    if(teamChanged||agentChanged)clear('artifacts');
    team=next;sessionID=nextSession;
    render();activate();
}

export function selectKnowledgeSource() { window.switchAgenticTab?.('knowledge', 'top'); }

// ── Knowledge tab ───────────────────────────────────────────────────────

let knowledgeLoaded = false;
let knowledgeLoading = false;
let knowledgeData = null;
let knowledgeMode = 'preview'; // 'preview' or 'edit'
let knowledgeActiveTab = 'index';
let knowledgeDirty = {};

async function loadKnowledgeTab() {
    const panel = document.getElementById('team-knowledge-view');
    if (!panel) return;
    const boardName = currentTeam();
    if (!boardName) {
        panel.innerHTML = '<div class="knowledge-empty">Select a team to view knowledge.</div>';
        return;
    }
    if (knowledgeLoaded) return;
    if (knowledgeLoading) return;
    knowledgeLoading = true;
    knowledgeMode = 'preview';
    knowledgeActiveTab = 'index';
    knowledgeDirty = {};

    panel.innerHTML = '<div class="knowledge-empty">Loading...</div>';

    const data = await teamFetch(boardName, n => `/api/teams/detail/${encodeURIComponent(n)}/knowledge`)
        .then(r => r.ok ? r.json() : null).catch(() => null);

    // If team changed while we were fetching, discard the result
    if (boardName !== currentTeam()) { knowledgeLoading = false; return; }

    knowledgeLoading = false;
    knowledgeLoaded = true;

    if (!data || !data.exists) {
        panel.innerHTML = `<div class="knowledge-empty">
            <div style="margin-bottom:12px">No distilled knowledge yet for this team.</div>
            <button class="btn btn-primary" onclick="distillTeamKnowledge('${escapeAttr(boardName)}')">Distill Knowledge</button>
        </div>`;
        return;
    }

    knowledgeData = data;
    renderKnowledgePanel(panel, boardName);
}

function navigateKnowledge(panel, tabName) {
    knowledgeActiveTab = tabName;
    panel.querySelectorAll('.knowledge-pane').forEach(p => p.classList.toggle('active', p.dataset.kpanel === tabName));
    const title = panel.querySelector('.knowledge-nav-title');
    const homeBtn = panel.querySelector('.knowledge-home-btn');
    if (tabName === 'index') {
        if (title) title.textContent = 'Overview';
        if (homeBtn) homeBtn.style.display = 'none';
    } else {
        if (title) title.textContent = tabName;
        if (homeBtn) homeBtn.style.display = '';
    }
}

function renderKnowledgePanel(panel, boardName) {
    const data = knowledgeData;
    const agents = data.agents || [];
    const agentNames = agents.map(a => a.name);

    let html = '<div class="knowledge-viewer">';

    // Single toolbar: Home + title on left, actions on right
    html += `<div class="knowledge-toolbar">
        <div class="knowledge-toolbar-left">
            <button class="inline-preview-mode-btn knowledge-home-btn" title="Back to overview" style="display:${knowledgeActiveTab === 'index' ? 'none' : ''}"><span class="material-icons">home</span></button>
            <span class="knowledge-nav-title">${knowledgeActiveTab === 'index' ? 'Overview' : escapeHtml(knowledgeActiveTab)}</span>
        </div>
        <div class="knowledge-toolbar-actions">
            <button class="inline-preview-mode-btn knowledge-redistill-btn" onclick="distillTeamKnowledge('${escapeAttr(boardName)}')" title="Re-distill"><span class="material-icons">refresh</span></button>
            <button class="inline-preview-mode-btn${knowledgeMode === 'preview' ? ' active' : ''}" data-kmode="preview" title="Preview"><span class="material-icons">visibility</span></button>
            <button class="inline-preview-mode-btn${knowledgeMode === 'edit' ? ' active' : ''}" data-kmode="edit" title="Edit"><span class="material-icons">edit</span></button>
            <button class="inline-preview-save knowledge-save-btn" style="display:${knowledgeMode === 'edit' ? '' : 'none'}">Save</button>
        </div>
    </div>`;

    // Content area
    html += '<div class="knowledge-content-area">';

    // Index/overview panel
    const indexContent = data.index || '*No index available*';
    html += `<div class="knowledge-pane${knowledgeActiveTab === 'index' ? ' active' : ''}" data-kpanel="index">`;
    html += `<div class="knowledge-preview">${renderKnowledgeMD(indexContent, agentNames)}</div>`;
    html += `<textarea class="knowledge-editor" style="display:none">${escapeHtml(indexContent)}</textarea>`;
    html += '</div>';

    // Agent panels
    for (const a of agents) {
        const content = a.content || '*No knowledge available*';
        html += `<div class="knowledge-pane${knowledgeActiveTab === a.name ? ' active' : ''}" data-kpanel="${escapeAttr(a.name)}">`;
        html += `<div class="knowledge-preview">${renderKnowledgeMD(content, agentNames)}</div>`;
        html += `<textarea class="knowledge-editor" style="display:none">${escapeHtml(content)}</textarea>`;
        html += '</div>';
    }

    html += '</div></div>';
    panel.innerHTML = html;

    applyKnowledgeMode(panel);

    // Home button
    panel.querySelector('.knowledge-home-btn').onclick = () => navigateKnowledge(panel, 'index');

    // Mode switching
    panel.querySelectorAll('[data-kmode]').forEach(btn => {
        btn.onclick = () => {
            knowledgeMode = btn.dataset.kmode;
            panel.querySelectorAll('[data-kmode]').forEach(b => b.classList.toggle('active', b.dataset.kmode === knowledgeMode));
            panel.querySelector('.knowledge-save-btn').style.display = knowledgeMode === 'edit' ? '' : 'none';
            applyKnowledgeMode(panel);
        };
    });

    // Save
    panel.querySelector('.knowledge-save-btn').onclick = async () => {
        const activePane = panel.querySelector(`.knowledge-pane[data-kpanel="${knowledgeActiveTab}"]`);
        const editor = activePane?.querySelector('.knowledge-editor');
        if (!editor || knowledgeActiveTab === 'index') return;
        const saveBtn = panel.querySelector('.knowledge-save-btn');
        saveBtn.textContent = 'Saving...'; saveBtn.disabled = true;
        try {
            const resp = await teamFetch(boardName, n => `/api/teams/detail/${encodeURIComponent(n)}/knowledge/${encodeURIComponent(knowledgeActiveTab)}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content: editor.value }),
            });
            const result = await resp.json();
            if (result.ok) {
                const agentEntry = data.agents.find(a => a.name === knowledgeActiveTab);
                if (agentEntry) agentEntry.content = editor.value;
                const preview = activePane.querySelector('.knowledge-preview');
                if (preview) preview.innerHTML = renderKnowledgeMD(editor.value, agentNames);
                saveBtn.textContent = 'Saved!';
                setTimeout(() => { saveBtn.textContent = 'Save'; }, 1500);
            } else {
                saveBtn.textContent = 'Error';
                setTimeout(() => { saveBtn.textContent = 'Save'; }, 2000);
            }
        } catch {
            saveBtn.textContent = 'Error';
            setTimeout(() => { saveBtn.textContent = 'Save'; }, 2000);
        } finally { saveBtn.disabled = false; }
    };

    // Inter-agent link navigation
    panel.addEventListener('click', (e) => {
        const link = e.target.closest('a[data-knowledge-link]');
        if (!link) return;
        e.preventDefault();
        const target = link.dataset.knowledgeLink;
        if (agentNames.includes(target)) navigateKnowledge(panel, target);
    });
}

function applyKnowledgeMode(panel) {
    panel.querySelectorAll('.knowledge-pane').forEach(pane => {
        const preview = pane.querySelector('.knowledge-preview');
        const editor = pane.querySelector('.knowledge-editor');
        if (preview) preview.style.display = knowledgeMode === 'preview' ? '' : 'none';
        if (editor) editor.style.display = knowledgeMode === 'edit' ? '' : 'none';
    });
}

function renderKnowledgeMD(md, agentNames = []) {
    const slugToName = {};
    for (const name of agentNames) {
        slugToName[name.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9_-]/g, '')] = name;
    }

    // Use marked.js if available for better rendering
    let text = md.replace(/^---[\s\S]*?^---\n*/m, '');

    // Rewrite internal links before marked processes them
    text = text.replace(/\[([^\]]+)\]\(\.\/([^)]+)\.md\)/g, (_, label, slug) => {
        const target = slugToName[slug];
        if (target) return `<a href="#" class="knowledge-link" data-knowledge-link="${escapeAttr(target)}">${escapeHtml(label)}</a>`;
        return `<span class="knowledge-link-dead">${escapeHtml(label)}</span>`;
    });

    if (typeof marked !== 'undefined') {
        const html = marked.parse(text);
        const sanitized = typeof DOMPurify !== 'undefined'
            ? DOMPurify.sanitize(html, { ADD_ATTR: ['data-knowledge-link'] })
            : html;
        return `<div class="knowledge-md notes-rendered">${sanitized}</div>`;
    }

    let html = text
        .replace(/^### (.+)$/gm, '<h4 class="knowledge-h4">$1</h4>')
        .replace(/^## (.+)$/gm, '<h3 class="knowledge-h3">$1</h3>')
        .replace(/^# (.+)$/gm, '<h2 class="knowledge-h2">$1</h2>')
        .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
        .replace(/`([^`]+)`/g, '<code class="knowledge-code">$1</code>')
        .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" class="knowledge-link-ext" target="_blank" rel="noopener">$1</a>')
        .replace(/^- (.+)$/gm, '<div class="knowledge-li">&#8226; $1</div>')
        .replace(/\n\n/g, '<div class="knowledge-spacer"></div>')
        .replace(/\n/g, '<br>');
    return `<div class="knowledge-md">${html}</div>`;
}

export function reloadKnowledgeTab() { knowledgeLoaded = false; knowledgeLoading = false; if (source === 'knowledge') loadKnowledgeTab(); }

export async function distillKnowledgeInTab(boardName) {
    selectSource('knowledge');
    const panel = document.getElementById('team-knowledge-view');
    if (!panel) return;
    knowledgeLoaded = true;

    panel.innerHTML = `<div class="knowledge-empty">
        <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" class="spin" style="margin-bottom:8px"><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>
        <div>Starting knowledge distillation...</div>
        <div style="font-size:12px;margin-top:4px">This reads all agent transcripts and runs 3 passes through Claude</div>
    </div>`;

    try {
        const resp = await teamFetch(boardName, n => `/api/teams/detail/${encodeURIComponent(n)}/distill-knowledge`, { method: 'POST' });
        if (!resp.ok) throw new Error('Failed to start distillation');

        const pollInterval = setInterval(async () => {
            try {
                const status = await teamFetch(boardName, n => `/api/teams/detail/${encodeURIComponent(n)}/distill-knowledge`).then(r => r.json());
                if (status.status === 'complete') {
                    clearInterval(pollInterval);
                    knowledgeLoaded = false;
                    loadKnowledgeTab();
                } else if (status.status === 'failed') {
                    clearInterval(pollInterval);
                    panel.innerHTML = `<div class="knowledge-empty" style="color:var(--error)">
                        Distillation failed: ${escapeHtml(status.error || 'Unknown error')}
                        <button class="btn btn-sm" style="margin-top:12px" onclick="distillTeamKnowledge('${escapeAttr(boardName)}')">Retry</button>
                    </div>`;
                } else if (status.progress) {
                    const p = status.progress;
                    const pct = p.total > 0 ? Math.round((p.completed / p.total) * 100) : 0;
                    let phaseLabel = p.phase;
                    if (p.phase === 'extracting') phaseLabel = 'Pass 1: Extracting knowledge';
                    else if (p.phase === 'cross-referencing') phaseLabel = 'Pass 2: Cross-referencing collaboration';
                    else if (p.phase === 'formatting') phaseLabel = 'Pass 3: Building OKF documents';
                    const agentLabel = p.agent ? ` — ${escapeHtml(p.agent)}` : '';
                    panel.innerHTML = `<div class="knowledge-empty">
                        <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" class="spin" style="margin-bottom:8px"><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>
                        <div>${phaseLabel}${agentLabel}</div>
                        <div style="margin-top:8px;width:200px;height:4px;background:var(--bg-tertiary);border-radius:2px;display:inline-block;overflow:hidden">
                            <div style="width:${pct}%;height:100%;background:var(--accent);border-radius:2px;transition:width 0.3s"></div>
                        </div>
                        <div style="font-size:12px;margin-top:4px">${p.completed}/${p.total} agents</div>
                    </div>`;
                }
            } catch { /* ignore poll errors */ }
        }, 2000);
    } catch (err) {
        panel.innerHTML = `<div class="knowledge-empty" style="color:var(--error)">${escapeHtml(err.message)}</div>`;
    }
}
