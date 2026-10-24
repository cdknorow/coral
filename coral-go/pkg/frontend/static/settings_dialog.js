/* Moved out of modals.js as it was (#4684): The Settings dialog: its sections, the API key, storage, database encryption, filling and saving it. No behaviour change. */
import { defaultTerminalFont, terminalFontFor } from './terminal_font.js';
import { importOnce } from './import_once.js';
import { updatesUiOn } from './update_flag.js';
import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr, copyText } from './utils.js';
import { refreshChangedFiles } from './changed_files.js';
import { getEngineNames } from './renderers.js';
import { renderCaptureText, syncPaneWidth } from './capture.js';
import { getTerminal } from './xterm_renderer.js';
import { desktopNotifySupported, desktopNotifyEnabled, enableDesktopNotify, disableDesktopNotify } from './desktop_notify.js';
import { applyCloudSettingsDialog, filterSettingsPayload } from './cloud_settings.js';
import { isCloud } from './server_base.js';
import { requestJson } from './api.js';
import { NOTHING_TO_SAVE } from './settings_diff.js';
import { settingBool, changedSettings, savedWords, renderRemoteAccessStatus, renderDebugInputLogStatus, renderToolsFolderStatus, applyTheme, clearCustomThemeVars, applyCustomThemeByName, defaultThemeChoice } from './settings_core.js';
import { attachDialogFocus } from './dialog_focus.js';
import { browserLocal } from './safe_storage.js';
import { openSettingsGroup } from './settings_nav.js';
const { _fillModelDatalist, _getAgentModels, _invalidateAgentModels, _invalidateDefaultModels } = await importOnce('./launch_models.js');
const { defaultPermissionMode, _updatePermModeDescription } = await importOnce('./permission_flags.js');

// ── Settings: API key (#3152) ─────────────────────────────────────────────
// Masked at full width with Show and Copy; a status message (not configured,
// not available) is shown as plain text with the controls off.

function _setApiKey(input, key, message) {
    const show = document.getElementById('settings-api-key-show');
    const copy = document.getElementById('settings-api-key-copy');
    input.value = key || message;
    input.type = key ? 'password' : 'text';
    if (show) { show.hidden = !key; show.textContent = 'Show'; show.setAttribute('aria-pressed', 'false'); }
    if (copy) copy.disabled = !key;
}

function _wireApiKeyControls(input) {
    if (input.dataset.wired) return;
    input.dataset.wired = '1';
    const show = document.getElementById('settings-api-key-show');
    const copy = document.getElementById('settings-api-key-copy');
    show?.addEventListener('click', () => {
        const reveal = input.type === 'password';
        input.type = reveal ? 'text' : 'password';
        show.textContent = reveal ? 'Hide' : 'Show';
        show.setAttribute('aria-pressed', String(reveal));
    });
    copy?.addEventListener('click', async () => {
        const ok = await copyText(input.value);
        copy.textContent = ok ? 'Copied!' : 'Copy failed';
        setTimeout(() => { copy.textContent = 'Copy'; }, 1500);
    });
}

// The deletion-request ID is read only when Settings opens, even if analytics is off.
async function loadTelemetryInstallID() {
    const input = document.getElementById('settings-telemetry-install-id');
    const copy = document.getElementById('settings-telemetry-install-copy');
    const status = document.getElementById('settings-telemetry-install-status');
    const hint = document.getElementById('settings-telemetry-install-hint'); // "Send it with a deletion request.": only when there is an ID to send (#7040)
    if (!input || !copy || isCloud()) return;
    input.textContent = 'Loading…';
    copy.disabled = true;
    if (status) status.textContent = '';
    if (!copy.dataset.wired) {
        copy.dataset.wired = '1';
        copy.addEventListener('click', async () => {
            if (copy.disabled) return;
            const ok = await copyText(input.textContent);
            if (status) status.textContent = ok ? 'Install ID copied.' : 'Could not copy. Select the ID and copy it.';
        });
    }
    try {
        const data = await requestJson('GET', '/api/system/telemetry');
        const available = data.install_id_status === 'available' && typeof data.install_id === 'string' && data.install_id.length > 0;
        input.textContent = available ? data.install_id : data.install_id_status === 'missing' ? 'No install ID has been created.' : 'Install ID unavailable.';
        copy.disabled = !available;
        if (hint) hint.hidden = !available;
    } catch (_) {
        input.textContent = 'Install ID unavailable.';
        if (hint) hint.hidden = true;
    }
}

// ── Settings Modal ────────────────────────────────────────────────────────



function renderDatabaseEncryptionStatus(status, loadError = false) {
    const select = document.getElementById('settings-database-encryption');
    const hint = document.getElementById('settings-database-encryption-status');
    if (!select || !hint) return;
    // A build without database encryption has nothing to choose: the row is not shown (UI audit #5589, finding 17),
    // and a save leaves the setting alone (the select is disabled).
    const row = select.closest('.settings-row');
    if (row) row.hidden = loadError || !status || status.encryption_compiled === false;
    if (loadError || !status) {
        select.disabled = true;
        hint.textContent = 'Database encryption settings are unavailable in this server build.';
        return;
    }
    const mode = status.saved_mode || status.effective_mode || 'disabled';
    select.value = mode;
    select.dataset.shown = mode; // what Save compares with, to tell a changed choice from none
    select.disabled = status.feature_available === false || status.encryption_compiled === false;
    const effective = status.effective_mode || 'disabled';
    const restart = status.restart_required === true;
    const surface = status.unlock_surface || 'unknown';
    const capability = status.encryption_compiled === false
        ? ' Experimental database encryption is not included in this standard build.'
        : status.feature_available === false ? ' Encryption is unavailable in this build.' : ' Database encryption is experimental.';
    hint.textContent = `Saved: ${mode}; running: ${effective}${restart ? '; restart required' : ''}. Unlock: ${surface}.${capability}`;
}

async function loadDatabaseEncryptionStatus() {
    try {
        const status = await requestJson('GET', '/api/system/database-security');
        renderDatabaseEncryptionStatus(status);
        return status;
    } catch (_) {
        renderDatabaseEncryptionStatus(null, true);
        return null;
    }
}

// True when the database encryption choice differs from what the dialog showed.
function databaseEncryptionChanged() {
    const select = document.getElementById('settings-database-encryption');
    return Boolean(select) && !select.disabled && select.dataset.shown !== undefined && select.value !== select.dataset.shown;
}

async function saveDatabaseEncryptionSettings() {
    const select = document.getElementById('settings-database-encryption');
    if (!select || select.disabled) return true;
    const mode = select.value;
    const resp = await fetch('/api/system/database-security', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode }),
    });
    if (!resp.ok) throw new Error(`database security save failed (${resp.status})`);
    const status = await resp.json();
    renderDatabaseEncryptionStatus(status);
    return true;
}





// ── Theme ────────────────────────────────────────────────────────────────






// ── Artifact store (#3553) ─────────────────────────────────────────────
const GIB = 1024 ** 3;
function _gib(n) {
    n = Number(n) || 0;
    if (n >= GIB / 10) return `${(n / GIB).toFixed(1)} GiB`;
    if (n >= 1024 ** 2) return `${Math.round(n / (1024 ** 2))} MiB`;
    if (n >= 1024) return `${Math.round(n / 1024)} KiB`;
    return `${n} B`;
}

/** Settings: the artifact store's size and the saved diffs' sizes, as separate lines, next to the
 *  store limit (which counts the artifacts only). This computer's dashboard only. */
async function loadArtifactStoreSize({ fresh = false } = {}) {
    let fileCount = null; // the file counts the breakdown shows (the limit lines say only how much space)
    const out = document.getElementById('settings-artifact-store-size');
    const input = document.getElementById('settings-artifact-store-max');
    if (!out || !input) return;
    const rin = document.getElementById('settings-received-max');
    const rout = document.getElementById('settings-received-size');
    if (fresh) {
        // Each open starts from nothing: the numbers of the last open must not outlive it. A failed or slow load then leaves the fields empty and off (a limit that was not
        // read is never saved back: the flag is gone), and the lines say that it is loading, not what it showed last time (#4469, UX's #4456 SHOULD 9).
        for (const [i, o] of [[input, out], [rin, rout]]) {
            if (!i || !o) continue;
            i.value = ''; delete i.dataset.loaded; i.disabled = true;
            o.textContent = 'Loading…'; delete o.dataset.state;
        }
        const ub = document.getElementById('settings-received-unlisted'); if (ub) ub.hidden = true;
    }
    try {
        const d = await requestJson('GET', '/api/system/artifact-store');
        fileCount = { artifacts: Number(d.objects) || 0, taskDiffs: Number(d.task_diffs?.files) || 0, sessionDiffs: Number(d.session_diffs?.files) || 0 };
        input.value = d.max_bytes > 0 ? String(Math.round(d.max_bytes / GIB)) : '0';
        input.dataset.loaded = '1'; // only a limit read from Coral is saved back
        input.disabled = false;
        const plural = (n, one, many) => `${Number(n).toLocaleString()} ${Number(n) === 1 ? one : many}`;
        // The same three states as the received files' line: below 80%, from 80% with what to do, and at the limit that new uploads are refused, in the warning colour.
        let art = _gib(d.bytes); // the limit field is beside it: "3.0 GiB of 10.0 GiB"; the file count is in the breakdown
        const atLimit = d.max_bytes > 0 && d.bytes >= d.max_bytes;
        if (d.max_bytes > 0) {
            const full = atLimit || d.bytes * 5 >= d.max_bytes * 4;
            art += atLimit ? ` of ${_gib(d.max_bytes)}: new uploads are refused` : full ? ` of ${_gib(d.max_bytes)} (${Math.floor((d.bytes / d.max_bytes) * 100)}% full)` : ` of ${_gib(d.max_bytes)}`;
            if (full) art += '. Delete artifacts in Team Artifacts to make room, or raise the limit.';
            // Coral raised an unset limit by itself when the 20 GiB default arrived (#4602): the Operator should know it, and can change it here.
            if (d.auto_raised > 0 && d.auto_raised === d.max_bytes) art += `${full ? ' ' : '. '}The limit was raised to ${_gib(d.auto_raised)} automatically when the 20 GiB default arrived, because the store already held more than 16 GiB. Change it here.`;
        }
        const artNode = document.createElement('span');
        artNode.textContent = art;
        if (atLimit) artNode.dataset.state = 'warn';
        const rest = [`Diffs: ${_gib((d.task_diffs?.bytes || 0) + (d.session_diffs?.bytes || 0))} in ${plural((d.task_diffs?.files || 0) + (d.session_diffs?.files || 0), 'file', 'files')}`];
        out.replaceChildren(artNode, ...rest.flatMap(l => [document.createElement('br'), l]));
        showReceivedCap(d.received);
    } catch {
        if (fresh) {
            for (const i of [input, rin]) if (i) { i.value = ''; delete i.dataset.loaded; } // nothing typed meanwhile is kept: the field is off and saves nothing
            out.textContent = "Couldn't read the store's size, so the limit is not shown.";
            if (rout) rout.textContent = "Couldn't read the received files' size, so their limit is not shown.";
        } else out.textContent = "Couldn't read the store's size.";
    }
    loadStorageDetail(fileCount);
    importOnce('./received_cap_notice.js').then(m => m.checkReceivedCap({ inSettings: true })).catch(() => {}); // remembered once at 80%; the line says it, no toast over this dialog (#4441)
}

// ── Terminal logs of ended agents (#4447) ─────────────────────────────
let _endedLogs = null; // { count, bytes } as last shown: what a confirmation is for

/** Settings: how many terminal logs there are and how many belong to ended agents. This computer's dashboard only. */
async function loadTerminalLogs() {
    const out = document.getElementById('settings-terminal-logs-status');
    const button = document.getElementById('settings-terminal-logs-delete');
    if (!out || !button) return;
    try {
        const d = await requestJson('GET', '/api/system/terminal-logs');
        const n = Number(d.ended?.count) || 0;
        _endedLogs = { count: n, bytes: Number(d.ended?.bytes) || 0 };
        const files = `${Number(d.files).toLocaleString()} log${d.files === 1 ? '' : 's'}`;
        out.textContent = n
            ? `${files}, ${_gib(d.bytes)}. ${n.toLocaleString()} from ended agents (${_gib(_endedLogs.bytes)}).`
            : `${files}, ${_gib(d.bytes)}`;
        button.hidden = n === 0;
        if (n) button.textContent = `Delete ${n.toLocaleString()} terminal log${n === 1 ? '' : 's'} of ended agents (${_gib(_endedLogs.bytes)})`;
        if (!button.dataset.wired) { button.dataset.wired = '1'; button.addEventListener('click', confirmDeleteEndedLogs); }
    } catch {
        _endedLogs = null;
        button.hidden = true;
        out.textContent = "Couldn't read the terminal logs.";
    }
}

function confirmDeleteEndedLogs() {
    const shown = _endedLogs;
    if (!shown || !shown.count) return;
    const n = shown.count;
    window.showConfirmModal?.('Delete terminal logs',
        `Delete ${n.toLocaleString()} terminal log${n === 1 ? '' : 's'} of ended agents (${_gib(shown.bytes)})? What ${n === 1 ? 'that agent\'s terminal' : 'those agents\' terminals'} showed can't be recovered. The logs of live, sleeping and archived agents are kept.`,
        async () => {
            try {
                const d = await requestJson('POST', '/api/system/terminal-logs/delete-ended', shown);
                const left = (d.failed || []).length;
                showToast(`Deleted ${Number(d.deleted || 0).toLocaleString()} terminal log${d.deleted === 1 ? '' : 's'}, ${_gib(d.bytes)} freed.${left ? ` ${left} could not be deleted or are not ended any more.` : ''}`, false, 8000);
            } catch (e) {
                if (e.status === 409) showToast('The logs of ended agents changed since you looked, so nothing was deleted. Look at the new numbers and try again.', true, 8000);
                else showToast(`Couldn't delete the terminal logs: ${e.message}`, true);
            }
            loadTerminalLogs();
        });
    requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
}

// ── Free space and automatic cleanup (#3554) ──────────────────────────
const PRUNE_KINDS = { diffs: ['diffs'], objects: ['objects'], all: ['all'] };

function _lines(el, lines) {
    el.replaceChildren(...lines.flatMap((l, i) => (i ? [document.createElement('br'), l] : [l])));
}

/** The store's breakdown (by age, by board, kept) and automatic cleanup's state. */
async function loadStorageDetail(fileCount = null) {
    const out = document.getElementById('settings-storage-detail');
    const days = document.getElementById('settings-retention-days');
    const kind = document.getElementById('settings-retention-kind');
    if (!out || !days) return;
    try {
        const d = (await requestJson('GET', '/api/system/artifact-store?detail=1')).detail;
        if (!d) throw new Error('no detail');
        const age = (k) => ['<1d', '1-7d', '7-30d', '>30d'].map(b => `${b} ${_gib(d.by_age?.[k]?.[b]?.bytes || 0)}`).join(' · ');
        const lines = [];
        if (fileCount) lines.push(`Files: ${fileCount.artifacts.toLocaleString()} artifacts, ${(fileCount.taskDiffs + fileCount.sessionDiffs).toLocaleString()} saved diffs`);
        lines.push(
            `Artifacts by age: ${age('objects')}`,
            `Task diffs by age: ${age('task_diffs')}`,
        );
        const boards = (d.by_board || []).slice(0, 5).map(b => `${b.label || b.board} ${_gib(b.bytes)}`);
        if (boards.length) lines.push(`Largest boards: ${boards.join(' · ')}`);
        if (d.kept?.count) lines.push(`Kept: ${d.kept.count} artifact${d.kept.count === 1 ? '' : 's'}, ${_gib(d.kept.bytes)}`);
        _lines(out, lines);
        const ret = d.retention || {};
        days.value = String(Number(ret.days) || 0);
        days.dataset.loaded = '1';
        kind.value = ['diffs', 'objects', 'all'].includes(ret.kinds) ? ret.kinds : 'diffs';
        showRetentionStatus(ret);
        syncRetentionKind();
        if (!days.dataset.wired) { days.dataset.wired = '1'; days.addEventListener('input', () => { syncRetentionKind(); const n = Number(days.value) > 0 ? Math.floor(Number(days.value)) : 0; if (!n) showRetentionStatus({ days: 0, last: ret.last }); else if (n !== (Number(ret.days) || 0)) _lines(document.getElementById('settings-retention-status'), [`Turns on at ${n} days when you save.`]); else showRetentionStatus(ret); }); }
    } catch {
        out.textContent = '';
    }
}

/** 0 days is off, and the select that says what it prunes is off with it (#7041): it looked live while it did nothing. */
function syncRetentionKind() {
    const days = document.getElementById('settings-retention-days');
    const kind = document.getElementById('settings-retention-kind');
    if (days && kind) kind.disabled = !(Number(days.value) > 0);
}

function showRetentionStatus(ret) {
    const status = document.getElementById('settings-retention-status');
    const review = document.getElementById('settings-retention-review');
    if (!status || !review) return;
    const days = Number(ret.days) || 0;
    const enabled = ret.enabled_at ? new Date(ret.enabled_at) : null;
    const acked = ret.acknowledged_at ? new Date(ret.acknowledged_at) : null;
    const waiting = days > 0 && enabled && !(acked && acked >= enabled) && Date.now() - enabled < 24 * 3600e3;
    review.hidden = !waiting;
    const lines = [];
    if (!days) lines.push('Off.');
    else if (waiting) lines.push(`On (${days} days), waiting: review what it would do to start now, or it starts on ${new Date(enabled.getTime() + 24 * 3600e3).toLocaleString()}.`);
    else if (days && !enabled) lines.push(`On (${days} days): its first check is within the hour; it then waits for your review.`);
    else lines.push(`On (${days} days).`);
    if (ret.last?.at) lines.push(`Last run ${new Date(ret.last.at).toLocaleString()}: freed ${_gib(ret.last.freed)}${ret.last.capped ? ' (stopped at the 2 GiB cap)' : ''}.`);
    _lines(status, lines);
}

/** Settings' value for automatic cleanup ({} when not read from Coral). */
function retentionSettings() {
    const days = document.getElementById('settings-retention-days');
    const kind = document.getElementById('settings-retention-kind');
    if (!days || isCloud() || days.dataset.loaded !== '1') return {};
    const n = Math.max(0, Math.min(3650, Math.floor(Number(days.value) || 0)));
    return { artifact_retention_days: String(n), artifact_retention_kinds: kind?.value || 'diffs' };
}

async function pruneRequest(body) {
    return requestJson('POST', '/api/artifacts/prune', body);
}

// The page's own words for what a prune removes, never the server's kind names (#4469, UX's #4456 SHOULD 11).
const PRUNE_NAMES = { diffs: ['saved diff', 'saved diffs'], objects: ['unlinked artifact', 'unlinked artifacts'], 'diff-objects': ['stored diff copy', 'stored diff copies'] };
function pruneSummary(rep) {
    const n = Object.values(rep.kinds || {}).reduce((a, k) => a + (k.count || 0), 0);
    return { n, text: Object.entries(rep.kinds || {}).map(([k, v]) => `${Number(v.count).toLocaleString()} ${(PRUNE_NAMES[k] || [k, k])[v.count === 1 ? 0 : 1]} (${_gib(v.bytes)})`).join(', ') };
}

/** A dry run, a confirmation (focus on Cancel), then the real run. */
async function previewAndRun(body, verb) {
    const result = document.getElementById('settings-storage-result');
    result.removeAttribute('data-state');
    result.textContent = 'Checking…';
    let dry;
    try {
        dry = await pruneRequest({ ...body, dry_run: true });
    } catch (e) {
        result.dataset.state = 'error';
        result.textContent = `Couldn't check: ${e.message}`;
        return;
    }
    const s = pruneSummary(dry);
    if (!s.n) {
        result.textContent = 'Nothing to do.';
        return;
    }
    result.textContent = '';
    const what = verb === 'Shrink' ? `saving about ${_gib(dry.freed)}. The code changes in them stay.` : `freeing about ${_gib(dry.freed)}. This can't be undone.`;
    const asker = document.getElementById(verb === 'Shrink' ? 'settings-storage-shrink' : 'settings-storage-prune');
    window.showConfirmModal?.(`${verb} saved data`, `${verb} ${s.text}, ${what}`, async () => {
        result.textContent = `${verb === 'Shrink' ? 'Shrinking' : 'Pruning'}…`;
        try {
            const done = await pruneRequest({ ...body, dry_run: false });
            result.textContent = `${verb === 'Shrink' ? 'Saved' : 'Freed'} ${_gib(done.freed)} from ${Number(done.removed).toLocaleString()} item${done.removed === 1 ? '' : 's'}${done.skipped ? `; ${done.skipped} left alone because ${done.skipped === 1 ? 'it' : 'they'} changed` : ''}${done.errors ? `; ${done.errors} error${done.errors === 1 ? '' : 's'}` : ''}.`;
            loadArtifactStoreSize();
        } catch (e) {
            result.dataset.state = 'error';
            result.textContent = `Couldn't finish: ${e.message}`;
        }
        asker?.focus(); // Yes closed the confirmation: the focus goes back to the button that asked, not to the page (#4469, UX's #4456 SHOULD 10)
    });
    // No default: focus starts on Cancel.
    requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
}

function wireStorageActions() {
    const shrink = document.getElementById('settings-storage-shrink');
    if (!shrink || shrink.dataset.wired) return;
    shrink.dataset.wired = '1';
    shrink.addEventListener('click', () => previewAndRun({ shrink: true }, 'Shrink'));
    document.getElementById('settings-storage-prune')?.addEventListener('click', () => {
        const days = Math.max(1, Math.floor(Number(document.getElementById('settings-prune-days').value) || 14));
        const kind = document.getElementById('settings-prune-kind').value;
        previewAndRun({ older_than_seconds: days * 86400, kinds: PRUNE_KINDS[kind] || ['diffs'] }, 'Prune');
    });
    document.getElementById('settings-retention-review')?.addEventListener('click', async () => {
        const status = document.getElementById('settings-retention-status');
        const days = Number(document.getElementById('settings-retention-days').value) || 0;
        const kind = document.getElementById('settings-retention-kind').value;
        if (!days) return;
        let dry;
        try {
            dry = await pruneRequest({ older_than_seconds: days * 86400, kinds: PRUNE_KINDS[kind] || ['diffs'], dry_run: true });
        } catch (e) {
            status.dataset.state = 'error';
            status.textContent = `Couldn't check: ${e.message}`;
            return;
        }
        const s = pruneSummary(dry);
        window.showConfirmModal?.('Start automatic cleanup',
            `Its first run would remove ${s.n ? `${s.text}, about ${_gib(dry.freed)}` : 'nothing right now'} (at most 2 GiB a run), then check every hour. Start it?`,
            async () => {
                let acked = true;
                try { await requestJson('POST', '/api/artifacts/retention/ack'); } catch { acked = false; }
                if (!acked) {
                    status.dataset.state = 'error';
                    status.textContent = "Couldn't start it: save the setting first.";
                    return;
                }
                loadStorageDetail();
            });
        requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
    });
}

/** Settings: what other teams and Corals have sent this computer, against the limit on it (#4441). One status line says the total, how full it is (from
 *  80% on, with what to do), that new files are refused at the limit (in the warning colour), and how many received files no board lists. */
function receivedLine(r) {
    let line = _gib(r.bytes);
    let full = false, atLimit = false;
    if (r.max_bytes > 0) {
        const pct = Math.floor((r.bytes / r.max_bytes) * 100);
        atLimit = r.bytes >= r.max_bytes;
        full = atLimit || r.bytes * 5 >= r.max_bytes * 4;
        line += atLimit ? ` of ${_gib(r.max_bytes)}: new received files are refused` : full ? ` of ${_gib(r.max_bytes)} (${pct}% full)` : ` of ${_gib(r.max_bytes)}`;
    }
    if (full) line += '. Delete received files in Team Artifacts to make room, or raise the limit.';
    const u = Number(r.unlisted?.count) || 0;
    if (u) line += `${full ? ' ' : '. '}Not listed on any board: ${u.toLocaleString()} file${u === 1 ? '' : 's'}, ${_gib(r.unlisted.bytes)}. Another Coral sent ${u === 1 ? 'it' : 'them'} but the message that names ${u === 1 ? 'it' : 'them'} never came. ${u === 1 ? 'It counts' : 'They count'} toward the limit.${u > (r.unlisted.items || []).length ? ` Showing the oldest ${(r.unlisted.items || []).length}; delete them, then the rest.` : ''}`;
    return { line, atLimit };
}

function showReceivedCap(r) {
    const out = document.getElementById('settings-received-size');
    const input = document.getElementById('settings-received-max');
    if (!out || !input || !r) return;
    input.value = r.max_bytes > 0 ? String(Math.round(r.max_bytes / GIB)) : '0';
    input.dataset.loaded = '1'; // only a limit read from Coral is saved back
    input.disabled = false;
    const { line, atLimit } = receivedLine(r);
    out.textContent = line;
    if (atLimit) out.dataset.state = 'warn'; else delete out.dataset.state; // at the limit new files are refused: the warning colour
    showUnlistedReceived(r.unlisted);
}

// Received files no board lists: their message never came. They count in the limit; the Operator can delete them, by exact digest, after a
// confirmation. Never automatic (#4441, Security's #4440 INFO 2). The count is in the status line above; the button names what it deletes.
let _unlistedItems = [];
let _unlistedBytes = 0;
function showUnlistedReceived(u) {
    const box = document.getElementById('settings-received-unlisted');
    const button = document.getElementById('settings-received-unlisted-delete');
    if (!box || !button) return;
    _unlistedItems = Array.isArray(u?.items) ? u.items.map(i => i.id) : [];
    _unlistedBytes = (u?.items || []).reduce((sum, i) => sum + (Number(i.size) || 0), 0);
    box.hidden = _unlistedItems.length === 0;
    if (!_unlistedItems.length) return;
    const n = _unlistedItems.length;
    button.textContent = `Delete ${n.toLocaleString()} unlisted file${n === 1 ? '' : 's'} (${_gib(_unlistedBytes)})`;
    if (!button.dataset.wired) {
        button.dataset.wired = '1';
        button.addEventListener('click', confirmDeleteUnlisted);
    }
}

function confirmDeleteUnlisted() {
    const ids = _unlistedItems.slice();
    if (!ids.length) return;
    const n = ids.length;
    window.showConfirmModal?.('Delete received files',
        `Delete ${n.toLocaleString()} unlisted file${n === 1 ? '' : 's'} (${_gib(_unlistedBytes)})? Only ${n === 1 ? 'this file is' : 'these files are'} removed from Coral and can't be recovered; nothing else is touched, and a file something now lists or links is left alone.`,
        async () => {
            let d;
            try {
                d = await requestJson('POST', '/api/artifacts/received/unlisted/delete', { ids });
            } catch (e) {
                showToast(`Couldn't delete the received files: ${e.message}`, true);
                return;
            }
            const left = (d.skipped || []).length;
            showToast(`Deleted ${Number(d.deleted || 0).toLocaleString()} received file${d.deleted === 1 ? '' : 's'}, ${_gib(d.bytes)} freed.${left ? ` ${left} left alone: something lists or links ${left === 1 ? 'it' : 'them'}.` : ''}`, false, 8000);
            loadArtifactStoreSize();
        });
    requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
}

/** The received-files limit to save, in bytes ({} when the field isn't on this page or was never read). */
function receivedMaxSetting() {
    const input = document.getElementById('settings-received-max');
    if (!input || isCloud() || input.dataset.loaded !== '1') return {};
    if (String(input.value).trim() === '') return {}; // an emptied field is unchanged, never 0 (no limit) saved by accident
    const gib = Math.max(0, Math.floor(Number(input.value) || 0));
    return { received_artifacts_max_bytes: String(gib * GIB) };
}

/** The limit to save, in bytes ({} when the field isn't on this page). */
function artifactStoreMaxSetting() {
    const input = document.getElementById('settings-artifact-store-max');
    // Not on this page, or never read from Coral (a failed load must not reset a limit to 0).
    if (!input || isCloud() || input.dataset.loaded !== '1') return {};
    if (String(input.value).trim() === '') return {}; // an emptied field is unchanged, never 0 (no limit) saved by accident
    const gib = Math.max(0, Math.floor(Number(input.value) || 0));
    return { artifact_store_max_bytes: String(gib * GIB) };
}

// The settings the dialog opened with: Save sends only the fields that differ from them (settings_diff.js).
let _settingsAtOpen = null;

// The CLI path fields, as the server has them now. The dialog opens at once from the settings this page loaded, which a
// CLI path saved from another tab or client has outdated: a moment later the fields the person has not touched show the
// server's value, and it becomes what Save compares with (so a path the person never edited is never written back).
const CLI_PATH_FIELDS = { cli_path_claude: 'settings-cli-path-claude', cli_path_codex: 'settings-cli-path-codex', cli_path_pi: 'settings-cli-path-pi', cli_path_agy: 'settings-cli-path-agy' };

// The CLI path settings whose field the person typed in since the dialog opened.
function editedCLIPathKeys() {
    return Object.entries(CLI_PATH_FIELDS).filter(([, id]) => document.getElementById(id)?.dataset.edited === '1').map(([key]) => key);
}

// dropUnfilledCLIPaths removes from payload the CLI path settings whose field the dialog has not filled and the person has not
// typed in: an empty field that was never filled is not a path cleared on purpose (it would clear the path, v1.4.80 gate).
function dropUnfilledCLIPaths(payload) {
    for (const [key, id] of Object.entries(CLI_PATH_FIELDS)) {
        const el = document.getElementById(id);
        if (!el || el.dataset.filled === '1' || el.dataset.edited === '1') continue;
        delete payload[key];
        if (key === 'cli_path_agy') delete payload.cli_path_gemini; // one field for both
    }
    return payload;
}

function refreshCLIPathFields() {
    if (isCloud()) return; // the CLI path fields are hidden on the hub (machine settings): nothing to refresh
    const { cli_path_agy: _agy, ...fields } = CLI_PATH_FIELDS;
    const opened = _settingsAtOpen;
    const ids = [...Object.values(fields), 'settings-cli-path-agy'];
    // Typing in a field marks it edited, even when what is typed equals what it showed.
    for (const id of ids) {
        const el = document.getElementById(id);
        if (!el) continue;
        el.dataset.edited = '';
        if (!el.dataset.watched) { el.dataset.watched = '1'; el.addEventListener('input', () => { el.dataset.edited = '1'; }); }
    }
    fetch('/api/settings').then(r => r.ok ? r.json() : null).then(data => {
        const fresh = data && data.settings;
        // A different open since (the dialog was closed and opened again) has its own refresh.
        if (!fresh || _settingsAtOpen !== opened) return;
        const update = {};
        for (const [key, id] of Object.entries(fields)) {
            const el = document.getElementById(id);
            if (!el || el.dataset.edited === '1') continue; // the person is editing it: leave it
            const now = fresh[key] || '';
            el.value = now;
            update[key] = now;
        }
        const agy = document.getElementById('settings-cli-path-agy');
        if (agy && agy.dataset.edited !== '1') {
            const now = fresh.cli_path_agy || fresh.cli_path_gemini || '';
            agy.value = now;
            update.cli_path_agy = fresh.cli_path_agy || '';
            update.cli_path_gemini = fresh.cli_path_gemini || '';
        }
        Object.assign(_settingsAtOpen, update);
        state.settings = { ...state.settings, ...update };
    }).catch(() => { /* the dialog keeps what it opened with */ });
}

// The machine-only parts of the dialog (privacy, remote access, storage, database encryption, the hub connection); the hub's dialog has none of them.
async function _fillMachineSettings(s) {
    if (!isCloud()) {
        const telemetry = document.getElementById('settings-telemetry-enabled');
        if (telemetry) telemetry.checked = settingBool(s.telemetry_enabled, true);
        loadArtifactStoreSize({ fresh: true }); // the store's size next to its limit (#3553)
        loadTerminalLogs(); // the terminal logs of ended agents (#4447)
        wireStorageActions(); // free space and automatic cleanup (#3554)
        const autoAttach = document.getElementById('settings-board-auto-attach');
        // Off unless the Operator turned it on (#3563); an empty value is unset.
        if (autoAttach) autoAttach.checked = settingBool(s.board_auto_attach || undefined, false);
        loadCodexTrustHooks(); // its own local route, saved when it changes (#153)
        const nudgeArtifacts = document.getElementById('settings-nudge-claude-artifacts');
        // On unless the Operator turned it off (#6583); an empty value is unset.
        if (nudgeArtifacts) nudgeArtifacts.checked = settingBool(s.nudge_claude_artifacts || undefined, true);
        const healthMonitor = document.getElementById('settings-board-health-monitor');
        // On unless the Operator turned it off (#5128).
        if (healthMonitor) healthMonitor.checked = settingBool(s.board_health_monitor || undefined, true);
        const autoResume = document.getElementById('settings-auto-resume');
        // Off unless the Operator turned it on (#3613).
        if (autoResume) autoResume.checked = settingBool(s.auto_resume_after_api_error || undefined, false);
        const autoGoals = document.getElementById('settings-auto-goals');
        // On unless the Operator turned it off (#4204).
        if (autoGoals) autoGoals.checked = settingBool(s.auto_goals || undefined, true);
        const debugInput = document.getElementById('settings-debug-input-log');
        if (debugInput) debugInput.checked = settingBool(s.debug_input_log, false);
        const remote = document.getElementById('settings-remote-access-enabled');
        if (remote) remote.checked = settingBool(s.remote_access_enabled, false);
        renderRemoteAccessStatus(state.privacyStatus);
        try {
            const privacyResp = await fetch('/api/system/privacy');
            if (!privacyResp.ok) throw new Error(`privacy status ${privacyResp.status}`);
            state.privacyStatus = await privacyResp.json();
            if (remote) remote.checked = settingBool(state.privacyStatus.remote_access_enabled, false);
            renderRemoteAccessStatus(state.privacyStatus);
            renderDebugInputLogStatus(state.privacyStatus);
            renderToolsFolderStatus(state.privacyStatus);
        } catch (_) {
            renderRemoteAccessStatus(state.privacyStatus, !state.privacyStatus);
        }
        // Bootstrap database-encryption metadata lives outside the locked DB. The
        // endpoint returns status only; passwords/keys are never sent to the UI.
        loadDatabaseEncryptionStatus();
        // Loaded with the dialog, through importOnce so a failed load is retried (#3140).
        importOnce('./cloud_connect.js').then(m => { m.initCloudConnect(); m.loadCloudStatus(); }).catch(() => {});
        importOnce('./hub_mode_setting.js').then(m => m.loadHubModeSetting()).catch(() => {});
        importOnce('./setup_code_setting.js').then(m => m.initSetupCode()).catch(() => {}); // read only while Network and Hub is the pane in view (#6410)
    }
}

// The theme list: the built-in themes, then the saved custom ones.
async function _fillThemeSelect(s) {
    // Theme — populate with built-in options + saved custom themes
    const themeSelect = document.getElementById("settings-theme");
    if (themeSelect) {
        // Fetch saved custom themes
        let customThemes = [];
        try {
            const resp = await fetch("/api/themes");
            const data = await resp.json();
            customThemes = data.themes || [];
        } catch (_) {}

        // Build options: built-in first, then custom
        let options = `<option value="dark">Dark</option>
            <option value="light">Light</option>
            <option value="system">System</option>`;
        if (customThemes.length > 0) {
            options += `<option disabled>──────────</option>`;
            for (const t of customThemes) {
                const name = escapeAttr(t.name);
                const label = escapeHtml(t.name);
                options += `<option value="custom:${escapeAttr(name)}">${label}</option>`;
            }
        }
        themeSelect.innerHTML = options;

        // Set current value
        const currentTheme = s.custom_theme ? `custom:${s.custom_theme}` : (s.theme || defaultThemeChoice()); // nothing stored shows what the page follows, never a theme that was not picked (#6114)
        themeSelect.value = currentTheme;
    }
}

// The defaults for a new agent: render engine, agent type, permission mode and working directory.
function _fillDefaults(s) {
    // Default Render Engine
    const currentEngine = s.default_renderer || "xterm";
    const options = getEngineNames()
        .map(name => `<option value="${escapeAttr(name)}"${name === currentEngine ? " selected" : ""}>${escapeHtml(name)}</option>`)
        .join("");
    document.getElementById("settings-renderer-select").innerHTML = options;

    // Default Agent Type
    const currentAgentType = s.default_agent_type || "claude";
    const agentTypeSelect = document.getElementById("settings-agent-type");
    if (agentTypeSelect) agentTypeSelect.value = currentAgentType;

    // Default Permission Mode
    const currentPermMode = defaultPermissionMode(s, "claude"); // not saved: Claude starts in Auto, the other CLIs keep their own default (#5836)
    const permModeSelect = document.getElementById("settings-permission-mode");
    if (permModeSelect) {
        permModeSelect.value = currentPermMode;
        _updatePermModeDescription(permModeSelect);
    }

    // Default Working Directory
    const dirInput = document.getElementById("settings-working-dir");
    if (dirInput) {
        dirInput.value = s.default_working_dir || "";
        dirInput.placeholder = dirInput.dataset.coralRoot || "/path/to/project";
    }
}

// The API key box (local only), read from the server.
function _fillApiKey() {
    // API Key
    const apiKeyInput = document.getElementById("settings-api-key");
    if (apiKeyInput && !isCloud()) {
        _wireApiKeyControls(apiKeyInput);
        fetch('/api/system/api-key').then(r => {
            // No sign-in (the dashboard opened without one, local sign-in off): the server refuses the key. Say how to see it, not "Not configured".
            if (r.status === 401 || r.status === 403) return { signIn: true };
            return r.ok ? r.json() : null;
        }).then(data => {
            _setApiKey(apiKeyInput, data && data.key, data && data.signIn ? 'Sign in to see the API key (run: coral open)' : 'Not configured');
        }).catch(() => _setApiKey(apiKeyInput, '', 'Not available'));
    }
}

/** The CLI paths are the way out of "command not found": when Coral cannot find the default agent's CLI the disclosure is already open, with the fields on screen (#7041). Silent on any failure. */
function openCliPathsWhenMissing(type) {
    const details = document.querySelector('#settings-modal .settings-cli-details');
    if (!details || details.open) return;
    fetch(`/api/system/cli-check?type=${encodeURIComponent(type)}`).then(r => r.ok ? r.json() : null).then(d => { if (d && d.found === false) details.open = true; }).catch(() => {});
}

// The CLI paths and the default models (and their datalists, from the model catalog; not on the hub).
function _fillCliAndModels(s) {
    // CLI paths
    const cliClaude = document.getElementById("settings-cli-path-claude");
    const cliCodex = document.getElementById("settings-cli-path-codex");
    const cliAgy = document.getElementById("settings-cli-path-agy");
    const cliPi = document.getElementById("settings-cli-path-pi");
    if (cliClaude) cliClaude.value = s.cli_path_claude || "";
    if (cliCodex) cliCodex.value = s.cli_path_codex || "";
    const agyCliPath = s.cli_path_agy || s.cli_path_gemini || "";
    if (cliAgy) cliAgy.value = agyCliPath;
    if (cliPi) cliPi.value = s.cli_path_pi || "";
    for (const id of Object.values(CLI_PATH_FIELDS)) { const el = document.getElementById(id); if (el) el.dataset.filled = '1'; }
    refreshCLIPathFields();
    openCliPathsWhenMissing(s.default_agent_type || 'claude');

    // Default Models — populate combo inputs + datalists from /api/agent-models
    const defClaude = document.getElementById("settings-default-model-claude");
    const defCodex = document.getElementById("settings-default-model-codex");
    const defAgy = document.getElementById("settings-default-model-agy");
    const defPi = document.getElementById("settings-default-model-pi");
    if (defClaude) defClaude.value = s.default_model_claude || "";
    const defThinkingClaude = document.getElementById("settings-default-thinking-claude");
    if (defThinkingClaude) defThinkingClaude.value = s.default_thinking_claude || "";
    if (defCodex) defCodex.value = s.default_model_codex || "";
    const agyModel = s.default_model_agy || s.default_model_gemini || "";
    if (defAgy) defAgy.value = agyModel;
    if (defPi) defPi.value = s.default_model_pi || "";
    // On the hub the Default Models rows are hidden (machine settings), and the hub has no model catalog of its own to ask.
    if (!isCloud()) importOnce('./custom_models.js').then(m => m.initCustomModels()).catch(() => {}); // the custom models list (#5480): hidden when this server has no such route
    if (!isCloud()) _getAgentModels().then(models => {
        // _getAgentModels swallows errors and resolves with {} on failure; on
        // success it returns the canonical map with an array for each agent type.
        if (!models || !Array.isArray(models.claude)) {
            const notice = document.getElementById("settings-default-models-notice");
            if (notice) notice.textContent = "Could not load model list — you can still type a custom model id.";
            return;
        }
        _fillModelDatalist(document.getElementById("settings-models-claude"), "claude", models);
        _fillModelDatalist(document.getElementById("settings-models-codex"), "codex", models);
        _fillModelDatalist(document.getElementById("settings-models-agy"), "agy", models);
        _fillModelDatalist(document.getElementById("settings-models-pi"), "pi", models);
    });
}

// Grouping, terminal font and scrollback, notifications, update check and scrollbars.
function _fillTerminalAndBrowserPrefs(s) {
    // Group by Team
    const groupByTeamCheck = document.getElementById("settings-group-by-team");
    if (groupByTeamCheck) {
        groupByTeamCheck.checked = browserLocal.get('coral-group-by-team') !== 'false';
    }

    // Terminal Font Size
    const fontSizeSelect = document.getElementById("settings-terminal-font-size");
    if (fontSizeSelect) fontSizeSelect.value = s.terminal_font_size || String(defaultTerminalFont()); // what is drawn when nothing is chosen: a phone's is smaller (#6577)

    // Terminal Scrollback
    const scrollbackSelect = document.getElementById("settings-terminal-scrollback");
    if (scrollbackSelect) scrollbackSelect.value = s.terminal_scrollback || "20000";

    // Fit Pane Width
    const fitPaneCheck = document.getElementById("settings-fit-pane-width");
    if (fitPaneCheck) fitPaneCheck.checked = !!s.fit_pane_width;

    // Notify Needs Input
    const notifyCheck = document.getElementById("settings-notify-needs-input");
    if (notifyCheck) notifyCheck.checked = !!s.notify_needs_input;

    // Desktop alerts: a per-browser opt-in, never sent to the server
    const desktopCheck = document.getElementById("settings-desktop-notify");
    if (desktopCheck) {
        desktopCheck.checked = desktopNotifyEnabled();
        desktopCheck.disabled = !desktopNotifySupported();
        if (!desktopCheck.dataset.bound) {
            desktopCheck.dataset.bound = "1";
            desktopCheck.addEventListener("change", async () => {
                if (desktopCheck.checked) desktopCheck.checked = await enableDesktopNotify();
                else disableDesktopNotify();
            });
        }
    }

    // Check for Updates (stored in localStorage, defaults to on)
    const updateCheck = document.getElementById("settings-check-updates");
    if (updateCheck) updateCheck.checked = browserLocal.get("coral-update-check-enabled") !== "false";

    // Show Scrollbars (defaults to true)
    const scrollbarsCheck = document.getElementById("settings-show-scrollbars");
    if (scrollbarsCheck) scrollbarsCheck.checked = s.show_scrollbars !== false;
}

// File search, git diff base and scan interval, agent priority.
function _fillFilesAndGit(s) {
    // File Search Mode (defaults to 'directory')
    const fileSearchSelect = document.getElementById("settings-file-search-mode");
    if (fileSearchSelect) fileSearchSelect.value = s.file_search_mode || 'directory';
    const fileSearchLimitSelect = document.getElementById("settings-file-search-limit");
    if (fileSearchLimitSelect) fileSearchLimitSelect.value = s.file_search_limit || "500";

    // Git Diff Mode (defaults to 'previous_commit')
    const gitDiffSelect = document.getElementById("settings-git-diff-mode");
    if (gitDiffSelect) gitDiffSelect.value = s.git_diff_mode || 'previous_commit';

    // Git scan interval, in seconds; "0" means the user scans manually.
    const gitPollSelect = document.getElementById("settings-git-poll-interval");
    if (gitPollSelect) {
        const secs = String(s.git_poll_interval_s ?? '120');
        // An interval set outside the presets (by hand, or by an older build)
        // still needs to show as itself rather than silently snapping.
        if (!Array.from(gitPollSelect.options).some(o => o.value === secs)) {
            const opt = new Option(`Every ${secs} seconds`, secs);
            gitPollSelect.add(opt);
        }
        gitPollSelect.value = secs;
    }

    // Run agents at lower priority (#3437, a checkbox since #3712): on unless set to normal.
    const agentLowPriority = document.getElementById("settings-agent-low-priority");
    if (agentLowPriority) agentLowPriority.checked = s.agent_priority !== 'normal';
}

/** The "Unsaved changes" note in Settings' footer (UI audit #5589, finding 27): shown while the form differs from what it held when it finished opening (or was last saved), or a database encryption or terminal backend or wake choice is pending. The baseline is the form's own payload, not the saved settings: a new install has no saved settings, so everything would differ. Changes to the custom models and the update switches are saved too but are not counted here. */
let _formAtOpen = null;
let _payloadAtOpen = null; // the same snapshot as an object: the Save toast names only the rows that differ from it (a first Save also writes defaults the person never touched, #7041)
const formKey = () => { try { return JSON.stringify(readSettingsForm().payload); } catch { return null; } };
const payloadNow = () => { try { return readSettingsForm().payload; } catch { return null; } };
/** The part of a saved payload the person changed since the dialog opened (or was last saved). */
const changedSince = payload => Object.fromEntries(Object.entries(payload).filter(([k, v]) => !_payloadAtOpen || JSON.stringify(_payloadAtOpen[k]) !== JSON.stringify(v)));
export async function refreshUnsavedNote() {
    const note = document.getElementById('settings-unsaved');
    if (!note) return;
    let dirty = _formAtOpen !== null && formKey() !== _formAtOpen;
    if (!dirty && _formAtOpen !== null) { dirty = databaseEncryptionChanged(); }
    if (!dirty && _formAtOpen !== null) { try { dirty = await importOnce('./terminal_backend.js').then(tb => tb.hasUnsavedBackendChange(), () => false); } catch { dirty = false; } }
    if (note.hidden === dirty) note.hidden = !dirty;
    // A note, not a live region (#6656): while there are unsaved changes the Save button is described by it, so it is read when the person reaches Save, where it matters, and not announced on every edit.
    const save = document.getElementById('settings-save-btn');
    if (save) { if (dirty) { if (save.getAttribute('aria-describedby') !== 'settings-unsaved') save.setAttribute('aria-describedby', 'settings-unsaved'); } else if (save.hasAttribute('aria-describedby')) save.removeAttribute('aria-describedby'); }
}

export async function showSettingsModal(group) {
    // Focus moves in on open, Tab stays inside, and focus comes back on close (accessibility pass).
    attachDialogFocus(document.getElementById('settings-modal'));

    importOnce('./terminal_backend.js').then(m => m.loadTerminalBackendRow()).catch(() => {}); // #3634
    importOnce('./worktrees_panel.js').then(m => m.loadWorktrees()).catch(() => {}); // the kept worktrees, in Storage and cleanup (#6743)
    importOnce('./security_settings.js').then(m => m.loadSecurityRow()).catch(() => {}); // #3948
    const s = state.settings || {};
    // What this dialog opens with: Save sends only the fields that end up different from it.
    _settingsAtOpen = { ...s };
    // The CLI path fields are filled later in this function (after awaited requests): until then they are empty, and a Save
    // made in that time (a suite that does not wait for the dialog) must not read them as "cleared".
    for (const id of Object.values(CLI_PATH_FIELDS)) { const el = document.getElementById(id); if (el) el.dataset.filled = ''; }
    // The machine-only parts of this dialog (privacy, remote access, database encryption, connecting to a hub) do not
    // exist on the hub: the cloud dialog shows only the per-user settings (applyCloudSettingsDialog below).
    await _fillMachineSettings(s);

    await _fillThemeSelect(s);

    _fillDefaults(s);

    _fillApiKey();

    await loadTelemetryInstallID();

    _fillCliAndModels(s);

    _fillTerminalAndBrowserPrefs(s);

    _fillFilesAndGit(s);

    // A partner build's name (Coral has no paid licence, #3221)
    if (!isCloud()) _loadTierBranding();

    // App version
    fetch('/api/system/status').then(r => r.json()).then(d => {
        const el = document.getElementById('app-version-label');
        if (el && d.version) el.textContent = 'Version ' + d.version;
    }).catch(() => {});

    if (isCloud()) applyCloudSettingsDialog();

    openSettingsGroup(typeof group === 'string' ? group : undefined); // the group last used here, or the one asked for
    const modalEl = document.getElementById('settings-modal');
    if (!modalEl._unsavedWired) { modalEl._unsavedWired = true; for (const type of ['input', 'change']) modalEl.addEventListener(type, () => { refreshUnsavedNote(); }); }
    _formAtOpen = formKey(); // the form as it finished opening
    _payloadAtOpen = payloadNow();
    refreshUnsavedNote();
    document.getElementById("settings-modal").style.display = "flex";
}

async function _loadTierBranding() {
    try {
        const resp = await fetch('/api/system/status');
        if (!resp.ok) return;
        const data = await resp.json();
        const branding = document.getElementById('tier-branding-label');
        if (branding && data.tier_name === 'dropboxers') {
            branding.textContent = 'Coral For Dropbox';
            branding.style.display = '';
        }
    } catch { /* non-critical */ }
}

export function hideSettingsModal() {
    document.getElementById("settings-modal").style.display = "none";
    if (!isCloud()) importOnce('./custom_models.js').then(m => m.discardCustomModels()).catch(() => {}); // unsaved custom models are dropped with the dialog (#5480)
}

/** What the dialog's controls hold: the payload to save (only what the person changed, and on the hub only the per-user keys) and the values the page applies once it is saved. */
function readSettingsForm() {
    const themeValue = document.getElementById("settings-theme")?.value || "dark";
    const engineName = document.getElementById("settings-renderer-select").value;
    const agentType = document.getElementById("settings-agent-type")?.value || "claude";
    const permissionMode = document.getElementById("settings-permission-mode")?.value || "auto";
    const workingDir = document.getElementById("settings-working-dir")?.value.trim() || "";
    const cliPathClaude = document.getElementById("settings-cli-path-claude")?.value.trim() || "";
    const cliPathCodex = document.getElementById("settings-cli-path-codex")?.value.trim() || "";
    const cliPathAgy = document.getElementById("settings-cli-path-agy")?.value.trim() || "";
    const cliPathPi = document.getElementById("settings-cli-path-pi")?.value.trim() || "";
    const defaultModelClaude = document.getElementById("settings-default-model-claude")?.value.trim() || "";
    const defaultThinkingClaude = document.getElementById("settings-default-thinking-claude")?.value || "";
    const defaultModelCodex = document.getElementById("settings-default-model-codex")?.value.trim() || "";
    const defaultModelAgy = document.getElementById("settings-default-model-agy")?.value.trim() || "";
    const defaultModelPi = document.getElementById("settings-default-model-pi")?.value.trim() || "";
    const fitPaneWidth = document.getElementById("settings-fit-pane-width")?.checked || false;
    const notifyNeedsInput = document.getElementById("settings-notify-needs-input")?.checked || false;
    const terminalFontSize = document.getElementById("settings-terminal-font-size")?.value || String(defaultTerminalFont());
    const terminalScrollback = document.getElementById("settings-terminal-scrollback")?.value || "20000";
    const checkUpdates = document.getElementById("settings-check-updates")?.checked ?? true;
    const showScrollbars = document.getElementById("settings-show-scrollbars")?.checked ?? true;
    const fileSearchMode = document.getElementById("settings-file-search-mode")?.value || "directory";
    const fileSearchLimit = document.getElementById("settings-file-search-limit")?.value || "500";
    const gitDiffMode = document.getElementById("settings-git-diff-mode")?.value || "branch_point";
    const gitPollIntervalS = document.getElementById("settings-git-poll-interval")?.value ?? "120";
    // The server takes only "low" or "normal" (#3470): a value stored before that check is sent as low.
    const lowBox = document.getElementById("settings-agent-low-priority");
    const agentPriority = lowBox ? (lowBox.checked ? "low" : "normal") : (state.settings?.agent_priority === "normal" ? "normal" : "low");
    const telemetryEnabled = document.getElementById("settings-telemetry-enabled")?.checked ?? settingBool(state.settings?.telemetry_enabled, true);
    const boardAutoAttach = document.getElementById("settings-board-auto-attach")?.checked ?? settingBool(state.settings?.board_auto_attach || undefined, false);
    const nudgeClaudeArtifacts = document.getElementById("settings-nudge-claude-artifacts")?.checked ?? settingBool(state.settings?.nudge_claude_artifacts || undefined, true);
    const boardHealthMonitor = document.getElementById("settings-board-health-monitor")?.checked ?? settingBool(state.settings?.board_health_monitor || undefined, true);
    const autoResume = document.getElementById("settings-auto-resume")?.checked ?? settingBool(state.settings?.auto_resume_after_api_error || undefined, false);
    const autoGoals = document.getElementById("settings-auto-goals")?.checked ?? settingBool(state.settings?.auto_goals || undefined, true);
    // When CORAL_DEBUG_INPUT/--debug-input force the log on, the checkbox is shown checked and
    // disabled: keep the saved setting rather than saving that forced state (#3256).
    const debugInputBox = document.getElementById("settings-debug-input-log");
    const debugInputLog = debugInputBox && !debugInputBox.disabled ? debugInputBox.checked : settingBool(state.settings?.debug_input_log, false);
    const remoteAccessEnabled = document.getElementById("settings-remote-access-enabled")?.checked ?? settingBool(state.settings?.remote_access_enabled, false);

    // Parse theme selection — "custom:<name>" or built-in "dark"/"light"/"system"
    let theme, customTheme;
    if (themeValue.startsWith("custom:")) {
        customTheme = themeValue.slice(7);
        theme = "dark"; // will be overridden by the custom theme's base
    } else {
        theme = themeValue;
        customTheme = "";
    }

    let payload = {
        theme: theme,
        custom_theme: customTheme,
        default_renderer: engineName,
        default_agent_type: agentType,
        default_permission_mode: permissionMode,
        default_working_dir: workingDir,
        fit_pane_width: fitPaneWidth,
        notify_needs_input: notifyNeedsInput,
        terminal_font_size: terminalFontSize,
        terminal_scrollback: terminalScrollback,
        show_scrollbars: showScrollbars,
        cli_path_claude: cliPathClaude,
        cli_path_codex: cliPathCodex,
        cli_path_agy: cliPathAgy,
        cli_path_gemini: cliPathAgy,
        cli_path_pi: cliPathPi,
        default_model_claude: defaultModelClaude,
        default_thinking_claude: defaultThinkingClaude,
        default_model_codex: defaultModelCodex,
        default_model_agy: defaultModelAgy,
        default_model_gemini: defaultModelAgy,
        default_model_pi: defaultModelPi,
        file_search_mode: fileSearchMode,
        file_search_limit: fileSearchLimit,
        git_diff_mode: gitDiffMode,
        git_poll_interval_s: gitPollIntervalS,
        agent_priority: agentPriority,
        telemetry_enabled: telemetryEnabled,
        board_auto_attach: boardAutoAttach,
        nudge_claude_artifacts: nudgeClaudeArtifacts,
        board_health_monitor: boardHealthMonitor,
        auto_resume_after_api_error: autoResume,
        auto_goals: autoGoals,
        ...artifactStoreMaxSetting(),
        ...receivedMaxSetting(),
        ...retentionSettings(),
        debug_input_log: debugInputLog,
        remote_access_enabled: remoteAccessEnabled,
    };

    // Only what the person changed since the dialog opened: a field this dialog never touched (a CLI path another
    // tab saved meanwhile) is not written back with the value this page opened with.
    payload = changedSettings(dropUnfilledCLIPaths(payload), _settingsAtOpen, themeValue, editedCLIPathKeys(), defaultThemeChoice());
    // The hub accepts only the per-user keys (anything else refuses the whole request); the machine ones are hidden.
    if (isCloud()) payload = filterSettingsPayload(payload).kept;

    return { themeValue, themeSaved: 'theme' in payload, theme, customTheme, payload, checkUpdates, fitPaneWidth, terminalFontSize, terminalScrollback, showScrollbars, gitDiffMode, remoteAccessEnabled, debugInputLog };
}

/** Sends the payload (nothing, when nothing changed) and takes it into state.settings and the privacy status. Returns true when there was nothing to save; throws when the server refuses. */
async function saveSettings({ payload, remoteAccessEnabled, debugInputLog }) {
    // Nothing to save when no setting changed and neither the database encryption choice nor the backend and wake choices did (they are saved apart).
    const sideChange = databaseEncryptionChanged() || await importOnce('./terminal_backend.js').then(tb => tb.hasUnsavedBackendChange(), () => true);
    const nothingToSave = Object.keys(payload).length === 0 && !sideChange;
    if (!isCloud()) await saveDatabaseEncryptionSettings();
    // Nothing changed: nothing to write.
    const response = Object.keys(payload).length === 0 ? { ok: true } : await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
    });
    if (!response.ok) {
        // A refused value (e.g. a CLI path that isn't an executable file, #3364) says why.
        const data = await response.json().catch(() => null);
        const err = new Error(`settings save failed (${response.status})`);
        if (data && typeof data.error === 'string') err.userMessage = data.error;
        throw err;
    }
    state.settings = { ...state.settings, ...payload };
    if (_settingsAtOpen) _settingsAtOpen = { ..._settingsAtOpen, ...payload }; // the dialog stays open on a cancelled backend change
    if (!isCloud() && state.privacyStatus) {
        state.privacyStatus.remote_access_enabled = remoteAccessEnabled;
        state.privacyStatus.remote_access_restart_required = state.privacyStatus.remote_access_effective !== remoteAccessEnabled;
    }
    if (!isCloud() && state.privacyStatus) {
        const log = state.privacyStatus.debug_input_log || {};
        state.privacyStatus.debug_input_log = { ...log, enabled: log.forced === true || debugInputLog };
        // The server knows whether a log file is now kept on disk; refresh from it.
        try {
            const fresh = await fetch('/api/system/privacy');
            if (fresh.ok) {
                const body = await fresh.json();
                if (body && body.debug_input_log) state.privacyStatus.debug_input_log = body.debug_input_log;
            }
        } catch { /* keep the local guess */ }
    }
    if (!isCloud()) renderRemoteAccessStatus(state.privacyStatus);
    if (!isCloud()) renderDebugInputLogStatus(state.privacyStatus);
    return nothingToSave;
}

/** Puts the saved settings on the page: the theme, the open session's capture, the terminal, the scrollbars, the changed files. */
async function applySettingsEffects({ themeSaved, theme, customTheme, fitPaneWidth, terminalFontSize, terminalScrollback, showScrollbars, gitDiffMode }, oldGitDiffMode) {
    // Defaults may have changed — force next ACF open to re-fetch.
    _invalidateDefaultModels();

    // The theme only when the person changed it (#6114): a Save that left the control alone changes nothing on the page and writes no theme.
    if (themeSaved) {
        // Clear any previously applied custom CSS variables
        clearCustomThemeVars();

        if (customTheme) {
            // Load and apply the custom theme (sets base + variables)
            await applyCustomThemeByName(customTheme);
        } else {
            // Apply built-in theme
            applyTheme(theme);
        }
    }

    // If a live session is selected, force re-render with new default
    if (state.currentSession?.session_id) {
        const el = document.getElementById("pane-capture");
        if (el && el._lastCapture) {
            renderCaptureText(el, el._lastCapture);
        }
    }

    // Trigger pane width sync if the setting was just enabled
    if (fitPaneWidth) {
        syncPaneWidth();
    }

    // Apply font size and scrollback to existing terminal
    const term = getTerminal();
    if (term) {
        term.options.fontSize = terminalFontFor(terminalFontSize);
        term.options.scrollback = parseInt(terminalScrollback, 10) || 20000;
        const { fitTerminal } = await importOnce('./xterm_renderer.js');
        fitTerminal();
    }

    // Apply scrollbar visibility
    document.body.classList.toggle('no-scrollbars', !showScrollbars);

    // Refresh changed files if git diff mode changed
    if (gitDiffMode !== oldGitDiffMode) {
        refreshChangedFiles();
    }
}

export async function applySettings() {
    const form = readSettingsForm();
    browserLocal.set("coral-update-check-enabled", form.checkUpdates ? "true" : "false");
    const oldGitDiffMode = state.settings?.git_diff_mode || 'previous_commit';

    try {
        const nothingToSave = await saveSettings(form);
        await applySettingsEffects(form, oldGitDiffMode);
        const updatesSaved = updatesUiOn() ? await importOnce('./update_settings.js').then(m => m.applyUpdatePrefs()) : false; // the update switch and channel (#5275, #5558)
        const modelsSaved = isCloud() ? false : await importOnce('./custom_models.js').then(m => m.applyCustomModels()); // the custom models (#5480)
        if (modelsSaved) { _invalidateAgentModels(); (await importOnce('./model_labels.js')).clearModelsCache(); }
        // the rows by name (#7041), not "Settings saved" for every change; what was saved apart (custom models, the update choices) is named too
        const extra = [modelsSaved && 'Custom models', updatesSaved && 'Updates'].filter(Boolean);
        showToast(nothingToSave && !extra.length ? NOTHING_TO_SAVE : savedWords(changedSince(form.payload), { extra, restart: !!state.privacyStatus?.remote_access_restart_required }));
        _formAtOpen = formKey(); _payloadAtOpen = payloadNow(); refreshUnsavedNote();
    } catch (e) {
        showToast(e && e.userMessage ? `Failed to save settings: ${e.userMessage}` : "Failed to save settings", true);
        return;
    }

    // A changed terminal backend asks first and applies at the next start (#3634).
    try {
        const tb = await importOnce('./terminal_backend.js');
        if (!(await tb.applyTerminalBackendChange())) return; // cancelled: the dialog stays open
    } catch { /* the rest is saved */ }
    hideSettingsModal();
}

// "Trust Coral's hooks in Codex" (#153): written only by its own route on this computer (never PUT /api/settings, never
// through Coral Hub), so it is saved as soon as it changes and is not part of the Save payload. The line under it says
// whether Coral trusts its hooks now, and why not (a tools folder this user can change, a platform other than Windows).
async function loadCodexTrustHooks() {
    const box = document.getElementById('settings-codex-trust-hooks');
    if (!box) return;
    let status = document.getElementById('settings-codex-trust-hooks-status');
    if (!status) {
        status = document.createElement('span');
        status.id = 'settings-codex-trust-hooks-status';
        status.className = 'toggle-desc';
        document.getElementById('settings-codex-trust-hooks-desc')?.after(status);
    }
    const show = (s) => {
        box.checked = !!s.trust;
        status.textContent = s.trusted ? "Now: new Codex agents start with Coral's hooks trusted."
            : (s.trust && s.why ? `Now: Codex will ask (${s.why}).` : '');
    };
    try {
        const r = await fetch('/api/system/codex-trust-hooks');
        if (r.ok) show(await r.json());
    } catch { /* the box stays as drawn */ }
    if (box.dataset.wired) return;
    box.dataset.wired = '1';
    box.addEventListener('change', async () => {
        try {
            const r = await fetch('/api/system/codex-trust-hooks', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ trust: box.checked }),
            });
            if (!r.ok) throw new Error(String(r.status));
            show(await r.json());
        } catch {
            box.checked = !box.checked;
            showToast("Couldn't change whether Coral trusts its hooks in Codex", 'error');
        }
    });
}
