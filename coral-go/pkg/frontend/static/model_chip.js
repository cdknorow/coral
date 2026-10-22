/* The model chip and the thinking chip by the composer (#4969, split in two by #5118). They say the model and level the agent ACTUALLY runs
 * (the transcript's, from the row: model, thinking_actual), or the level it was started with, marked as requested, until a reply shows one.
 * The model chip's menu offers the models the agent's CLI lists; the thinking chip's, the levels that model takes (no chip for a CLI with none). Choosing one on a CLI with a live switch (Claude) asks the server to
 * type the CLI's own command (POST /api/sessions/live/{name}/model); the chip then says "Switching…" until the transcript confirms, and
 * says why when it does not (model_switch). A CLI with no live switch shows "Apply with restart": the Restart dialog opens with the
 * choice filled in. Typing in the composer is never held up by any of it. */
import { serverFetch } from './server_base.js';
import { escapeHtml, escapeAttr, showToast } from './utils.js';
import { importOnce } from './import_once.js';
import { modelLabel, readModelsCache, writeModelsCache, levelName } from './model_labels.js';
import { setText, setHidden, setTitle, setData, setAttr } from './dom_write.js';

/** The agent types whose CLI takes /model and /effort in a running session (the server's liveSwitchTypes). */
const LIVE_SWITCH_TYPES = new Set(['claude']);

let _cur = null;            // { session, server } the chip shows
let _data = null;           // the models list for the current agent type: { models, thinking }
let _dataKey = '';
let _local = null;          // { until, text, kind }: "switching" shown on the chip that was changed, from the click until the row says so
let _open = '';             // which menu is open: 'model', 'thinking' or ''
let _loading = false;       // the list has not arrived (or the server is still asking the CLI for it): the menu says so instead of "No models"
let _loadToken = 0;
const LOAD_DELAYS_MS = [600, 1000, 1500, 2500, 4000, 4000, 4000, 4000, 4000, 4000]; // about 30 s of asking while the server says it is refreshing

const $ = id => document.getElementById(id);

const KINDS = ['model', 'thinking'];
const chipId = k => (k === 'model' ? 'model-chip' : 'thinking-chip');

function parts() {
    const o = { bar: $('model-chip-bar'), note: $('model-chip-note') };
    for (const k of KINDS) o[k] = { chip: $(chipId(k)), text: $(chipId(k) + '-text'), menu: $(chipId(k) + '-menu'), wrap: $(chipId(k) + '-wrap') };
    return o;
}

/** What the chips say for a row: { model: {text, state}, thinking: {text, state, shown}, note } (state: '', 'pending', 'failed', 'requested'; note is plain text). */
export function chipView(s, server) {
    const base = modelLabel(s.agent_type, s.model, server) || 'Model';
    const sw = s.model_switch;
    const lvl = s.thinking_actual ? levelName(s.thinking_actual) : (s.thinking ? levelName(s.thinking) : '');
    const view = { model: { text: base, state: '' }, thinking: { text: lvl || 'Default', state: !s.thinking_actual && s.thinking ? 'requested' : '' }, note: '' };
    if (view.thinking.state === 'requested') view.note = 'The level it was started with: the agent has not confirmed one yet.';
    if (sw && sw.state === 'pending') {
        // The chip(s) the switch concerns say "Switching to …"; the other keeps saying what runs.
        const toModel = sw.model ? modelLabel(s.agent_type, sw.model, server).replace(/^[^·]*· /, '') : '';
        const toLevel = sw.thinking === undefined || sw.thinking === null ? '' : (sw.thinking ? levelName(sw.thinking) : 'Default');
        if (toModel || !toLevel) view.model = { text: `Switching to ${toModel || 'the new setting'}…`, state: 'pending' };
        if (toLevel) view.thinking = { text: `Switching to ${toLevel}…`, state: 'pending' };
        view.note = sw.after_reply ? 'Switches after this reply.' : (sw.thinking ? 'The level shows once the agent has replied.' : '');
        return view;
    }
    if (_local && Date.now() < _local.until && !(sw && sw.state !== 'pending')) { view[_local.kind] = { text: _local.text, state: 'pending' }; return view; }
    if (sw && sw.state === 'failed') {
        const k = sw.model ? 'model' : (sw.thinking !== undefined && sw.thinking !== null ? 'thinking' : '');
        for (const kk of k ? [k] : KINDS) view[kk].state = 'failed';
        view.note = sw.message || 'The change was not made.';
    }
    return view;
}

/** Whether the row gets a thinking chip: Claude always (its levels load with the menu); another CLI when it reports a level or its list has levels. */
function _hasLevels(s) {
    return LIVE_SWITCH_TYPES.has(s.agent_type) || !!(s.thinking_actual || s.thinking) || (_dataKey === `${_cur && _cur.server}:${s.agent_type}` && !!_data && _data.thinking.length > 0);
}

export function updateModelChip(session, server) {
    _cur = session ? { session, server } : null;
    const p = parts();
    if (!p.bar || !p.model.chip) return;
    wireModelChip();
    const s = session;
    const show = !!s && s.agent_type && s.agent_type !== 'terminal' && !s.sleeping && !s.archived;
    setHidden(p.bar, !show);
    if (!show) { _closeMenu(); return; }
    const v = chipView(s, server);
    const hasLevels = _hasLevels(s);
    setHidden(p.thinking.wrap, !hasLevels);
    if (!hasLevels && _open === 'thinking') _closeMenu();
    const labels = { model: `Model: ${v.model.text}`, thinking: `Thinking level: ${v.thinking.text}` };
    for (const k of KINDS) {
        const { chip, text } = p[k];
        setText(text, v[k].text);                  // plain text, never markup (a failure message is the CLI's own output)
        setData(chip, 'state', v[k].state);
        setTitle(chip, v[k].state === 'requested' ? v.note : '');
        setAttr(chip, 'aria-label', labels[k]);    // the name holds what the chip shows (label in name), not a fixed phrase that hides it
    }
    setText(p.note, v.note); // the live region stays in the page, empty and out of the way (css), so a note that appears is announced
    const st = v.model.state === 'failed' || v.thinking.state === 'failed' ? 'failed' : (v.thinking.state === 'requested' ? 'requested' : '');
    setData(p.note, 'state', st);
    if (_open) _renderMenu(_open);
}

// ── The menu ─────────────────────────────────────────────────────────────

/** One answer of the list route: { models, thinking, refreshing }, or null when it could not be read. refreshing: the server is still asking the CLI (it answers with no models at first). */
async function _askList(agentType) {
    try {
        const resp = await serverFetch(_cur.server, `/api/agents/models?type=${encodeURIComponent(agentType)}`);
        if (!resp.ok) return null;
        const d = await resp.json();
        return d && Array.isArray(d.models) ? { models: d.models, thinking: Array.isArray(d.thinking) ? d.thinking : [], refreshing: d.refreshing === true, raw: d } : null;
    } catch { return null; }
}

/** The models list for the open agent. Only an answer that HAS models is remembered for the session (and cached in the browser): an empty one is the server still asking the CLI ("refreshing": the menu says
 *  "Loading models…" and asks again with a growing pause, up to about 30 seconds) or a final nothing, and neither is kept, so the next open asks again (a CLI that lists nothing at first, like Codex, showed
 *  "No models to choose from" for good). */
async function _loadList() {
    const s = _cur && _cur.session;
    if (!s) return null;
    const key = `${_cur.server}:${s.agent_type}`;
    if (_dataKey === key && _data) return _data;
    if (_dataKey !== key) { _data = null; _dataKey = ''; }
    const cached = readModelsCache(_cur.server, s.agent_type);
    if (cached) { _data = { models: cached.models, thinking: [] }; _dataKey = key; }
    const token = ++_loadToken;
    try {
        for (let i = 0; ; i++) {
            const answer = await _askList(s.agent_type);
            if (token !== _loadToken || !_cur || `${_cur.server}:${_cur.session.agent_type}` !== key) return _data;
            if (!answer) return _data; // it could not be read: keep what there is (the cached list)
            if (answer.models.length) {
                writeModelsCache(_cur.server, s.agent_type, answer.raw);
                _data = { models: answer.models, thinking: answer.thinking };
                _dataKey = key;
                return _data;
            }
            if (_data && _data.models.length) return _data; // a cached list stands while the server has none yet
            _data = { models: [], thinking: answer.thinking }; _dataKey = ''; // shown, not remembered
            if (!answer.refreshing || i >= LOAD_DELAYS_MS.length) return _data;
            await new Promise(r => setTimeout(r, LOAD_DELAYS_MS[i]));
            if (token !== _loadToken || !_open) return _data; // the menu was closed: stop asking; the next open asks again
        }
    } finally {
        if (token === _loadToken) { _loading = false; if (_open) _renderMenu(_open); }
    }
}

/** The levels the switcher offers for a model of a catalog reply `data` ({ thinking, models }): the type's levels, narrowed to the model's own efforts. A custom model (#5480) lists exactly the levels its owner ticked, so none ticked means no level choice. */
export function levelsFor(data, modelId) {
    const levels = (data && data.thinking) || [];
    const hit = ((data && data.models) || []).find(m => m.value === modelId);
    const efforts = hit && hit.custom === true ? (Array.isArray(hit.efforts) ? hit.efforts : []) : (hit && Array.isArray(hit.efforts) && hit.efforts.length ? hit.efforts : null);
    return efforts ? levels.filter(l => efforts.includes(l)) : levels;
}

function _levelsFor(modelId) { return levelsFor(_data, modelId); }

function _renderMenu(kind) {
    const { menu } = parts()[kind];
    if (!menu || !_cur) return;
    const s = _cur.session;
    const live = LIVE_SWITCH_TYPES.has(s.agent_type);
    const curLevel = s.thinking_actual || s.thinking || '';
    const how = live ? '' : '<span class="model-chip-how">Apply with restart</span>';
    const item = (kind, value, label, checked) =>
        `<button type="button" role="menuitemradio" aria-checked="${checked}" class="model-chip-item${checked ? ' checked' : ''}" data-${kind}="${escapeAttr(value)}">`
        + `<span class="model-chip-item-label">${escapeHtml(label)}</span>${checked ? '<span class="material-icons" aria-hidden="true">check</span>' : ''}</button>`;
    let html = '';
    if (kind === 'model') {
        const models = (_data && _data.models) || [];
        if (models.length) {
            html = `<div class="model-chip-group" role="group" aria-label="Model"><div class="model-chip-head">Model ${how}</div>`
                + models.map(m => item('model', m.value, (m.display_name || m.value) + (m.custom === true ? ' (custom)' : ''), m.value === s.model)).join('') + '</div>';
        }
    } else {
        const levels = _levelsFor(s.model);
        if (levels.length) {
            html = `<div class="model-chip-group" role="group" aria-label="Thinking level"><div class="model-chip-head">Thinking ${how}</div>`
                + item('level', '', 'Default', !curLevel)
                + levels.map(l => item('level', l, levelName(l), l === curLevel)).join('') + '</div>';
        }
    }
    if (!html) {
        // Loading says so once (a status the screen reader reads when it appears); the same words are not drawn again by the feed's updates (below)
        html = _loading ? `<div class="model-chip-empty" role="status">${kind === 'model' ? 'Loading models…' : 'Loading thinking levels…'}</div>`
            : `<div class="model-chip-empty">${kind === 'model' ? 'No models to choose from for this agent.' : 'No thinking levels to choose from for this model.'}</div>`;
    }
    if (menu.dataset.sig === html) return; // nothing changed: the feed's ticks do not redraw the menu (a redraw would announce the loading line again and drop the focused item)
    menu.dataset.sig = html;
    menu.innerHTML = html;
}

async function _openMenu(kind) {
    const p = parts();
    if (!p[kind].chip || !p[kind].menu || !_cur) return;
    if (_open && _open !== kind) _closeMenu(); // one menu at a time
    _open = kind;
    _loading = !(_data && _data.models.length && _dataKey === `${_cur.server}:${_cur.session.agent_type}`);
    p[kind].menu.hidden = false;
    p[kind].chip.setAttribute('aria-expanded', 'true');
    _renderMenu(kind);
    await _loadList();
    updateModelChip(_cur && _cur.session, _cur && _cur.server); // the list's display names are cached now: the chips say them
    if (_open === kind) { _renderMenu(kind); const first = p[kind].menu.querySelector('.model-chip-item'); if (first) first.focus(); }
}

function menuHasFocus() {
    const p = parts();
    return KINDS.find(k => !!p[k].menu && p[k].menu.contains(document.activeElement)) || '';
}

function _closeMenu() {
    const p = parts();
    _open = ''; _loading = false;
    for (const k of KINDS) {
        if (p[k].menu) { p[k].menu.hidden = true; delete p[k].menu.dataset.sig; }
        if (p[k].chip) p[k].chip.setAttribute('aria-expanded', 'false');
    }
}

// ── Applying a choice ────────────────────────────────────────────────────

async function _apply(change) {
    const s = _cur && _cur.session;
    if (!s) return;
    const fromMenu = menuHasFocus();
    _closeMenu();
    if (fromMenu) parts()[fromMenu].chip?.focus(); // the item that had the keyboard is gone with the menu: focus goes back to its own chip
    const name = s.name;
    if (!LIVE_SWITCH_TYPES.has(s.agent_type)) { _viaRestart(change); return; }
    const body = { session_id: s.session_id, agent_type: s.agent_type };
    if (change.model !== undefined) body.model = change.model;
    if (change.thinking !== undefined) body.thinking = change.thinking;
    const target = change.model !== undefined ? modelLabel(s.agent_type, change.model, _cur.server).replace(/^[^·]*· /, '') : (change.thinking ? levelName(change.thinking) : 'Default');
    _local = { until: Date.now() + 2000, text: `Switching to ${target}…`, kind: change.model !== undefined ? 'model' : 'thinking' }; // until the feed's row says it (a tick or two)
    updateModelChip(s, _cur.server);
    setTimeout(() => { if (_cur) updateModelChip(_cur.session, _cur.server); }, 2100);
    try {
        const resp = await serverFetch(_cur.server, `/api/sessions/live/${encodeURIComponent(name)}/model`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
        });
        if (resp.status === 202) return; // the row's model_switch takes over
        _local = null;
        const out = await resp.json().catch(() => ({}));
        if (resp.status === 409 && out.code === 'needs_restart') { _viaRestart(change); return; }
        showToast(out.error || 'The change was not made.', true);
    } catch {
        _local = null;
        showToast('Could not reach Coral to make the change.', true);
    }
    updateModelChip(s, _cur && _cur.server);
}

/** "Apply with restart": the Restart dialog, opened with the choice filled in (the dialog is the confirmation). */
async function _viaRestart(change) {
    if (typeof window.restartSession !== 'function' || !_cur) return;
    const running = _cur.session;
    window.restartSession();
    for (let i = 0; i < 50 && !document.querySelector('#restart-acf .acf-model'); i++) await new Promise(r => setTimeout(r, 100));
    const { setAgentConfig, getAgentConfig } = await importOnce('./agent_config_form.js');
    const cfg = getAgentConfig('restart-acf');
    // The choice, over what the agent runs now: a level chosen for a model it is not yet on belongs to the model it is on.
    setAgentConfig('restart-acf', {
        ...cfg,
        model: change.model !== undefined ? change.model : (running.model || cfg.model),
        ...(change.thinking !== undefined ? { thinking: change.thinking } : {}),
    });
}

/** Wire the chips once: open and close each menu, choose an item. */
export function wireModelChip() {
    const p = parts();
    const first = p.model.chip;
    if (!first || first.dataset.wired) return;
    first.dataset.wired = '1';
    for (const k of KINDS) {
        const { chip, menu } = p[k];
        chip.addEventListener('click', () => { if (_open === k) { _closeMenu(); } else { _openMenu(k); } });
        menu.addEventListener('click', e => {
            const b = e.target.closest('.model-chip-item');
            if (!b) return;
            if (b.dataset.model !== undefined) _apply({ model: b.dataset.model });
            else if (b.dataset.level !== undefined) _apply({ thinking: b.dataset.level });
        });
    }
    document.addEventListener('click', e => { if (_open && !e.target.closest('#model-chip-bar')) _closeMenu(); });
    document.addEventListener('keydown', e => {
        if (!_open) return;
        const { chip, menu } = p[_open];
        if (e.key === 'Escape') { _closeMenu(); chip.focus(); return; }
        if (e.key === 'Tab' && menu.contains(document.activeElement)) { _closeMenu(); return; } // the menu is not a place to tab through: it closes and focus goes on from where it was
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Home' || e.key === 'End') {
            const items = [...menu.querySelectorAll('.model-chip-item')];
            if (!items.length) return;
            const i = items.indexOf(document.activeElement);
            const to = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : (i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length;
            items[to].focus();
            e.preventDefault();
        }
    });
}
