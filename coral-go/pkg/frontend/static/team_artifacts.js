/* Optional, lazy team artifact source for the existing Files viewer. */
import { state } from './state.js';
import { isOwnerLabelRefusal, OWNER_LABEL_TEXT } from './owner_label.js';
import { inlineArtifactPath } from './artifact_paths.js';
import { boardOfPage } from './dock_page.js';
import { escapeHtml, escapeAttr, sanitizeHtml, refusedTooLarge } from './utils.js';
import { registerActions, textOf } from './actions.js';
import { bindDownload } from './artifact_download.js';
import { readersAllowed, readersDisclosure } from './artifact_readers.js';
import { appendRows, FIRST_ROWS, setIcon } from './chunked_rows.js';
import { fileIconFor, mountExplorer, showExplorer, syncExplorerSession } from './file_explorer.js';
import { boardFetch, teamFetch, serverUrl, splitKey, sessionTeamKey, sessionServer, artifactPath, isCloud, keyLabel } from './server_base.js';
import { refreshDistill, startDistill, distillJob } from './knowledge_distill.js';
import { browserLocal } from './safe_storage.js';
import { buildBar } from './team_artifact_bar.js';
import { loadPanelOpen, savePanelOpen } from './list_bar.js';
import { EMPTY, isFiltered, loadFilters, saveFilters, toParams } from './team_artifact_filters.js';

let source = 'files';
let team = null;
let sessionID = null;
let previewArtifact = null;
let previewFile = null;
const fresh = () => ({items:[], loaded:false, busy:false, error:'', hasMore:false, truncated:false, cursor:'', controller:null, generation:0, counts:null});
const caches = {artifacts:fresh(), 'team-artifacts':fresh()};
let notice = ''; // the outcome of the last delete (#3553), shown once under the heading

// The search box, filters and sort of the Team Artifacts list (#4573) and, the same component, of one agent's Artifacts tab (#5283): asked of the server (a search covers every artifact, not just the
// loaded page), kept per board and, for an agent, per agent. Each panel has its own bar (built once: the list is rebuilt on every change, which would drop the keyboard focus out of a search box).
const views = { artifacts: { filters: { ...EMPTY }, key: null, bar: null, panelKey: null }, 'team-artifacts': { filters: { ...EMPTY }, key: null, bar: null, panelKey: null } };
const view = () => views[source];
/** What the filters are kept under: the board, or for an agent's tab the board and the agent. null when there is nothing to filter (no team: a standalone agent's list has no search). */
function filtersKey() { return source === 'artifacts' ? (team && sessionID ? `${team}::agent::${sessionID}` : null) : team; }
function syncFilters() {
    const v = view(), key = filtersKey();
    if (key === v.key) return;
    v.key = key;
    v.filters = loadFilters(key, browserLocal);
    caches[source].counts = null;
}
/** The list's totals from the reply: total (after the search and filters) and all (the whole list before them: "12 of 340"). */
function noteCounts(data, cache) {
    const matched = Number.isSafeInteger(data.total) ? data.total : cache.items.length;
    cache.counts = { matched, all: Number.isSafeInteger(data.all) ? Math.max(data.all, matched) : matched, truncated: !!data.truncated };
}
function setFilters(scope, next) {
    views[scope].filters = next;
    saveFilters(views[scope].key, next, browserLocal);
    if (source === scope && filtersKey()) load(false);
}

// ── Delete (#3553): the Operator removes a stored artifact ──────────────────
// Only on this computer's own dashboard (not through Coral Hub, not in the pop-out), and only for
// an artifact stored on this Coral (a remote's goes through the hub, which never deletes).
function canDelete(link) {
    return !!link && link.preview && /^\/api\/artifacts\/[a-f0-9]{64}/.test(link.url)
        && !isCloud() && !document.body.classList.contains('popout-mode');
}

// ── Keep (#3554): an artifact the Operator keeps is never pruned ────────────
let kept = null; // Set of kept ids, read once per render pass
async function loadKept() {
    if (isCloud() || document.body.classList.contains('popout-mode')) return new Set();
    try {
        const r = await fetch('/api/system/artifact-store/kept');
        return new Set(r.ok ? (await r.json()).kept || [] : []);
    } catch { return new Set(); }
}
async function toggleKeep(hex, name, btn) {
    const on = btn.getAttribute('aria-pressed') !== 'true';
    const r = await fetch(`/api/artifacts/${hex}/keep`, { method: on ? 'PUT' : 'DELETE' }).catch(() => null);
    if (!r?.ok) { notice = `Couldn't ${on ? 'keep' : 'release'} ${name}.`; render(); return; }
    if (on) kept?.add(hex); else kept?.delete(hex);
    btn.setAttribute('aria-pressed', String(on));
    btn.title = on ? 'Kept: never pruned (click to release)' : 'Keep: never prune this';
}

// Other tabs of this dashboard drop a row deleted here.
const deletions = typeof BroadcastChannel === 'function' ? new BroadcastChannel('coral-artifact-deletions') : null;
deletions?.addEventListener('message', e => { if (/^[a-f0-9]{64}$/.test(String(e.data))) forget(e.data); });

function forget(hex) {
    for (const c of Object.values(caches)) {
        c.items = c.items.filter(it => !String(it.uri || '').toLowerCase().includes(hex));
    }
    if (isArtifactSource()) render();
}

// After a delete the row the focus was on is gone: the focus goes to the same place in the list (the next row, else the one before), else to Refresh, never to the page (#4469).
let afterDeleteRow = null;
function activeRowIndex() {
    const row = document.activeElement?.closest?.('tr.team-artifact-row');
    return row ? [...row.parentElement.children].indexOf(row) : -1;
}

function deleteArtifact(hex, name, size) {
    const wasRow = activeRowIndex();
    window.showConfirmModal?.('Delete artifact',
        `Delete ${name}${size ? ` (${size})` : ''}? It is removed from Coral, and links to it in board posts and task results will show "unavailable". A copy a browser already cached may remain. This can't be undone.`,
        async () => {
            try {
                const r = await fetch(`/api/artifacts/${hex}`, { method: 'DELETE' });
                if (r.ok || r.status === 404) {
                    notice = r.ok ? `Deleted ${name}.` : `${name} was already gone.`;
                    afterDeleteRow = wasRow;
                    forget(hex);
                    deletions?.postMessage(hex);
                } else {
                    const d = await r.json().catch(() => ({}));
                    notice = `Couldn't delete ${name}: ${d.error || `HTTP ${r.status}`}`;
                    render();
                }
            } catch {
                notice = `Couldn't delete ${name}: Coral didn't answer.`;
                render();
            }
        });
    // No default: focus starts on Cancel.
    requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
}
const isArtifactSource = () => source === 'artifacts' || source === 'team-artifacts';
const panels = {files:'agentic-panel-files', browse:'agentic-panel-browse', artifacts:'agentic-panel-artifacts', 'team-artifacts':'agentic-panel-team-artifacts', knowledge:'agentic-panel-knowledge'};
function cancel(cache) { cache.controller?.abort(); ++cache.generation; cache.busy=false; }
function clear(scope) { cancel(caches[scope]); caches[scope]=fresh(); }

function currentTeam() {
    // A board's page shows that board's artifacts (#4539).
    const pageBoard = boardOfPage();
    if (pageBoard) return pageBoard;
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
    if (item.task_title) return `${item.task_title} — artifact`;
    return item.task_id ? `Task #${item.task_id} artifact` : 'Uploaded artifact';
}

// What a row says about where it came from, and whether Coral checked the link (#4396). A row whose provenance is "unverified" cites a Coral artifact
// this team has no upload of: it has no size, type, preview or download here, and it says so. A shared row is a file this team sent; a received one
// came from another computer. The words are the server's display text, never markup.
function isUnverified(item) { return item.provenance === 'unverified'; }
function originBadge(item) {
    if (isUnverified(item)) return { kind: 'unverified', text: 'Link not verified' };
    if (item.provenance === 'received' && item.source !== 'received') return { kind: 'received', text: 'Received file' }; // a task result that cites a file this board received
    if (item.source === 'shared') return { kind: 'shared', text: `Sent to ${item.shared_to || 'another team'}` };
    if (item.source === 'received') return { kind: 'received', text: `From ${item.received_from || 'another computer'}` };
    return null;
}

function artifactLink(item) {
    if (item.available === false || isUnverified(item)) return null;
    const managed = /^(?:coral:\/\/artifacts\/|\/api\/artifacts\/)([a-f0-9]{64})(?:\?v=\d+)?$/i.exec(item.uri || '');
    if (managed) return { url: serverUrl(artifactPath(managed[1]), splitKey(team).server), preview: true };
    const inline = item.inline ? inlineArtifactPath(item.content_url, splitKey(team).name) : null;
    if (inline) {
        return { url: serverUrl(inline, splitKey(team).server), preview: true };
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
const AGENT_ARTIFACT_PROMPT = `To publish an artifact in Coral (so it shows in the Artifacts tab): run coral-agent artifact upload <file> (prints JSON whose "uri" is the coral://artifacts/<digest> reference).
It appears in the Artifacts tab right away. A team agent's upload also appears in Team Artifacts.
Local paths like /tmp/<file> are not reachable by the user, so always upload. Full docs: artifacts.md in the Coral agent docs.`;

const RELOAD_ICON = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M13.5 8a5.5 5.5 0 1 1-1.7-3.97"/><path d="M13.5 2.5v3h-3"/></svg>';
const INFO_ICON = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="8" r="6"/><path d="M8 7.2V11"/><path d="M8 5h.01"/></svg>';

const COPY_ICONS = {
    copy: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6.5 9.5a3 3 0 0 0 4.2 0l2.3-2.3a3 3 0 0 0-4.2-4.2l-.8.8"/><path d="M9.5 6.5a3 3 0 0 0-4.2 0L3 8.8a3 3 0 0 0 4.2 4.2l.8-.8"/></svg>',
    done: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m3 8.5 3 3 7-7"/></svg>',
    failed: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6"/><path d="M8 5v3.5M8 11h0"/></svg>',
};

// Copies the artifact's absolute URL, with the header copy button's check /
// error icon feedback.
function copyLinkButton(url, name) {
    const button = el('button', 'team-artifacts-action team-artifact-icon-btn team-artifact-copy-link');
    button.type = 'button';
    setIcon(button, COPY_ICONS.copy);
    button.title = 'Copy link';
    button.setAttribute('aria-label', `Copy link to ${name}`);
    button.addEventListener('click', async () => {
        try {
            await navigator.clipboard.writeText(new URL(url, location.href).href);
            setIcon(button, COPY_ICONS.done);
        } catch { setIcon(button, COPY_ICONS.failed); }
        setTimeout(() => { setIcon(button, COPY_ICONS.copy); }, 1500);
    });
    return button;
}

// The panel is rebuilt on every change, which dropped the keyboard focus (Refresh, Retry, Load more). Remember what had it (its row and its
// name) across the rebuilds of one load: while a control is disabled (Refresh during a load) the focus waits until it is back.
let focusKey = null;
function focusKeyOf(root) {
    const a = document.activeElement;
    if (!a || a === root || !root.contains(a) || a.tagName === 'BODY') return null;
    if (a.classList.contains('team-artifacts-more')) return { more: true, count: caches[source].items.length };   // Load more: the focus follows to the first new row
    const row = a.closest('tr.team-artifact-row');
    return { row: row ? [...row.parentElement.children].indexOf(row) : -1, label: (a.getAttribute('aria-label') || a.textContent || '').trim() };
}
let afterRetry = false;
function focusAfterAction(root) {
    const rows = [...root.querySelectorAll('tr.team-artifact-row')];
    const refresh = root.querySelector('.team-artifacts-refresh');
    if (afterDeleteRow !== null) {
        const at = afterDeleteRow; afterDeleteRow = null;
        const target = at >= 0 ? (rows[at] || rows[at - 1] || null) : null;
        (target?.querySelector('button, a[href], summary') || refresh)?.focus();
        focusKey = null;
        return true;
    }
    if (afterRetry && !caches[source].busy) {
        afterRetry = false;
        // A retry that worked: the list is new, start at its first control; one that failed again: back on Retry.
        (caches[source].error ? root.querySelector('.team-artifacts-retry') : (rows[0]?.querySelector('button, a[href], summary') || refresh))?.focus();
        focusKey = null;
        return true;
    }
    return false;
}

function restoreFocus(root) {
    if (focusAfterAction(root)) return;
    if (!focusKey) return;
    if (focusKey.more) {
        const more = root.querySelector('.team-artifacts-more');
        if (more) { if (!more.disabled) { more.focus(); focusKey = null; } return; }   // still loading: wait
        const first = root.querySelectorAll('tr.team-artifact-row')[focusKey.count]?.querySelector('button, a[href], summary');
        focusKey = null;
        if (first) first.focus();
        return;
    }
    const pool = focusKey.row >= 0 ? (root.querySelectorAll('tr.team-artifact-row')[focusKey.row]?.querySelectorAll('button, a[href], summary') || []) : root.querySelectorAll('.team-artifacts-heading button, .list-bar-tail button, .team-artifacts-status ~ button, .team-artifacts-more');
    const hit = [...pool].find(e => (e.getAttribute('aria-label') || e.textContent || '').trim() === focusKey.label);
    if (!hit) { focusKey = null; return; }   // the row or the control is gone
    if (hit.disabled) return;                 // back when it is enabled again
    hit.focus(); focusKey = null;
}

/** The panel's heading: its title, Refresh, and the button that copies how an agent publishes an artifact. With a search bar they go on the bar's one row (the title before the box, the two
 *  icons after the filter icons, #5315); without one (a standalone agent's list) they are a heading of their own. */
function buildHeading({ personal, scopeName, busy }, bar) {
    const heading = el('div', 'team-artifacts-heading');
    const title = el('span', 'team-artifacts-title', (personal || team) ? `${personal ? 'Artifacts' : 'Team Artifacts'} · ${scopeName}` : personal ? 'Artifacts' : 'Team Artifacts');
    title.title = title.textContent;
    heading.append(title);
    const refresh = el('button', 'team-artifacts-action team-artifact-icon-btn team-artifacts-refresh');
    refresh.type = 'button'; refresh.disabled = personal ? !sessionID : !team;
    refresh.innerHTML = RELOAD_ICON;
    refresh.title = 'Refresh'; refresh.setAttribute('aria-label', 'Refresh');
    if (busy) refresh.setAttribute('aria-disabled', 'true'); // not disabled: the focus stays on it while the list loads
    refresh.addEventListener('click', () => { if (!caches[source].busy) load(false, true); });
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
    if (bar) { bar.setHeader(title, [refresh, help]); return null; }
    heading.append(refresh, help);
    return heading;
}

/** The line that says how the last delete went (the eye's copy; a screen reader gets it from the live region). */
function buildNotice(text) {
    const n = el('div', 'team-artifacts-status team-artifacts-notice', text);
    n.setAttribute('aria-hidden', 'true');
    return n;
}

/** The status line: what to select, the load's error, "Loading…", the count or why the list is empty. */
function buildStatus({ personal, items, busy, error, truncated }) {
    const status = el('div', 'team-artifacts-status');
    status.setAttribute('role', error ? 'alert' : 'status');
    status.textContent = (personal ? !sessionID : !team) ? (personal ? 'Select an agent to browse its artifacts.' : 'Select an agent on a team to browse its artifacts.')
        : error ? error : busy ? (personal ? 'Loading agent artifacts…' : 'Loading team artifacts…')
        : !items.length ? (isFiltered(view().filters) ? 'No artifacts match this search.' : personal ? 'No artifacts yet.' : 'No artifacts yet.') : (caches[source].counts && caches[source].counts.matched > items.length) ? `Showing ${items.length} of ${caches[source].counts.matched}${truncated ? '+' : ''}` : `${items.length} artifact${items.length === 1 ? '' : 's'}`;
    if (error) status.dataset.state = 'error';
    return status;
}

/** Retry, after a failed load. */
function buildRetry(items) {
    const retry = el('button', 'team-artifacts-action team-artifacts-retry', 'Retry');
    retry.type = 'button'; retry.addEventListener('click', () => { afterRetry = true; load(items.length > 0 && !!caches[source].askedMore, true); }); // what failed is asked again: a next page appends, a first page (a Refresh that kept its rows, #6509) replaces them
    return retry;
}

/** Load more, while the server has more. */
function buildMore(busy) {
    const more = el('button', 'team-artifacts-action team-artifacts-more', busy ? 'Loading…' : 'Load more');
    more.type = 'button'; more.disabled = busy;
    more.addEventListener('click', () => load(true));
    return more;
}

const rowKey = item => `${item.source || ''}|${item.uri || ''}|${item.name || ''}`;
/** The second icon of the bar: open every row's details, or close them. */
function setAllDetails(open) {
    for (const item of caches[source].items) { if (open) openDetails.add(rowKey(item)); else openDetails.delete(rowKey(item)); }
    render();
}
function syncExpandAll() {
    const v = view(), items = caches[source].items;
    if (v.bar) v.bar.setExpandNext(items.length && items.every(i => openDetails.has(rowKey(i))) ? 'collapse' : 'expand');
}
let detailSeq = 0; // ids for the details blocks, unique across rebuilds
const openDetails = new Set(); // the rows whose details are open

/** One artifact's row: its name, type, created time and actions (the first of them opens its details). */
function buildRow(item) {
    const row = el('tr', 'team-artifact-row');
    const unverified = isUnverified(item);
    const badge = originBadge(item);
    const info = el('td', 'team-artifact-info team-artifact-col-name');
    const name = readableName(item);
    const link = artifactLink(item);
    const doPreview = () => {
        if (!link || !previewArtifact) return;
        const managed = /^(?:coral:\/\/artifacts\/|\/api\/artifacts\/)([a-f0-9]{64})(?:\?v=\d+)?$/i.exec(item.uri || '');
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
    title.prepend(fileIconFor(name, item.media_type)); // the Browse tab's icon for this name, or for the media type when the name has no extension (#5314)
    title.title = name;
    info.append(title);
    if (badge) {
        const chip = el('span', `team-artifact-badge team-artifact-badge-${badge.kind}`, badge.text);
        chip.title = badge.text; // one line with an ellipsis; the whole text is here and in Details
        info.append(chip);
        row.dataset.origin = badge.kind;
    }
    const date = item.created_at ? new Date(item.created_at) : null;
    const created = date && !Number.isNaN(date.getTime()) ? date.toLocaleString() : '—';
    // An artifact uploaded outside a task says so (#3045).
    const task = item.task_id ? `#${item.task_id}` : 'No task';
    const type = unverified ? '—' : artifactType(item);
    const size = unverified ? '—' : sizeLabel(item.size) || '—';
    // The details are a block under the name that the (i) button in the Actions column opens (#5283); which rows are open is kept across the rebuilds the panel does on every change.
    const openKey = rowKey(item);
    const details = el('div', 'team-artifact-details');
    details.id = `team-artifact-details-${++detailSeq}`;
    details.hidden = !openDetails.has(openKey);
    const fields = el('dl');
    for (const [label, value] of [
        ['Name', name], ['Type', unverified ? null : type], ['MIME', unverified ? null : item.media_type || 'Unknown'],
        ['Size', unverified ? null : size], ['Task', task], ['Task title', item.task_title],
        ['Created', created], ['Producer', item.produced_by],
        ['Status', unverified ? 'Link not verified: this team has not uploaded this file, so Coral shows no size, type or preview' : null],
        ['Sent to', item.source === 'shared' ? item.shared_to : null],
        ['Sent by', item.source === 'shared' || item.source === 'received' ? item.sent_by : null],
        ['Received from', item.source === 'received' ? item.received_from : null],
        ['Board (claimed by the sender)', item.source === 'received' ? item.from_board : null],
        ['Also received from', item.source === 'received' ? alsoReceivedLines(item.also_received_from) : null],
        ['Checked', item.source === 'received' ? 'Not scanned: this file came from another computer' : null],
        ['Source', item.source], ['References', item.reference_count],
    ]) {
        if (Array.isArray(value)) {
            // One line per source: server names hold spaces, brackets and hyphens, so a comma list is not readable.
            if (value.length) fields.append(el('dt', '', label), ...value.map(v => el('dd', '', v)));
        } else if (value != null && value !== '') fields.append(el('dt', '', label), el('dd', '', String(value)));
    }
    details.append(fields);
    // "Who can read this", in the row's details, for the Operator (the owner through Coral Hub): asked when the details are open (a listing makes no extra request), drawn once the machine has said this person may see it.
    const managed = /^(?:coral:\/\/artifacts\/|\/api\/artifacts\/)([a-f0-9]{64})(?:\?v=\d+)?$/i.exec(item.uri || '');
    let readersAsked = false;
    const addReaders = () => {
        if (readersAsked || !managed || !link) return;
        readersAsked = true;
        const hex = managed[1].toLowerCase(), srv = splitKey(team).server;
        readersAllowed(hex, srv).then(ok => { if (ok) details.append(readersDisclosure(hex, srv)); }).catch(() => {});
    };
    if (!details.hidden) addReaders();
    info.append(details);row.append(info);
    row.append(el('td', 'team-artifact-col-type', type), el('td', 'team-artifact-col-created', created));
    const actionCell = el('td', 'team-artifact-col-actions');row.append(actionCell);
    const actions = el('div', 'team-artifact-actions');
    const infoBtn = el('button', 'team-artifacts-action team-artifact-icon-btn team-artifact-info-btn');
    infoBtn.type = 'button'; setIcon(infoBtn, INFO_ICON);
    infoBtn.title = 'Details'; infoBtn.setAttribute('aria-label', `Details for ${name}`);
    infoBtn.setAttribute('aria-controls', details.id); infoBtn.setAttribute('aria-expanded', String(!details.hidden));
    infoBtn.addEventListener('click', () => {
        details.hidden = !details.hidden;
        if (details.hidden) openDetails.delete(openKey); else openDetails.add(openKey);
        infoBtn.setAttribute('aria-expanded', String(!details.hidden));
        if (!details.hidden) addReaders();
        syncExpandAll();
    });
    actions.append(infoBtn);
    if (link) {
        const preview = el('button', 'team-artifacts-action team-artifact-icon-btn team-artifact-preview');
        preview.type = 'button';
        setIcon(preview, '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 8s2.5-5 7-5 7 5 7 5-2.5 5-7 5-7-5-7-5z"/><circle cx="8" cy="8" r="2.5"/></svg>');
        preview.title = 'Preview';
        preview.setAttribute('aria-label', `Preview ${name}`);
        preview.addEventListener('click', doPreview);
        actions.append(preview);
        const download = el('a', 'team-artifacts-action team-artifact-icon-btn');
        download.href = link.url;
        if (link.preview) {
            download.download = name;
            setIcon(download, '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 10v3h12v-3"/><path d="M8 2v8m-3-3 3 3 3-3"/></svg>');
            download.title = 'Download';
        } else {
            download.target = '_blank'; download.rel = 'noopener noreferrer';
            setIcon(download, '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M10 2h4v4"/><path d="M14 2 7 9"/><path d="M12 9v4a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h4"/></svg>');
            download.title = 'Open link';
        }
        if (link.preview) bindDownload(download, link.url, name);
        download.setAttribute('aria-label', `${link.preview ? 'Download' : 'Open'} ${name}`);
        actions.append(download, copyLinkButton(link.url, name));
        if (canDelete(link)) {
            const hex = /([a-f0-9]{64})/.exec(link.url)[1];
            const del = el('button', 'team-artifacts-action team-artifact-icon-btn team-artifact-delete');
            del.type = 'button';
            setIcon(del, '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 4h11M6 4V2.5h4V4M4 4l.7 9.5h6.6L12 4"/></svg>');
            del.title = 'Delete';
            del.setAttribute('aria-label', `Delete ${name}`);
            del.addEventListener('click', () => deleteArtifact(hex, name, sizeLabel(item.size)));
            const keep = el('button', 'team-artifacts-action team-artifact-icon-btn team-artifact-keep');
            keep.type = 'button';
            setIcon(keep, '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 2.5h8v11L8 10.5l-4 3z"/></svg>');
            const isKept = !!kept?.has(hex);
            keep.setAttribute('aria-pressed', String(isKept));
            keep.setAttribute('aria-label', `Keep ${name}`);
            keep.title = isKept ? 'Kept: never pruned (click to release)' : 'Keep: never prune this';
            keep.addEventListener('click', () => toggleKeep(hex, name, keep));
            actions.append(keep, del);
        }
    } else actions.append(el('span', 'team-artifact-missing', unverified ? 'Not verified' : item.available === false ? 'Missing' : 'Unavailable'));
    actionCell.append(actions);
    return row;
}

const scrollSeen = new WeakMap(); // panel root -> its scrollTop as of the last scroll event

/** The table of artifacts, in a scrolling box. */
function buildList(items, personal, scrolled = false) {
    // The table fits its panel at any width: Size and Task live in each row's
    // Details, and a narrow panel drops Type and Created (also in Details).
    const scroll = el('div', 'team-artifacts-scroll');
    const list = el('table', 'team-artifacts-list');
    const caption = el('caption', 'team-artifacts-caption', personal ? 'Agent artifacts' : 'Team artifacts');
    list.append(caption);
    const head = el('thead');
    const headers = el('tr');
    for (const label of ['Name', 'Type', 'Created', 'Actions']) {
        const cell = el('th', `team-artifact-col-${label.toLowerCase()}`, label);
        cell.scope = 'col';headers.append(cell);
    }
    head.append(headers);list.append(head);
    const rows = el('tbody');list.append(rows);
    // A long list draws its first rows at once and the rest a few frames later (chunked_rows.js, #5329). One the person is mid-action in (focus on a row far down, a delete or a retry just done) or has
    // scrolled is drawn whole, so what the focus goes back to is there and the scroll position is kept.
    appendRows(rows, items, buildRow, { whole: scrolled || !!(focusKey && focusKey.row >= FIRST_ROWS) || afterDeleteRow !== null || afterRetry });
    scroll.append(list);
    return scroll;
}

function render() {
    const root = document.getElementById(`${panels[source]}-view`);
    if (!root || !isArtifactSource()) return;
    { const k = focusKeyOf(root); if (k) focusKey = k; else if (document.activeElement && document.activeElement !== document.body && !root.contains(document.activeElement)) focusKey = null; }
    const {items, busy, error, hasMore, truncated} = caches[source];
    const personal = source === 'artifacts';
    const scopeName = personal ? state.currentSession?.display_name || state.currentSession?.name || 'Selected agent' : team && keyLabel(team); // a board's name, never its id
    // Everything but the search bar is rebuilt: the bar keeps what is typed in it and its focus (the team list and an agent's list on a team have one; a standalone agent's has none).
    syncFilters();
    const showBar = !!filtersKey();
    const v = view(), scope = source;
    if (!v.bar) v.bar = buildBar(next => setFilters(scope, next), { personal, hooks: { onPanel: open => savePanelOpen(scope, views[scope].key, open), onExpandAll: next => setAllDetails(next === 'expand') } });
    const bar = v.bar, filters = v.filters, counts = caches[source].counts;
    if (v.panelKey !== v.key) { v.panelKey = v.key; bar.setOpen(loadPanelOpen(scope, v.key)); } // the filter panel is left as it was for this board or agent (#5293)
    // Where the box is scrolled, as the last scroll event said: a list drawn in chunks over a scrolled box would shorten it under the scroll position and the browser would clamp it to the top (QA, #5331), so a
    // scrolled list is drawn whole. (Reading scrollTop here would itself force the layout that clamps it, in the middle of a redraw.)
    if (!scrollSeen.has(root)) { scrollSeen.set(root, 0); root.addEventListener('scroll', () => scrollSeen.set(root, root.scrollTop), { passive: true }); }
    const scrolled = scrollSeen.get(root) > 0;
    for (const c of [...root.children]) if (c !== bar.node) c.remove();
    if (showBar) { if (bar.node.parentElement !== root) root.append(bar.node); } else bar.node.remove();
    const before = node => (showBar ? root.insertBefore(node, bar.node) : root.append(node));
    root.setAttribute('aria-busy', String(busy));
    const heading = buildHeading({ personal, scopeName, busy }, showBar ? bar : null);
    if (heading) before(heading);
    if (notice) {
        before(buildNotice(notice));
        // The visible line is for the eye; screen readers get it from the live region that is always in the page (a region that appears with its text is often missed).
        const live = document.getElementById(`${panels[source]}-live`);
        if (live) live.textContent = notice;
        notice = '';
    }
    const status = buildStatus({ personal, items, busy, error, truncated });
    root.append(status);
    if (error) root.append(buildRetry(items));
    if (items.length) root.append(buildList(items, personal, scrolled)); // no header over nothing: an empty list or a failed load shows its line only
    if (hasMore) root.append(buildMore(busy));
    if (truncated) root.append(el('div', 'team-artifacts-status', 'Some older artifacts are not included in this list.'));
    if (showBar) {
        syncExpandAll();
        bar.names([...new Set(items.flatMap(i => [i.produced_by, i.sent_by]).filter(n => typeof n === 'string' && n))].sort().slice(0, 50)); // the names on screen, offered in the Uploaded by field
        bar.set(filters, counts ? counts : items.length || !busy ? { matched: items.length, all: items.length } : null);
    }
    restoreFocus(root);
}

// keep: the same list asked for again (Refresh): what is on screen stays until the new answer is here. Emptying it first shrank the scrolling box, and the browser put the person back at the top as soon as a frame
// passed while the answer was on its way (#6509); a list of another board, agent or search is emptied (the old rows are not its rows).
async function load(append = false, keep = false) {
    if (!isArtifactSource() || (source === 'artifacts' ? !sessionID : !team)) return;
    const scope = source;
    syncFilters(); // this board's remembered search and filters, before the first ask
    const cache = caches[scope];
    cancel(cache);
    const gen = cache.generation;
    cache.controller = new AbortController();
    const requestedTeam = team;
    // A standalone agent has no team board: its artifacts are the files it uploaded.
    const standalone = scope === 'artifacts' && !team;
    const requestedSession = sessionID;
    if (!append) { if (!keep || !cache.loaded) { cache.items=[]; cache.hasMore=false; cache.truncated=false; cache.loaded=false; } cache.cursor=''; }
    const from = append ? cache.items.length : 0; // where this answer starts: the first page, or after what is shown
    cache.askedMore = append; // for Retry: a failed next page is asked again as a next page, a failed first page as the first
    cache.busy=true; cache.error=''; render();
    try {
        const params = new URLSearchParams({limit:'100'});
        if (scope === 'artifacts' && standalone) { params.set('offset',String(from)); params.set('session_id',requestedSession); }
        else if (!standalone) {
            if (scope === 'artifacts') params.set('session_id', requestedSession); // one agent's list: the same query, scoped to the agent (the totals are the agent's)
            // The search, the filters and the sort are the server's to apply; the next page is asked for by the cursor the last one gave (not by offset).
            for (const [k, v] of Object.entries(toParams(views[scope].filters))) params.set(k, v);
            if (!append) params.set('offset', '0');
            else if (cache.cursor) params.set('cursor', cache.cursor);
            else params.set('offset', String(from)); // a reply with no cursor is paged by offset
        } else params.set('offset',String(from));
        const response = await (standalone
            ? fetch(serverUrl(`/api/session-artifacts?${params}`, sessionServer(state.currentSession)), {signal:cache.controller.signal})
            : boardFetch(team, `/artifacts?${params}`, {signal:cache.controller.signal}));
        if (!response.ok) {
            if (await isOwnerLabelRefusal(response)) throw new Error(OWNER_LABEL_TEXT); // linked before it recorded its owner (#6108)
            throw new Error(`Unable to load ${scope === 'artifacts' ? 'agent' : 'team'} artifacts (${response.status}).`);
        }
        const data = await response.json();
        if (gen !== cache.generation || caches[scope] !== cache || requestedTeam !== currentTeam() || source !== scope || (scope === 'artifacts' && requestedSession !== state.currentSession?.session_id)) return;
        if ((!standalone && data.project !== splitKey(requestedTeam).name) || !Array.isArray(data.artifacts) || (scope === 'artifacts' && data.session_id !== requestedSession)) throw new Error('Unable to load artifacts: unexpected response scope.');
        cache.items = append ? [...cache.items,...data.artifacts] : data.artifacts;
        cache.hasMore=!!data.has_more; cache.truncated=!!data.truncated; cache.loaded=true;
        cache.cursor = typeof data.next_cursor === 'string' ? data.next_cursor : '';
        if (!standalone) noteCounts(data, cache);
        if (!append) kept = await loadKept(); // the Keep toggles' state (#3554)
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

// alsoReceivedLines: the later senders of a received file ("<server>/<board>" each), worded as the first
// source's two fields are: the server, then the board with the caveat that it is only the sender's claim.
function alsoReceivedLines(list) {
    if (!Array.isArray(list)) return null;
    return list.map(v => {
        const at = String(v).indexOf('/');
        const server = at < 0 ? String(v) : String(v).slice(0, at);
        const board = at < 0 ? '' : String(v).slice(at + 1);
        return board ? `${server}, board ${board} (claimed by the sender)` : server;
    });
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

 function selectKnowledgeSource() { window.switchAgenticTab?.('knowledge', 'top'); }

// ── Knowledge tab ───────────────────────────────────────────────────────

let knowledgeLoaded = false;
let knowledgeLoading = false;
let knowledgeData = null;
let knowledgeMode = 'preview'; // 'preview' or 'edit'
let knowledgeActiveTab = 'index';

async function loadKnowledgeTab() {
    const panel = document.getElementById('team-knowledge-view');
    if (!panel) return;
    const boardName = currentTeam();
    if (!boardName) {
        panel.innerHTML = '<div class="knowledge-empty">Select a team to view knowledge.</div>';
        return;
    }
    // Loaded already, unless the panel was re-rendered empty meanwhile (#3325).
    if (knowledgeLoaded && panel.childElementCount > 0) return;
    if (knowledgeLoading) return;
    knowledgeLoading = true;
    knowledgeMode = 'preview';
    knowledgeActiveTab = 'index';

    panel.innerHTML = '<div class="knowledge-empty">Loading...</div>';

    // A run in progress (started here earlier, in another window, or before a reload) is
    // shown and followed; the bundle is loaded once there is none (#3325).
    const job = await refreshDistill(boardName);
    if (boardName !== currentTeam()) { knowledgeLoading = false; return; }
    if (job.status === 'running') {
        knowledgeLoading = false;
        knowledgeLoaded = true;
        renderDistillProgress(panel, job);
        return;
    }

    let refused = false; // too large to check through Coral Hub (#3718): said, not "no knowledge yet"
    const data = await teamFetch(boardName, n => `/api/teams/detail/${encodeURIComponent(n)}/knowledge`)
        .then(async r => { if (r.ok) return r.json(); refused = await refusedTooLarge(r); return null; }).catch(() => null);

    // If team changed while we were fetching, discard the result
    if (boardName !== currentTeam()) { knowledgeLoading = false; return; }

    knowledgeLoading = false;
    knowledgeLoaded = true;

    if (refused) {
        panel.innerHTML = '<div class="knowledge-empty">This team\u2019s knowledge is too large to show through Coral Hub; open it on the computer.</div>';
        return;
    }
    if (!data || !data.exists) {
        if (job.status === 'failed') { renderDistillFailed(panel, boardName, job); return; }
        panel.innerHTML = `<div class="knowledge-empty">
            <div style="margin-bottom:12px">No distilled knowledge yet for this team.</div>
            <button class="btn btn-primary" data-on-click="knowledge-distill" data-board="${escapeAttr(boardName)}">Distill Knowledge</button>
        </div>`;
        return;
    }

    knowledgeData = data;
    renderKnowledgePanel(panel, boardName);
    // The last run's outcome above the bundle: when it finished, or that it failed.
    const viewer = panel.querySelector('.knowledge-viewer');
    if (viewer && (job.status === 'complete' || job.status === 'failed')) {
        const note = document.createElement('div');
        note.className = 'knowledge-run-note' + (job.status === 'failed' ? ' is-failed' : '');
        note.setAttribute('role', 'status');
        note.textContent = job.status === 'failed'
            ? `The last distillation failed${job.error ? ': ' + job.error : ''}. This is the knowledge from before it.`
            : (job.finished_at ? `Updated ${_formatWhen(job.finished_at)}` : 'Updated') + _partialNote(job);
        if (job.status === 'complete' && _partialNote(job)) note.classList.add('is-partial');
        viewer.prepend(note);
    }
}

/** A complete run can be partial: the agents it couldn't distill keep their earlier knowledge. */
function _partialNote(job) {
    const failed = Array.isArray(job.failed_agents) ? job.failed_agents : [];
    let text = '';
    if (failed.length) {
        const list = failed.map(f => `${f.agent}${f.error ? ` (${f.error})` : ''}`).join('; ');
        text += `. ${failed.length} agent${failed.length === 1 ? '' : 's'} couldn't be distilled: ${list}. ${failed.length === 1 ? 'It keeps its' : 'They keep their'} earlier knowledge, if any`;
    }
    if (job.collaboration_error) text += `. The collaboration pass failed (${job.collaboration_error}), so the documents were built without it`;
    return text;
}

function _formatWhen(iso) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

const _SPIN = '<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" class="spin" style="margin-bottom:8px" aria-hidden="true"><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>';
const _PHASES = { starting: 'Starting knowledge distillation', extracting: 'Pass 1: Extracting knowledge', 'cross-referencing': 'Pass 2: Cross-referencing collaboration', formatting: 'Pass 3: Building OKF documents' };

/** The run in progress, from its last status. */
function renderDistillProgress(panel, job) {
    const p = (job && job.progress) || {};
    const pct = p.total > 0 ? Math.round((p.completed / p.total) * 100) : 0;
    const phase = _PHASES[p.phase] || (p.phase ? String(p.phase) : _PHASES.starting);
    const agent = p.agent ? ` — ${escapeHtml(p.agent)}` : '';
    const counts = p.total > 0 ? `${Number(p.completed) || 0}/${Number(p.total) || 0} agents` : 'This reads all agent transcripts and runs 3 passes through Claude';
    panel.innerHTML = `<div class="knowledge-empty knowledge-distilling" role="status">
        ${_SPIN}
        <div>${escapeHtml(phase)}${agent}</div>
        <div class="knowledge-progress" aria-hidden="true"><div class="knowledge-progress-bar" style="width:${pct}%"></div></div>
        <div style="font-size:0.75rem;margin-top:4px">${escapeHtml(counts)}</div>
        <div style="font-size:0.75rem;margin-top:4px">You can leave this view: the run continues, and you'll be told when it's done.</div>
    </div>`;
}

function renderDistillFailed(panel, boardName, job) {
    panel.innerHTML = `<div class="knowledge-empty" style="color:var(--error-text)" role="alert">
        Distillation failed: ${escapeHtml((job && job.error) || 'Unknown error')}
        <div><button class="btn btn-sm" style="margin-top:12px" data-on-click="knowledge-distill" data-board="${escapeAttr(boardName)}">Retry</button></div>
    </div>`;
}

// The Knowledge tab follows the team's run wherever it was started (#3325). The panel is
// looked up on every change, so a re-rendered or re-opened view is updated too.
document.addEventListener('coral:distill-status', e => {
    const { team: t, job, prev } = e.detail || {};
    // Finished while the tab was not on screen: the next visit loads the result.
    if (t && t === currentTeam() && prev && prev.status === 'running' && job.status !== 'running') knowledgeLoaded = false;
    if (!t || t !== currentTeam() || source !== 'knowledge') return;
    const panel = document.getElementById('team-knowledge-view');
    if (!panel) return;
    if (job.status === 'running') { knowledgeLoaded = true; renderDistillProgress(panel, job); return; }
    // Finished (or found finished): show the result.
    if (prev && prev.status === 'running') { knowledgeLoaded = false; knowledgeLoading = false; loadKnowledgeTab(); }
});

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
            <button class="inline-preview-mode-btn knowledge-home-btn" title="Back to overview" style="display:${knowledgeActiveTab === 'index' ? 'none' : ''}" aria-label="Back to overview"><span class="material-icons" aria-hidden="true">home</span></button>
            <span class="knowledge-nav-title">${knowledgeActiveTab === 'index' ? 'Overview' : escapeHtml(knowledgeActiveTab)}</span>
        </div>
        <div class="knowledge-toolbar-actions">
            <button class="inline-preview-mode-btn knowledge-redistill-btn" data-on-click="knowledge-distill" data-board="${escapeAttr(boardName)}" title="Re-distill" aria-label="Re-distill"><span class="material-icons" aria-hidden="true">refresh</span></button>
            <button class="inline-preview-mode-btn${knowledgeMode === 'preview' ? ' active' : ''}" data-kmode="preview" title="Preview" aria-label="Preview"><span class="material-icons" aria-hidden="true">visibility</span></button>
            <button class="inline-preview-mode-btn${knowledgeMode === 'edit' ? ' active' : ''}" data-kmode="edit" title="Edit" aria-label="Edit"><span class="material-icons" aria-hidden="true">edit</span></button>
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
        const sanitized = sanitizeHtml(html, { ADD_ATTR: ['data-knowledge-link'] }, { links: 'board', knowledgeLinks: true }); // distilled from agents' text (#3542)
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

export async function distillKnowledgeInTab(boardName) {
    selectKnowledgeSource();
    const panel = document.getElementById('team-knowledge-view');
    if (!panel) return;
    knowledgeLoaded = true;
    renderDistillProgress(panel, distillJob(boardName).status === 'running' ? distillJob(boardName) : { progress: { phase: 'starting' } });
    try {
        // Starts a run, or joins the one already running (#3325). Progress, the result and a
        // toast follow from knowledge_distill.js, wherever the user is by then.
        await startDistill(boardName);
    } catch (err) {
        const p = document.getElementById('team-knowledge-view');
        if (p && boardName === currentTeam()) p.innerHTML = `<div class="knowledge-empty" style="color:var(--error-text)">${escapeHtml(err.message)}</div>`;
    }
}

// Distill / Retry / Re-distill in the team's knowledge panel (#3645).
registerActions({
    'knowledge-distill': el => { const b = textOf(el.dataset.board); if (b) window.distillTeamKnowledge(b); },
});
