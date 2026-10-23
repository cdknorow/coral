/* The Launch Agent Team dialog's three steps: Team, Members, Start (#6397). Pure DOM: the step the tabs show, and the buttons that go with it. */
export const TEAM_STEPS = ['team', 'members', 'start'];

export function currentTeamStep() {
    const tab = document.querySelector('#launch-step-team .team-step-tab[aria-selected="true"]');
    return tab?.dataset.step || 'team';
}

export function showTeamStep(step, { focus = false } = {}) {
    const i = TEAM_STEPS.indexOf(step);
    if (i < 0) return;
    for (const name of TEAM_STEPS) {
        const on = name === step;
        const tab = document.getElementById(`team-tab-${name}`);
        if (tab) { tab.setAttribute('aria-selected', on ? 'true' : 'false'); tab.tabIndex = on ? 0 : -1; }
        const panel = document.getElementById(`team-panel-${name}`);
        if (panel) panel.hidden = !on;
    }
    const back = document.getElementById('launch-team-back');
    const next = document.getElementById('launch-team-next');
    const go = document.getElementById('launch-team-go');
    if (back) back.hidden = i === 0;
    if (next) next.hidden = i === TEAM_STEPS.length - 1;
    if (go) go.hidden = i !== TEAM_STEPS.length - 1;
    const live = document.getElementById('team-step-live');
    if (live) live.textContent = `Step ${i + 1} of ${TEAM_STEPS.length}`;
    if (focus) document.getElementById(`team-tab-${step}`)?.focus();
}

/** Arrow keys move between the tabs, as a tablist does. */
export function teamTabKey(event) {
    const tabs = TEAM_STEPS.map(n => document.getElementById(`team-tab-${n}`));
    const i = tabs.indexOf(document.activeElement);
    if (i < 0) return null;
    const to = event.key === 'ArrowRight' ? i + 1 : event.key === 'ArrowLeft' ? i - 1 : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1;
    return to < 0 ? null : TEAM_STEPS[(to + tabs.length) % tabs.length];
}
