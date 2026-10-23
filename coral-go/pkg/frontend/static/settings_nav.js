/* The Settings dialog's groups: a vertical tab list on the left (a selector at the top when the dialog is narrow), one pane per
 * group, and the explanation under each label behind an (i) button (#4566). The panes are in the dialog's template and every
 * control keeps its id, so the code that fills and saves Settings reads the same elements whichever pane is shown.
 *
 *   showSettingsGroup(id)   open the dialog's pane for a group (general, agents, terminal, notifications, files, cleanup, network, security)
 *
 * No imports from modals.js: the dialog module imports this one. */
import { browserLocal } from './safe_storage.js';
import { storeKey } from './local_store.js';
import { onBuilt } from './lazy_dom.js';
import { updatesUiOn } from './update_flag.js';

const GROUP_KEY = 'coral-settings-group';
const FIRST = 'general';
let infoSeq = 0;

const modalEl = () => document.getElementById('settings-modal');
const panes = () => [...(modalEl()?.querySelectorAll('.settings-pane') || [])];
/** Panes with something to show: Coral Hub's dashboard hides every row a hub user cannot change, and a group with none is left out. */
const shown = () => panes().filter(p => !p.dataset.empty);

function tabFor(id) { return document.getElementById(`settings-tab-${id}`); }

/** Opens the pane for a group; an unknown or empty group opens the first one. Returns the group shown. */
function showSettingsGroup(id, { focus = false, remember = true } = {}) {
    const list = shown();
    const pane = list.find(p => p.dataset.group === id) || list[0];
    if (!pane) return null;
    for (const p of panes()) {
        const on = p === pane;
        p.hidden = !on;
        const tab = tabFor(p.dataset.group);
        if (tab) { tab.setAttribute('aria-selected', String(on)); tab.tabIndex = on ? 0 : -1; if (on) tab.setAttribute('data-initial-focus', ''); else tab.removeAttribute('data-initial-focus'); }
    }
    const pick = document.getElementById('settings-group-select');
    if (pick) pick.value = pane.dataset.group;
    pane.parentElement.scrollTop = 0;
    if (remember) browserLocal.set(storeKey(GROUP_KEY), pane.dataset.group);
    if (focus) tabFor(pane.dataset.group)?.focus();
    document.dispatchEvent(new CustomEvent('coral-settings-group', { detail: { id: pane.dataset.group } })); // the Updates pane draws itself when it is shown (#5275)
    return pane.dataset.group;
}

/** The group Settings opens on: the one last used in this browser. */
export function openSettingsGroup(id) {
    return showSettingsGroup(id || browserLocal.get(storeKey(GROUP_KEY)) || FIRST, { remember: false });
}

/** Called after a hub dashboard has hidden the rows it does not allow: a group with no row left leaves the rail. */
export function refreshSettingsGroups() {
    for (const p of panes()) {
        const empty = !p.querySelector('.settings-row:not([hidden]), .tb-row:not([hidden])');
        if (empty) p.dataset.empty = '1'; else delete p.dataset.empty;
        const tab = tabFor(p.dataset.group);
        if (tab) tab.hidden = empty;
    }
    const pick = document.getElementById('settings-group-select');
    if (pick) for (const o of pick.options) o.hidden = !!panes().find(p => p.dataset.group === o.value)?.dataset.empty;
    const cur = panes().find(p => !p.hidden);
    if (!cur || cur.dataset.empty) showSettingsGroup(FIRST, { remember: false });
}

/** An (i) button after each label whose explanation is long: the text sits under the row and opens in place. */
function buildInfoButtons(root) {
    for (const row of root.querySelectorAll('.settings-row')) {
        const dt = row.querySelector(':scope > dt, :scope > .net-group-head');
        const texts = [...row.querySelectorAll('dt .toggle-desc, [data-info]')];
        if (!dt || !texts.length || dt.querySelector('.settings-info-btn')) continue;
        const label = dt.querySelector('label, .net-group-title') || dt;
        const name = (label.textContent || '').trim();
        const box = document.createElement('div');
        box.className = 'settings-info-text';
        box.id = `settings-info-${++infoSeq}`;
        box.hidden = true;
        for (const t of texts) { t.classList.remove('toggle-desc'); t.removeAttribute('data-info'); box.append(t); }
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'settings-info-btn';
        btn.setAttribute('aria-expanded', 'false');
        btn.setAttribute('aria-controls', box.id);
        btn.setAttribute('aria-label', `About ${name}`);
        btn.innerHTML = '<span class="material-icons" aria-hidden="true">info</span>';
        btn.addEventListener('click', () => { const open = box.hidden; box.hidden = !open; btn.setAttribute('aria-expanded', String(open)); });
        dt.append(btn);
        row.append(box);
    }
}

/** The Hub rows nested in the Coral Hub row (Hub access, the switches under it): the same (i) after the label, the explanation closed under it (#5985). */
function buildCloudInfoButtons(root) {
    const rows = [
        ...[...root.querySelectorAll('.settings-cloud-switch-row')].map(r => ({ row: r, label: r.querySelector('.settings-cloud-switch-label'), texts: [r.querySelector('.settings-cloud-switch-text > .settings-hint')] })),
        ...[...root.querySelectorAll('.settings-cloud-mode')].map(r => ({ row: r, label: r.querySelector(':scope > label'), texts: [r.querySelector('[data-cloud="mode-full"]')] })),
    ];
    for (const { row, label, texts } of rows) {
        const list = texts.filter(Boolean);
        if (!label || !list.length || label.parentElement.classList.contains('settings-cloud-head')) continue; // (the mode row holds the switch rows: look at its own label only)
        const box = document.createElement('div');
        box.className = 'settings-info-text';
        box.id = `settings-info-${++infoSeq}`;
        box.hidden = true;
        for (const t of list) box.append(t); // the switch stays described by it (aria-describedby points at its id); a closed box still counts
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'settings-info-btn';
        btn.setAttribute('aria-expanded', 'false');
        btn.setAttribute('aria-controls', box.id);
        btn.setAttribute('aria-label', `About ${(label.textContent || '').trim()}`);
        btn.innerHTML = '<span class="material-icons" aria-hidden="true">info</span>';
        btn.addEventListener('click', () => { const open = box.hidden; box.hidden = !open; btn.setAttribute('aria-expanded', String(open)); });
        const head = document.createElement('div'); // the label and its (i) on one line
        head.className = 'settings-cloud-head';
        const host = label.parentElement;
        label.replaceWith(head); head.append(label, btn);
        host.append(box);
    }
}

function buildTabs(modal) {
    const tabs = modal.querySelector('#settings-tabs');
    const pick = modal.querySelector('#settings-group-select');
    if (!tabs || tabs.childElementCount) return;
    for (const p of panes()) {
        const id = p.dataset.group;
        const b = document.createElement('button');
        b.type = 'button'; b.id = `settings-tab-${id}`; b.className = 'settings-tab'; b.setAttribute('role', 'tab');
        b.setAttribute('aria-controls', p.id); b.setAttribute('aria-selected', String(!p.hidden)); b.tabIndex = p.hidden ? -1 : 0;
        b.innerHTML = `<span class="material-icons" aria-hidden="true">${p.dataset.icon}</span><span class="settings-tab-name"></span>`;
        b.querySelector('.settings-tab-name').textContent = p.dataset.title;
        b.addEventListener('click', () => showSettingsGroup(id));
        tabs.append(b);
        const o = document.createElement('option'); o.value = id; o.textContent = p.dataset.title; pick?.append(o);
    }
    tabs.addEventListener('keydown', e => {
        const list = [...tabs.querySelectorAll('[role="tab"]:not([hidden])')];
        const i = list.indexOf(document.activeElement);
        if (i < 0) return;
        const to = { ArrowDown: i + 1, ArrowRight: i + 1, ArrowUp: i - 1, ArrowLeft: i - 1, Home: 0, End: list.length - 1 }[e.key];
        if (to === undefined) return;
        e.preventDefault();
        showSettingsGroup(list[(to + list.length) % list.length].id.replace('settings-tab-', ''), { focus: true });
    });
    pick?.addEventListener('change', () => showSettingsGroup(pick.value));
}

onBuilt('settings-modal', modal => {
    if (!updatesUiOn()) modal.querySelectorAll('[data-flag="updates"]').forEach(e => e.remove()); // behind the flag: no tab, no pane (#5275)
    buildTabs(modal); buildInfoButtons(modal); buildCloudInfoButtons(modal);
});
