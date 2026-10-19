/* The model and thinking-level chip by the composer (#4969). It says the model and level the agent ACTUALLY runs (the transcript's, from
 * the row: model, thinking_actual), or the level it was started with, marked as requested, until a reply shows one. Its menu offers the
 * models the agent's CLI lists and the levels that model takes. Choosing one on a CLI with a live switch (Claude) asks the server to
 * type the CLI's own command (POST /api/sessions/live/{name}/model); the chip then says "Switching…" until the transcript confirms, and
 * says why when it does not (model_switch). A CLI with no live switch shows "Apply with restart": the Restart dialog opens with the
 * choice filled in. Typing in the composer is never held up by any of it. */
import { serverFetch } from './server_base.js';
import { escapeHtml, escapeAttr, showToast } from './utils.js';
import { importOnce } from './import_once.js';
import { modelLabel, readModelsCache, writeModelsCache } from './model_labels.js';
import { setText, setHidden, setTitle, setData, setAttr } from './dom_write.js';

/** The agent types whose CLI takes /model and /effort in a running session (the server's liveSwitchTypes). */
const LIVE_SWITCH_TYPES = new Set(['claude']);
const LEVEL_NAMES = { low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' };
const levelName = l => LEVEL_NAMES[l] || l;

let _cur = null;            // { session, server } the chip shows
let _data = null;           // the models list for the current agent type: { models, thinking }
let _dataKey = '';
let _local = null;          // { until, text }: "switching" shown from the click until the row says so
let _open = false;

const $ = id => document.getElementById(id);

function parts() {
    return { bar: $('model-chip-bar'), chip: $('model-chip'), text: $('model-chip-text'), note: $('model-chip-note'), menu: $('model-chip-menu') };
}

/** What the chip says for a row: the text, the state ('', 'pending', 'failed', 'requested') and a note (plain text). */
export function chipView(s, server) {
    const base = modelLabel(s.agent_type, s.model, server) || 'Model';
    const sw = s.model_switch;
    if (sw && sw.state === 'pending') {
        const to = [sw.model ? modelLabel(s.agent_type, sw.model, server).replace(/^[^·]*· /, '') : '', sw.thinking === undefined || sw.thinking === null ? '' : (sw.thinking ? levelName(sw.thinking) : 'Default')].filter(Boolean).join(' · ');
        return { text: `Switching to ${to || 'the new setting'}…`, state: 'pending', note: sw.after_reply ? 'Switches after this reply.' : (sw.thinking ? 'The level shows once the agent has replied.' : '') };
    }
    if (_local && Date.now() < _local.until && !(sw && sw.state !== 'pending')) return { text: _local.text, state: 'pending', note: '' };
    const lvl = s.thinking_actual ? levelName(s.thinking_actual) : (s.thinking ? levelName(s.thinking) : '');
    let text = lvl ? `${base} · ${lvl}` : base;
    let st = !s.thinking_actual && s.thinking ? 'requested' : '';
    let note = st === 'requested' ? 'The level it was started with: the agent has not confirmed one yet.' : '';
    if (sw && sw.state === 'failed') { st = 'failed'; note = sw.message || 'The change was not made.'; }
    return { text, state: st, note };
}

export function updateModelChip(session, server) {
    _cur = session ? { session, server } : null;
    const { bar, chip, text, note } = parts();
    if (!bar || !chip) return;
    wireModelChip();
    const s = session;
    const show = !!s && s.agent_type && s.agent_type !== 'terminal' && !s.sleeping && !s.archived;
    setHidden(bar, !show);
    if (!show) { _closeMenu(); return; }
    const v = chipView(s, server);
    setText(text, v.text);                     // plain text, never markup (a failure message is the CLI's own output)
    setData(chip, 'state', v.state);
    setTitle(chip, v.state === 'requested' ? v.note : '');
    setAttr(chip, 'aria-label', `Model and thinking level: ${v.text}`); // the name holds what the chip shows (label in name), not a fixed phrase that hides it
    setText(note, v.note); // the live region stays in the page, empty and out of the way (css), so a note that appears is announced
    setData(note, 'state', v.state);
    if (_open) _renderMenu();
}

// ── The menu ─────────────────────────────────────────────────────────────

async function _loadList() {
    const s = _cur && _cur.session;
    if (!s) return null;
    const key = `${_cur.server}:${s.agent_type}`;
    if (_dataKey === key && _data) return _data;
    const cached = readModelsCache(_cur.server, s.agent_type);
    if (cached) { _data = { models: cached.models, thinking: [] }; _dataKey = key; }
    try {
        const resp = await serverFetch(_cur.server, `/api/agents/models?type=${encodeURIComponent(s.agent_type)}`);
        if (resp.ok) {
            const d = await resp.json();
            if (d && Array.isArray(d.models)) {
                if (d.models.length) writeModelsCache(_cur.server, s.agent_type, d);
                _data = { models: d.models, thinking: Array.isArray(d.thinking) ? d.thinking : [] };
                _dataKey = key;
            }
        }
    } catch { /* keep the cached list */ }
    return _data;
}

function _levelsFor(modelId) {
    const levels = (_data && _data.thinking) || [];
    const hit = ((_data && _data.models) || []).find(m => m.value === modelId);
    const efforts = hit && Array.isArray(hit.efforts) && hit.efforts.length ? hit.efforts : null;
    return efforts ? levels.filter(l => efforts.includes(l)) : levels;
}

function _renderMenu() {
    const { menu } = parts();
    if (!menu || !_cur) return;
    const s = _cur.session;
    const live = LIVE_SWITCH_TYPES.has(s.agent_type);
    const models = (_data && _data.models) || [];
    const levels = _levelsFor(s.model);
    const curLevel = s.thinking_actual || s.thinking || '';
    const how = live ? '' : '<span class="model-chip-how">Apply with restart</span>';
    const item = (kind, value, label, checked) =>
        `<button type="button" role="menuitemradio" aria-checked="${checked}" class="model-chip-item${checked ? ' checked' : ''}" data-${kind}="${escapeAttr(value)}">`
        + `<span class="model-chip-item-label">${escapeHtml(label)}</span>${checked ? '<span class="material-icons" aria-hidden="true">check</span>' : ''}</button>`;
    let html = '';
    if (models.length) {
        html += `<div class="model-chip-group" role="group" aria-label="Model"><div class="model-chip-head">Model ${how}</div>`
            + models.map(m => item('model', m.value, m.display_name || m.value, m.value === s.model)).join('') + '</div>';
    }
    if (levels.length) {
        html += `<div class="model-chip-group" role="group" aria-label="Thinking level"><div class="model-chip-head">Thinking ${how}</div>`
            + item('level', '', 'Default', !curLevel)
            + levels.map(l => item('level', l, levelName(l), l === curLevel)).join('') + '</div>';
    }
    if (!html) html = '<div class="model-chip-empty">No models to choose from for this agent.</div>';
    menu.innerHTML = html;
}

async function _openMenu() {
    const { chip, menu } = parts();
    if (!chip || !menu || !_cur) return;
    _open = true;
    menu.hidden = false;
    chip.setAttribute('aria-expanded', 'true');
    _renderMenu();
    await _loadList();
    updateModelChip(_cur && _cur.session, _cur && _cur.server); // the list's display names are cached now: the chip says them
    if (_open) { _renderMenu(); const first = menu.querySelector('.model-chip-item'); if (first) first.focus(); }
}

function menuHasFocus() {
    const { menu } = parts();
    return !!menu && menu.contains(document.activeElement);
}

function _closeMenu() {
    const { chip, menu } = parts();
    _open = false;
    if (menu) menu.hidden = true;
    if (chip) chip.setAttribute('aria-expanded', 'false');
}

// ── Applying a choice ────────────────────────────────────────────────────

async function _apply(change) {
    const s = _cur && _cur.session;
    if (!s) return;
    const fromMenu = menuHasFocus();
    _closeMenu();
    if (fromMenu) parts().chip?.focus(); // the item that had the keyboard is gone with the menu: focus goes back to the chip
    const name = s.name;
    if (!LIVE_SWITCH_TYPES.has(s.agent_type)) { _viaRestart(change); return; }
    const body = { session_id: s.session_id, agent_type: s.agent_type };
    if (change.model !== undefined) body.model = change.model;
    if (change.thinking !== undefined) body.thinking = change.thinking;
    const target = change.model !== undefined ? modelLabel(s.agent_type, change.model, _cur.server).replace(/^[^·]*· /, '') : (change.thinking ? levelName(change.thinking) : 'Default');
    _local = { until: Date.now() + 2000, text: `Switching to ${target}…` }; // until the feed's row says it (a tick or two)
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

/** Wire the chip once: open and close the menu, choose an item. */
export function wireModelChip() {
    const { chip, menu } = parts();
    if (!chip || chip.dataset.wired) return;
    chip.dataset.wired = '1';
    chip.addEventListener('click', () => { if (_open) { _closeMenu(); } else { _openMenu(); } });
    menu.addEventListener('click', e => {
        const b = e.target.closest('.model-chip-item');
        if (!b) return;
        if (b.dataset.model !== undefined) _apply({ model: b.dataset.model });
        else if (b.dataset.level !== undefined) _apply({ thinking: b.dataset.level });
    });
    document.addEventListener('click', e => { if (_open && !e.target.closest('#model-chip-bar')) _closeMenu(); });
    document.addEventListener('keydown', e => {
        if (!_open) return;
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
