/* The Health check tab of Team settings (#4230, the operator; Backend's #4175 step 2 API): a team's health check on or off and its interval,
 * who receives it, the all-clear line, the default reviewers, and each automatic rule on or off with its numbers. The values are the "health"
 * object of GET and PUT /api/board/{project}/settings (PUT replaces the whole object, Operator only, and the server clamps every value and
 * refuses a recipient who is not a member of the team). Through Coral Hub the server leaves "health" out of the GET, and then there is no tab.
 *
 * The inputs' limits are the server's clamps (HEALTH_LIMITS mirrors internal/board/health_settings.go); the form saves on its own, not with the
 * main Save bar, and a field the form does not know (a rule the server added) is sent back as it was read. Since PUT replaces the whole
 * object, Save reads it again first and changes only what the person changed (#4423): a rule another client changed while the tab was
 * open keeps that change. */
import { importOnce } from './import_once.js';
import { switchRow } from './switch_row.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { boardFetch } from './server_base.js';
import { rebaseChanges } from './settings_diff.js';
const { fieldRow } = await importOnce('./field_row.js');
const { infoButton, infoBox, bindInfo } = await importOnce('./info_toggle.js'); // lazy modules load each other through importOnce, so a failed load is retried
const { refusalOf, answeredLocally } = await importOnce('./owner_refusal.js'); // a lazy module loads the others it needs through importOnce, so a failed load is retried

const HEALTH_LIMITS = { minutes: [5, 1440], nudges: [1, 5], reviewers: 8 };

// The server's defaults (DefaultHealthSettings); held_notice is the Lead's #4225 field, filled in only when the server returns it.
const HEALTH_DEFAULTS = {
    on: false, interval_min: 10, recipient: '', all_clear: true, default_reviewers: [], // the check is off until a team turns it on (#4962)
    auto_delegate: { on: true },
    claim_nudge: { on: true, after_min: 10, nudges: 2 },
    status_nudge: { on: true, after_min: 20, escalate_after_min: 10 },
    mention_renudge: { on: true, after_min: 10 },
    held_notice: { on: true, after_min: 10 },
};

const [MIN_LO, MIN_HI] = HEALTH_LIMITS.minutes;
const [N_LO, N_HI] = HEALTH_LIMITS.nudges;

function knob(name, label, value, lo, hi, unit, unitName) {
    // The name is the label and the unit ("After minutes idle"); the range is in the box (min, max, its tooltip) and is the description for a screen reader, not a line of its own.
    return fieldRow({ id: `hk-${name}`, label, name, type: 'number', value, min: lo, max: hi, unit, unitName, desc: `${Number(lo)} to ${Number(hi)}`, hideDesc: true, attrs: { title: `${Number(lo)} to ${Number(hi)}` } });
}

function rule(key, title, why, on, knobs, extra = '') {
    // The title names the switch and the group of numbers under it, so "After minutes" is heard inside its rule; the explanation is behind the (i) and describes the switch.
    const info = `hr-${key}-w`;
    return `<div class="health-rule" data-rule="${escapeAttr(key)}" role="group" aria-labelledby="hr-${escapeAttr(key)}-t"><div class="health-line">${switchRow({ label: title, name: `${key}_on`, checked: on, labelClass: 'health-rule-head', labelId: `hr-${key}-t`, describedBy: [info] })}${infoButton(info, title)}</div>${infoBox(info, why)}<div class="health-knobs">${knobs}${extra}</div></div>`;
}

/** The panel's HTML for a health object and the team's members (active subscribers: { subscriber_id, job_title }). */
function healthPanelHTML(h, members) {
    const names = members.map(m => m.subscriber_id);
    const rcpt = h.recipient || '';
    const options = [`<option value="">The orchestrator (default)</option>`]
        .concat(members.map(m => `<option value="${escapeAttr(m.subscriber_id)}"${m.subscriber_id === rcpt ? ' selected' : ''}>${escapeHtml(m.subscriber_id)}${m.job_title ? ` (${escapeHtml(m.job_title)})` : ''}</option>`));
    if (rcpt && !names.includes(rcpt)) options.push(`<option value="${escapeAttr(rcpt)}" selected>${escapeHtml(rcpt)} (not in the team now)</option>`);
    const reviewers = fieldRow({ id: 'hk-default_reviewers', label: 'Default reviewers', name: 'default_reviewers', value: (h.default_reviewers || []).join(', '), wide: true, descFirst: true,
        desc: `Names of agents on this team, up to ${HEALTH_LIMITS.reviewers}, separated by commas.`, hideDesc: true,
        attrs: { list: 'health-members', autocomplete: 'off', maxlength: 400, placeholder: `Agent names (up to ${HEALTH_LIMITS.reviewers})` },
        after: `<datalist id="health-members">${names.map(n => `<option value="${escapeAttr(n)}">`).join('')}</datalist>` });
    const held = h.held_notice ? rule('held_notice', 'Tell the Operator about a held delivery',
        'A message Coral could not type into an agent (its terminal is on a dialog) and has held back for a while is reported to the Operator.',
        !!h.held_notice.on, knob('held_notice_after_min', 'After', h.held_notice.after_min, MIN_LO, MIN_HI, 'minutes held')) : '';
    return `<form class="health-form working-mode-form" autocomplete="off" novalidate>
      <fieldset class="team-settings-section" aria-label="Health check">
        <div class="health-line">${switchRow({ label: 'Send health check', name: 'on', checked: h.on, labelClass: 'health-rule-head', labelId: 'hc-on-t', titleTag: 'b', desc: 'Uses tokens each time.', descId: 'hc-on-h', descClass: 'health-why', descInside: true, sep: ' ', describedBy: ['hc-on-w'] })}${infoButton('hc-on-w', 'the health check')}</div>
        ${infoBox('hc-on-w', 'Checks in on the team every few minutes to spot stuck or idle agents, and tells one person who is idle, stuck or waiting, and what changed.')}
        <div class="health-row">${knob('interval_min', 'Every', h.interval_min, MIN_LO, MIN_HI, 'min', 'minutes')}
          <label class="health-wide-select">Send it to<select name="recipient">${options.join('')}</select></label></div>
        <div class="health-line" data-check-dep>${switchRow({ label: 'Post when all is well', name: 'all_clear', checked: h.all_clear, labelClass: 'health-rule-head', labelId: 'hc-clear-t', describedBy: ['hc-clear-w'] })}${infoButton('hc-clear-w', 'posting when all is well')}</div>
        ${infoBox('hc-clear-w', 'A short line so a quiet team is not mistaken for a stopped check.')}
      </fieldset>
      <details class="disclosure health-advanced"><summary>Reviews and automatic rules</summary>
      <fieldset class="team-settings-section"><legend>Reviews</legend>
        ${rule('auto_delegate', 'Hand reviews to the default reviewers', 'When a task is submitted for review and nobody is assigned, Coral delegates it to the default reviewers; with none named, reviews wait for the orchestrator. Works whether or not the health check is on.', !!h.auto_delegate.on, '', reviewers)}
      </fieldset>
      <fieldset class="team-settings-section"><legend>Automatic rules</legend>
        <p class="working-mode-hint">Run only while the health check is on.</p>
        ${rule('claim_nudge', 'Nudge an idle agent to claim its work', 'An agent that is idle with ready work gets a reminder; past the count, the orchestrator is told.', !!h.claim_nudge.on,
            knob('claim_nudge_after_min', 'After', h.claim_nudge.after_min, MIN_LO, MIN_HI, 'minutes idle') + knob('claim_nudge_nudges', 'Nudges before telling the orchestrator', h.claim_nudge.nudges, N_LO, N_HI, 'nudges'))}
        ${rule('status_nudge', 'Ask a quiet agent for a status', 'An agent that has done nothing for a while is asked where it stands; if it stays quiet, the orchestrator is told.', !!h.status_nudge.on,
            knob('status_nudge_after_min', 'Ask after', h.status_nudge.after_min, MIN_LO, MIN_HI, 'minutes quiet') + knob('status_nudge_escalate_after_min', 'Tell the orchestrator', h.status_nudge.escalate_after_min, MIN_LO, MIN_HI, 'minutes later'))}
        ${held}
        ${rule('mention_renudge', 'Remind the orchestrator of an unanswered mention', 'A message that @mentions the orchestrator and has had no reply gets a reminder.', !!h.mention_renudge.on,
            knob('mention_renudge_after_min', 'After', h.mention_renudge.after_min, MIN_LO, MIN_HI, 'minutes'))}
      </fieldset>
      </details>
      <div class="team-settings-actions health-actions"><button type="submit" class="btn btn-primary" disabled>Save health check</button><button type="button" class="btn health-reset">Reset to defaults</button>
        <span class="health-state" role="status" aria-live="polite"></span></div>
    </form>`;
}

const field = (form, name) => form.elements[name];
const num = (form, name) => parseInt(field(form, name).value, 10);

/** The form's values as a health object: what it shows over what was read (so a field it does not know goes back as it was). */
function readHealth(form, base) {
    const h = JSON.parse(JSON.stringify(base));
    h.on = field(form, 'on').checked;
    // what the three show is saved as shown; a number that is not a whole number (typed, then the check turned off) keeps what was read
    const every = num(form, 'interval_min');
    h.interval_min = Number.isInteger(every) ? every : base.interval_min;
    h.recipient = field(form, 'recipient').value;
    h.all_clear = field(form, 'all_clear').checked;
    h.default_reviewers = field(form, 'default_reviewers').value.split(',').map(s => s.trim()).filter((s, i, a) => s && a.indexOf(s) === i);
    h.auto_delegate = { ...h.auto_delegate, on: field(form, 'auto_delegate_on').checked };
    h.claim_nudge = { ...h.claim_nudge, on: field(form, 'claim_nudge_on').checked, after_min: num(form, 'claim_nudge_after_min'), nudges: num(form, 'claim_nudge_nudges') };
    h.status_nudge = { ...h.status_nudge, on: field(form, 'status_nudge_on').checked, after_min: num(form, 'status_nudge_after_min'), escalate_after_min: num(form, 'status_nudge_escalate_after_min') };
    h.mention_renudge = { ...h.mention_renudge, on: field(form, 'mention_renudge_on').checked, after_min: num(form, 'mention_renudge_after_min') };
    if (h.held_notice && field(form, 'held_notice_on')) h.held_notice = { ...h.held_notice, on: field(form, 'held_notice_on').checked, after_min: num(form, 'held_notice_after_min') };
    return h;
}

// The same serialisation as team_working_mode.js's settingsFormSnapshot, so its unsaved-changes guard can compare against the initial state kept here.
const snapshot = form => JSON.stringify(Array.from(form.elements).filter(el => el.name).map(el => ({ name: el.name, type: el.type, value: el.type === 'checkbox' ? el.checked : el.value })));

/** The first problem with the numbers or reviewers, in plain words, and the field to focus; null when the form is fine. */
function healthProblem(form) {
    for (const el of form.querySelectorAll('input[type=number]:not(:disabled)')) {
        const v = el.value.trim();
        const n = Number(v);
        if (v === '' || !Number.isInteger(n) || n < Number(el.min) || n > Number(el.max)) return { el, text: `${el.closest('label')?.firstChild?.textContent?.trim() || 'A number'}: use a whole number from ${el.min} to ${el.max}.` };
    }
    const revs = field(form, 'default_reviewers').value.split(',').map(s => s.trim()).filter(Boolean);
    if (new Set(revs).size > HEALTH_LIMITS.reviewers) return { el: field(form, 'default_reviewers'), text: `Default reviewers: at most ${HEALTH_LIMITS.reviewers} names.` };
    return null;
}

/** Draw the tab's panel into host for the team (a team key) from the health object the settings GET returned. Returns { dirty() }. */
export async function mountHealthSettings(host, team, health) {
    let members = [];
    try {
        const resp = await boardFetch(team, '/subscribers');
        if (resp.ok) members = (await resp.json()).filter(m => m.is_active !== 0);
    } catch { /* the recipient list is then just the default and the saved name */ }
    return bind(host, team, JSON.parse(JSON.stringify(health)), members, '');
}

/** Draw the form for base and wire it; said is a line to show at once (after a save). */
function bind(host, team, base, members, said) {
    host.innerHTML = healthPanelHTML(base, members);
    bindInfo(host);
    const form = host.querySelector('form');
    const save = form.querySelector('[type=submit]');
    const state = form.querySelector('.health-state');
    const idle = state.textContent;
    const say = (text, error = false) => { state.textContent = text; state.classList.toggle('health-error', error); };
    // While the check is off, what only matters with it on (how often, to whom, the all-clear line) is dimmed and off: the screen says "turn this on first".
    const syncCheck = () => {
        const on = field(form, 'on').checked;
        for (const name of ['interval_min', 'recipient', 'all_clear']) field(form, name).disabled = !on;
        for (const el of form.querySelectorAll('.health-row, [data-check-dep]')) el.classList.toggle('off', !on);
    };
    const syncRules = () => {
        syncCheck();
        for (const card of form.querySelectorAll('.health-rule')) {
            const on = card.querySelector('.health-rule-head input').checked;
            card.classList.toggle('off', !on);
            for (const el of card.querySelectorAll('.health-knobs input')) el.disabled = !on;
        }
    };
    const changed = () => { save.disabled = snapshot(form) === form.dataset.initialState; if (!state.classList.contains('health-error') && !save.disabled) say(idle); };
    syncRules();
    form.dataset.initialState = snapshot(form);
    save.disabled = true;
    if (said) say(said);
    form.addEventListener('input', () => { syncRules(); changed(); });
    form.addEventListener('change', () => { syncRules(); changed(); });
    form.querySelector('.health-reset').addEventListener('click', () => {
        const d = HEALTH_DEFAULTS;
        const set = (name, v) => { const el = field(form, name); if (!el) return; if (el.type === 'checkbox') el.checked = !!v; else el.value = String(v); };
        set('on', d.on); set('interval_min', d.interval_min); set('recipient', d.recipient); set('all_clear', d.all_clear); set('default_reviewers', d.default_reviewers.join(', '));
        set('auto_delegate_on', d.auto_delegate.on);
        for (const k of ['claim_nudge', 'status_nudge', 'mention_renudge', 'held_notice']) for (const [p, v] of Object.entries(d[k])) set(`${k}_${p}`, v);
        syncRules(); changed();
        say('The defaults are filled in. Save to apply them.');
        field(form, 'on').focus();
    });
    form.addEventListener('submit', async event => {
        event.preventDefault();
        const problem = healthProblem(form);
        if (problem) { say(problem.text, true); const folded = problem.el.closest('details'); if (folded) folded.open = true; problem.el.focus(); return; } // a field behind "Reviews and automatic rules" opens it first
        save.disabled = true; say('Saving…');
        try {
            const now = await boardFetch(team, '/settings');
            const fresh = now.ok ? (await now.json()).health : null;
            if (!fresh || typeof fresh !== 'object') { say(`Could not read the current health check to save it. ${now.ok ? 'Try again.' : await refusalOf(now, answeredLocally(team))}`, true); save.disabled = false; return; }
            const health = rebaseChanges(base, readHealth(form, base), fresh);
            const resp = await boardFetch(team, '/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Operator', health }) });
            if (!resp.ok) { say(`Could not save the health check. ${await refusalOf(resp, answeredLocally(team))}`, true); save.disabled = false; return; } // the refusal in our words, never the server's (#5323)
            const saved = (await resp.json()).health;
            // Drawn again from what the server kept: it clamps every value.
            bind(host, team, saved ? JSON.parse(JSON.stringify(saved)) : readHealth(form, base), members, 'Saved.');
            host.dispatchEvent(new CustomEvent('health-saved', { bubbles: true }));
        } catch (error) { say(error.message || 'Could not save the health check.', true); save.disabled = false; }
    });
    return { dirty: () => snapshot(host.querySelector('form')) !== host.querySelector('form').dataset.initialState };
}
