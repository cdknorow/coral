/* The sectioned dialog shared by Restart, New Agent, Agent Team and the team's agent dialog (#4950): the Settings dialog's shell (css/components.css, .settings-shell) with a section list with icons on the
 * left, one pane per section on the right (a heading and a one-line description over its fields), and the dialog's own fixed footer. On a narrow dialog (a phone, large text) the list is a selector at
 * the top, as in Settings. A pane is any element with class settings-pane inside the shell:
 *     <section class="settings-pane" data-section="agent" data-title="Agent" data-icon="smart_toy" data-desc="Which CLI and model it runs.">fields</section>
 * Every pane stays in the page while another is shown (hidden), so the values of the fields are kept when the person moves between sections, and a script reaches any field.
 *
 *   initSectionDialog(shell)        build the list from the panes now in the shell (call again after the panes are replaced); returns the shell's controller
 *   showSection(shell, id, opts)    open a section ({ focus: true } moves the keyboard to its entry in the list)
 *   revealField(el)                 open the section holding el, if it sits in a sectioned dialog (a field about to be focused)
 *   markSectionError(shell, id, on) mark a section in the list as holding a problem, or clear it
 *   refreshSectionErrors(shell)     mark every section that holds a field flagged aria-invalid
 *
 * Keys in the list: arrows (and Home, End) move and open; the panes are tab panels, so Tab goes from the list into the open pane. */
let seq = 0;

const panesOf = shell => [...shell.querySelectorAll('.settings-pane[data-section]')];
const tabsOf = shell => [...shell.querySelectorAll('.settings-tabs [role="tab"]')];

function name(pane) { return pane.dataset.title || pane.dataset.section; }

/** Builds (or rebuilds) the list, the selector and the pane headings. The open section is kept when it is still there. */
export function initSectionDialog(shell) {
    const nav = shell.querySelector('.settings-nav');
    const tabs = nav?.querySelector('.settings-tabs');
    const pick = nav?.querySelector('.settings-group-pick select');
    if (!nav || !tabs) return null;
    const uid = shell._sdUid || (shell._sdUid = `sd${++seq}`);
    const panes = panesOf(shell);
    const open = panes.find(p => !p.hidden)?.dataset.section;
    tabs.replaceChildren(); pick?.replaceChildren();
    for (const p of panes) {
        const id = p.dataset.section;
        p.id = `${uid}-pane-${id}`; p.setAttribute('role', 'tabpanel'); p.setAttribute('aria-labelledby', `${uid}-tab-${id}`); p.tabIndex = -1;
        if (!p.querySelector(':scope > .settings-pane-title')) {
            const h = document.createElement('h4'); h.className = 'settings-pane-title'; h.textContent = name(p);
            const sub = document.createElement('p'); sub.className = 'settings-pane-sub'; sub.textContent = p.dataset.desc || '';
            p.prepend(h, sub);
        }
        const b = document.createElement('button');
        b.type = 'button'; b.id = `${uid}-tab-${id}`; b.className = 'settings-tab'; b.setAttribute('role', 'tab'); b.setAttribute('aria-controls', p.id);
        b.innerHTML = '<span class="material-icons" aria-hidden="true"></span><span class="settings-tab-name"></span><span class="sd-dot" aria-hidden="true" hidden></span>';
        b.querySelector('.material-icons').textContent = p.dataset.icon || 'tune';
        b.querySelector('.settings-tab-name').textContent = name(p);
        b.addEventListener('click', () => showSection(shell, id));
        tabs.append(b);
        if (pick) { const o = document.createElement('option'); o.value = id; o.textContent = name(p); pick.append(o); }
    }
    if (!shell._sdWired) {
        shell._sdWired = true;
        tabs.addEventListener('keydown', e => {
            const list = tabsOf(shell).filter(t => !t.hidden);
            const i = list.indexOf(document.activeElement);
            if (i < 0) return;
            const to = { ArrowDown: i + 1, ArrowRight: i + 1, ArrowUp: i - 1, ArrowLeft: i - 1, Home: 0, End: list.length - 1 }[e.key];
            if (to === undefined) return;
            e.preventDefault();
            showSection(shell, list[(to + list.length) % list.length].dataset.section, { focus: true });
        });
        pick?.addEventListener('change', () => showSection(shell, pick.value));
        watchSectionErrors(shell);
    }
    for (const t of tabsOf(shell)) t.dataset.section = t.id.slice(`${uid}-tab-`.length);
    showSection(shell, open || panes[0]?.dataset.section, { remember: false });
    refreshSectionErrors(shell);
    return shell;
}

export function showSection(shell, id, { focus = false } = {}) {
    const panes = panesOf(shell);
    const pane = panes.find(p => p.dataset.section === id) || panes[0];
    if (!pane) return null;
    for (const p of panes) {
        const on = p === pane;
        p.hidden = !on;
        const tab = document.getElementById(`${shell._sdUid}-tab-${p.dataset.section}`);
        if (tab) { tab.setAttribute('aria-selected', String(on)); tab.tabIndex = on ? 0 : -1; }
    }
    const pick = shell.querySelector('.settings-group-pick select');
    if (pick) pick.value = pane.dataset.section;
    (pane.closest('.settings-panes') || pane.parentElement).scrollTop = 0;
    if (focus) document.getElementById(`${shell._sdUid}-tab-${pane.dataset.section}`)?.focus();
    return pane.dataset.section;
}

/** Opens the section that holds `el` (no-op outside a sectioned dialog, or when it is already open). */
export function revealField(el) {
    const pane = el?.closest?.('.settings-pane[data-section]');
    const shell = pane?.closest('.settings-shell');
    if (pane && shell && pane.hidden) showSection(shell, pane.dataset.section);
    const options = el?.closest?.('#launch-more-options:not([open])'); if (options) options.open = true; // a field in the closed "More options" of a phone's New Agent (#7066)
    el?.closest?.('[data-sd-more][hidden]')?.dispatchEvent(new CustomEvent('sd-more-open', { bubbles: true })); // the sections sit behind a "More options" button (New Agent, #5595): it opens
}

export function markSectionError(shell, id, on) {
    const tab = document.getElementById(`${shell._sdUid}-tab-${id}`);
    if (!tab) return;
    const pane = panesOf(shell).find(p => p.dataset.section === id);
    tab.dataset.error = on ? '1' : '';
    if (!on) delete tab.dataset.error;
    const dot = tab.querySelector('.sd-dot'); if (dot) dot.hidden = !on;
    tab.setAttribute('aria-label', on ? `${name(pane)}, has a problem` : name(pane));
    const o = [...(shell.querySelectorAll('.settings-group-pick option'))].find(x => x.value === id);
    if (o) o.textContent = on ? `${name(pane)} (has a problem)` : name(pane);
}

export function refreshSectionErrors(shell) {
    if (!shell._sdUid) return;
    for (const p of panesOf(shell)) markSectionError(shell, p.dataset.section, !!p.querySelector('[aria-invalid="true"]'));
}

// A field flagged (aria-invalid) or cleared anywhere in a sectioned dialog marks its section in the list.
const watch = new MutationObserver(records => {
    const shells = new Set();
    for (const r of records) { const s = r.target.closest?.('.settings-shell'); if (s && s._sdUid) shells.add(s); }
    for (const s of shells) refreshSectionErrors(s);
});
export function watchSectionErrors(shell) { watch.observe(shell, { subtree: true, attributes: true, attributeFilter: ['aria-invalid'] }); }
