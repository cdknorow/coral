/* Lazy directory tree for the selected agent, rooted at the API-resolved repository or working directory. */
import { state } from './state.js';

let sessionKey = '';
let session = null;
let rootPath = '';
let generation = 0;
let directories = new Map();
let expanded = new Set();
let focused = null;
let preview = null;
const rowByPath = new Map();
let visibleRows = [];
let rowIndex = new Map();
let tabStop = null;
let mountedRoot = null;
let tree = null;
let rootLabel = null;
let refreshButton = null;
let collapseButton = null;

const element = (tag, cls, text) => {
    const e = document.createElement(tag); e.className = cls;
    if (text != null) e.textContent = text;
    return e;
};
const record = path => {
    if (!directories.has(path)) directories.set(path, { entries: [], loaded: false, loading: false, more: false, error: '', controller: null });
    return directories.get(path);
};

function reset() {
    ++generation;
    for (const dir of directories.values()) dir.controller?.abort();
    directories = new Map(); expanded = new Set(); focused = null;
    rowByPath.clear(); tabStop = null;
}

export function syncExplorerSession() {
    const next = state.currentSession?.type === 'live' ? state.currentSession : null;
    const key = next ? JSON.stringify([next.session_id, next.name, next.working_directory]) : '';
    if (key === sessionKey) return;
    reset(); sessionKey = key; session = next ? {...next} : null;
    rootPath = session?.working_directory || '';
    render();
}

export function mountExplorer(onPreview) {
    preview = onPreview; syncExplorerSession(); render();
}

export function showExplorer() {
    syncExplorerSession(); render();
    if (session && !record('.').loaded && !record('.').loading) load('.');
}

async function load(path, append = false) {
    const dir = record(path);
    if (!session || dir.loading) return;
    const gen = generation;
    dir.controller = new AbortController(); dir.loading = true; dir.error = ''; render();
    try {
        const params = new URLSearchParams({session_id:session.session_id, dir:path, view:'explorer', offset:String(append ? dir.entries.length : 0)});
        const response = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/search-files?${params}`, {signal:dir.controller.signal});
        if (!response.ok) throw new Error(`Could not read directory (${response.status}).`);
        const data = await response.json();
        if (gen !== generation) return;
        if (!Array.isArray(data.entries)) throw new Error('Could not read directory: unexpected response.');
        if (data.root) rootPath = data.root;
        dir.entries = append ? [...dir.entries, ...data.entries] : data.entries;
        dir.entries.sort((a,b) => (a.type === 'dir' ? 0 : 1) - (b.type === 'dir' ? 0 : 1) || a.name.localeCompare(b.name));
        dir.loaded = true; dir.more = !!data.has_more;
    } catch (error) {
        if (gen !== generation || error.name === 'AbortError') return;
        dir.error = error.message || 'Could not read directory.';
    } finally {
        if (gen === generation) { dir.loading = false; render(); }
    }
}

function setTabStop(row) {
    if (row) focused = row.dataset.path;
    if (tabStop === row) return;
    if (tabStop) tabStop.tabIndex = -1;
    tabStop = row;
    if (row) row.tabIndex = 0;
}

function select(path) {
    const row = rowByPath.get(path);
    if (row && rowIndex.has(row)) { setTabStop(row); row.focus(); }
}

function toggle(path) {
    focused = path;
    if (expanded.has(path)) { expanded.delete(path); render(); }
    else {
        expanded.add(path);
        // load renders the loading state synchronously; do not render it twice.
        if (!record(path).loaded && !record(path).loading) load(path);
        else render();
    }
    select(path);
}

function open(entry, path) {
    if (entry.type === 'dir') { toggle(path); return; }
    focused = path;
    // Explorer entries and file-content/edit/diff endpoints all use paths
    // relative to the same resolved repository root. rootPath is display-only.
    preview?.(path);
}

function renderDirectory(nodes, path, level) {
    const dir = record(path);
    for (const entry of dir.entries) {
        const child = entry.path || `${path === '.' ? '' : path + '/'}${entry.name.replace(/\/$/, '')}`;
        const isDir = entry.type === 'dir';
        let row = rowByPath.get(child);
        if (!row) {
            row = element('button','explorer-item');
            row.type = 'button'; row.dataset.path = child; row.dataset.parent = path; row.dataset.directory = String(isDir);
            row.setAttribute('role','treeitem'); row.setAttribute('aria-level',String(level));
            row.tabIndex = -1;
            row.style.paddingLeft = `${12 + (level - 1) * 16}px`;
            row.title = child;
            const icon = element('span','explorer-chevron',isDir ? '▸' : '·');
            icon.setAttribute('aria-hidden','true');
            row.append(icon,element('span','explorer-name',entry.name.replace(/\/$/, '')));
            row.addEventListener('focus',()=>setTabStop(row));
            row.addEventListener('click',()=>open(entry,child));
            rowByPath.set(child,row);
        }
        if (isDir && row.getAttribute('aria-expanded') !== String(expanded.has(child))) {
            row.setAttribute('aria-expanded',String(expanded.has(child)));
            row.firstChild.textContent = expanded.has(child) ? '▾' : '▸';
        }
        nodes.push(row); visibleRows.push(row);
        if (isDir && expanded.has(child)) renderDirectory(nodes,child,level+1);
    }
    const note = dir.loading ? 'Loading…' : dir.error || (dir.loaded && !dir.entries.length ? 'Empty directory' : '');
    if (note) {
        const message = element('div','explorer-status',note);
        message.style.paddingLeft = `${12 + (level - 1) * 16}px`;
        message.setAttribute('role',dir.error?'alert':'status'); nodes.push(message);
    }
    if (dir.error || dir.more) {
        const more = element('button','team-artifacts-action explorer-more',dir.error?'Retry':'Load more');
        more.type='button'; more.disabled=dir.loading;
        more.style.marginLeft=`${12 + (level - 1) * 16}px`;
        more.setAttribute('aria-label',`${dir.error?'Retry':'Load more'} ${path === '.' ? 'root directory' : path}`);
        more.addEventListener('click',()=>load(path,dir.entries.length>0));nodes.push(more);
    }
}

function handleKeydown(event) {
    const row=event.target.closest('.explorer-item');if(!row)return;
    const rows=visibleRows, index=rowIndex.get(row), path=row.dataset.path;
    if(event.key==='ArrowDown')select(rows[Math.min(index+1,rows.length-1)].dataset.path);
    else if(event.key==='ArrowUp')select(rows[Math.max(index-1,0)].dataset.path);
    else if(event.key==='Home')select(rows[0].dataset.path);
    else if(event.key==='End')select(rows[rows.length-1].dataset.path);
    else if(event.key==='ArrowRight' && row.dataset.directory==='true'){
        if(!expanded.has(path))toggle(path);else if(rows[index+1]?.dataset.parent===path)select(rows[index+1].dataset.path);
    } else if(event.key==='ArrowLeft'){
        if(expanded.has(path))toggle(path);else if(row.dataset.parent!=='.')select(row.dataset.parent);
    } else return;
    event.preventDefault();
}

function render() {
    const root = document.getElementById('file-explorer-view');
    if (!root) return;
    const hadRowFocus = root.contains(document.activeElement) && document.activeElement.classList.contains('explorer-item');
    if (mountedRoot !== root) {
        mountedRoot = root;
        const heading = element('div','explorer-heading');
        rootLabel = element('span','explorer-root');
        const actions=element('div','team-artifact-actions');
        refreshButton=element('button','team-artifacts-action','Refresh');refreshButton.type='button';
        refreshButton.addEventListener('click',()=>{reset();load('.');});
        collapseButton=element('button','team-artifacts-action','Collapse all');collapseButton.type='button';
        collapseButton.addEventListener('click',()=>{expanded.clear();focused=null;render();});
        actions.append(refreshButton,collapseButton);heading.append(rootLabel,actions);
        tree=element('div','explorer-tree');tree.setAttribute('role','tree');tree.setAttribute('aria-label','Working directory');
        tree.addEventListener('keydown',handleKeydown);
        root.replaceChildren(heading,tree);
    }
    const label = rootPath || 'Working directory';
    if (rootLabel.textContent !== label) {rootLabel.textContent=label;rootLabel.title=rootPath;}
    refreshButton.disabled=!session;collapseButton.disabled=!expanded.size;
    const nodes=[];
    visibleRows=[];
    if (session) renderDirectory(nodes,'.',1);
    else nodes.push(element('p','explorer-status','Select an agent to browse its working directory.'));
    rowIndex=new Map(visibleRows.map((row,index)=>[row,index]));
    const target=rowByPath.get(focused);
    setTabStop(rowIndex.has(target) ? target : visibleRows[0] || null);

    // Keep existing rows connected. Replacing the whole tree made a small
    // expansion rebuild thousands of buttons, listeners and layout objects.
    const wanted=new Set(nodes);
    for (const child of Array.from(tree.children)) if (!wanted.has(child)) child.remove();
    let cursor=tree.firstChild;
    for (const node of nodes) {
        if (node === cursor) cursor=cursor.nextSibling;
        else tree.insertBefore(node,cursor);
    }
    if(hadRowFocus && tabStop && document.activeElement !== tabStop) tabStop.focus();
}
