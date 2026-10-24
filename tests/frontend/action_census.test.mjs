// The final census of CSP stage 2 (#3645, Security's requirement 3): with 'unsafe-inline' gone from
// the dashboard's script-src, every control works through a delegated data-on-* action, so:
//   1. every action the UI's markup names is registered, every registered action is named by
//      some markup (no dead or missing control), and none is registered twice;
//   2. no markup in the shipped UI (templates, static JS) has an inline on* handler, and no
//      code reads one (an onclick-text classifier would silently match nothing now);
//   3. every action whose name says it restarts, kills, renames, deletes, resets, removes, stops
//      or cancels is either on actions.js's DESTRUCTIVE_ACTIONS (a popout removes it) or on the
//      reviewed NOT_POPOUT_RELEVANT list below with its reason; so are the launch, import, save
//      and apply-settings actions, and the ones Security named on the review;
//   4. every send-type action (cmd-*, send-menu-*) is on SEND_ACTIONS (a popout gates it while
//      the agent can't take input), and every name on the three lists is a registered action.
//   node action_census.test.mjs
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { installDom, importUi, FRONTEND } from './helpers/jsdom_env.mjs';

installDom({ mode: 'cloud' });
const { DESTRUCTIVE_ACTIONS, SEND_ACTIONS } = await importUi('actions');

const STATIC = path.join(FRONTEND, 'static'), TEMPLATES = path.join(FRONTEND, 'templates');
const walk = (d, ext) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => e.isDirectory()
    ? (['vendor', '_src', 'agent_docs', 'node_modules'].includes(e.name) ? [] : walk(path.join(d, e.name), ext))
    : e.name.endsWith(ext) ? [path.join(d, e.name)] : []);
const JS = walk(STATIC, '.js'), HTML = [...walk(TEMPLATES, '.html'), ...walk(STATIC, '.html')];
const rel = f => path.relative(FRONTEND, f).split(path.sep).join('/');
const src = new Map([...JS, ...HTML].map(f => [f, fs.readFileSync(f, 'utf8')]));

// ── 1. named vs registered ─────────────────────────────────────────────
const named = new Map(), registered = new Map();
const note = (m, k, f) => { if (!m.has(k)) m.set(k, []); m.get(k).push(rel(f)); };
for (const [f, s] of src) {
    for (const m of s.matchAll(/data-on-([a-z]+)=\\?["']([^"'\\]*)/g)) {
        // actions.js's actionSelector builds a selector from names; it names no action itself.
        if (rel(f) === 'static/actions.js' && m[2] === ' + JSON.stringify(n) + ') continue;
        assert.match(m[2], /^[a-z0-9-]+$/, `${rel(f)}: an action name must be a constant: ${m[0]}`);
        note(named, m[2], f);
    }
    for (const m of s.matchAll(/setAttribute\(\s*['"]data-on-[a-z]+['"]\s*,\s*['"]([a-z0-9-]+)['"]/g)) note(named, m[1], f);
}
let blocks = 0;
for (const f of JS) {
    const s = src.get(f);
    // Every registration is a top-level statement (a comment's example, indented, isn't one).
    const calls = [...s.matchAll(/^registerActions\(\{/gm)].length;
    // A block (one action per line, closed by "});" at column 0), or one action on one line.
    const found = [...s.matchAll(/^registerActions\(\{ ('[a-z0-9-]+':.*) \}\);$|^registerActions\(\{\n([\s\S]*?)\n\}\);/gm)]
        .map(m => [m[0], m[1] !== undefined ? '\n' + m[1] : m[2]]);
    // A registration this parse can't read would hide an unused or missing action.
    assert.equal(found.length, calls, `${rel(f)}: ${calls} registerActions({ calls, ${found.length} read`);
    blocks += calls;
    for (const b of found) {
        for (const k of b[1].matchAll(/^\s*'([a-z0-9-]+)':/gm)) note(registered, k[1], f);
        for (const k of b[1].matchAll(/^\s*(?:"[^"]*"|[A-Za-z_$][\w$]*):/gm)) assert.fail(`${rel(f)}: register an action with a quoted kebab-case name: ${k[0].trim()}`);
    }
}
const missing = [...named.keys()].filter(n => !registered.has(n)).map(n => `${n} (${named.get(n)})`);
const unused = [...registered.keys()].filter(n => !named.has(n)).map(n => `${n} (${registered.get(n)})`);
const twice = [...registered].filter(([, fs_]) => fs_.length > 1).map(([n, fs_]) => `${n} (${fs_})`);
assert.deepStrictEqual(missing, [], 'markup names an action nobody registers (a dead control)');
assert.deepStrictEqual(unused, [], 'a registered action no markup names (dead code, or a typo in the markup)');
assert.deepStrictEqual(twice, [], 'an action registered twice');
assert.ok(registered.size > 300 && blocks > 20, `the parse found the registrations (${registered.size} actions in ${blocks} blocks)`);

// ── 2. no inline handler, and nothing reads one ────────────────────────
const EVENTS = 'click|dblclick|contextmenu|change|input|submit|reset|keydown|keyup|keypress|focus|blur|focusin|focusout|'
    + 'mouse(?:down|up|over|out|move|enter|leave)|touch(?:start|end|move|cancel)|pointer(?:down|up|over|out|move|enter|leave|cancel)|'
    + 'load|error|abort|scroll|wheel|drag(?:start|end|over|enter|leave)?|drop|paste|copy|cut|toggle|animation(?:start|end)|transition(?:end|start)';
const INLINE = new RegExp(`(?<![\\w.-])on(?:${EVENTS})\\s*=\\s*\\\\?["'\`]`, 'gi');
const READ = new RegExp(`(?:get|has|remove)Attribute\\(\\s*['"]on(?:${EVENTS})['"]|\\[on(?:${EVENTS})\\s*(?:[*^$|~]?=|\\])`, 'gi');
const inline = [], reads = [];
for (const [f, s] of src) {
    s.split('\n').forEach((line, i) => {
        for (const m of line.matchAll(INLINE)) inline.push(`${rel(f)}:${i + 1}: ${m[0]}`);
        if (f.endsWith('.js')) for (const m of line.matchAll(READ)) reads.push(`${rel(f)}:${i + 1}: ${m[0]}`);
    });
}
assert.deepStrictEqual(inline, [], 'an inline on* handler (the dashboard CSP has no unsafe-inline: it would not run)');
assert.deepStrictEqual(reads, [], 'code reads an inline handler attribute (none exists any more)');
for (const f of HTML) {
    for (const m of src.get(f).matchAll(/<script\b([^>]*)>/g)) {
        if (/\bsrc=/.test(m[1]) || /type="application\/json"/.test(m[1])) continue;
        // panel_host.html and view_host.html are the sandboxed hosts of agent panels and custom views: loaded by URL
        // into an opaque-origin frame, each with its own CSP (they exist so that the dashboard's is not inherited).
        if (rel(f) === 'static/panel_host.html' || rel(f) === 'static/view_host.html') continue;
        assert.fail(`${rel(f)}: an inline <script> (the dashboard CSP has no unsafe-inline): <script${m[1]}>`);
    }
}

// ── 3. destructive-sounding, launch, import, save, apply: classified ───
// Reviewed: none of these needs a popout to remove it. A popout shows only one agent's workspace;
// popout.css hides the top bar, sidebar, mobile tabs and the board tab.
const NOT_POPOUT_RELEVANT = new Map(Object.entries({
    // Security's named list (review of the final step)
    'agent-sleep': 'sidebar agent menu (a popout has no sidebar); sleep is reversible (Wake)',
    'cmd-stop': 'Stop on a phone\'s chat (#6802): sends Escape to interrupt the agent, a send gated by SEND_ACTIONS in a popout; the interrupt loses nothing (the agent can be asked again)',
    'agent-archive': 'sidebar agent menu; archive is reversible (Unarchive)',
    'team-sleep': 'sidebar team menu; reversible (Wake)',
    'agent-move': 'sidebar order only',
    'group-move': 'sidebar order only',
    'goal-request': 'sidebar row: asks the server to summarise the agent\'s goal; types nothing into the agent',
    'msg-edit-resubmit': 'copies a past message into the command input; sending is cmd-send (gated)',
    // Names with a destructive word, reviewed one by one
    'attachment-remove': 'drops a pending attachment from the unsent message',
    'board-chat-select-cancel': 'leaves the board chat\'s selection mode (UI state)',
    'custom-view-delete': 'deletes a custom view definition, after showConfirmModal',
    'dep-tag-remove': 'removes a dependency chip from the task dialog\'s unsaved draft',
    'folder-tag-remove': 'sidebar folder tag menu; a label, not an agent',
    'job-delete': 'Jobs view (dashboard nav only), after showConfirmModal',
    'm-close-top-bar-settings-then-stop-all-auto-accept': 'top bar menu (a popout has no top bar)',
    'm-hide-restart-modal': 'the restart dialog\'s Cancel: closes it, restarts nothing',
    'm-reset-default-prompt': 'Settings > default prompts (top bar only); resets an editor field',
    'macro-delete': 'removes a saved quick-action button (the operator\'s own setting); command toolbar, gated with it',
    'mb-select-cancel': 'leaves the board\'s selection mode (UI state)',
    'notes-cancel': 'history view: discards unsaved note edits',
    'session-tag-remove': 'history view: removes a label from a session',
    'task-cancel': 'opens the inline "Cancel this task?" confirmation; cancels nothing itself',
    'task-cancel-back': 'the confirmation\'s No: restores the footer',
    'task-cancel-do': 'the confirmation\'s Yes: cancels a board task (a task, not the agent)',
    'task-edit-cancel': 'discards the task dialog\'s unsaved edits',
    'task-remind-stop': 'stops a task\'s reminder; types nothing into the agent',
    'team-agent-remove': 'removes a row from the team builder\'s unsaved draft',
    'theme-delete': 'theme editor (top bar only), after showConfirmModal',
    'theme-reset': 'theme editor: reverts unsaved colour edits',
    'wf-delete': 'Workflows view (dashboard nav only), after confirm',
    'wf-step-remove': 'removes a step from the workflow editor\'s unsaved draft',
    // QA's list: launch, import, save, apply settings (dialogs opened from the top bar, sidebar or views)
    'm-launch-agent-to-board': 'launch dialog: starts a new agent (opened from the sidebar or top bar)',
    'm-launch-session': 'launch dialog: starts a new agent',
    'm-launch-team': 'launch dialog: starts a new team',
    'm-import-personas': 'launch dialog: imports persona files into the form',
    'm-import-team-from-folder': 'launch dialog: reads a team definition into the form',
    'm-import-team-templates': 'launch dialog: imports team templates (the operator\'s settings)',
    'm-handle-team-folder-import': 'launch dialog: the folder picker of the import above',
    'm-apply-settings': 'Settings dialog (top bar only): saves settings',
    'm-confirm-save-template': 'saves a team template (settings)',
    'm-hide-save-template-modal': 'the save-template dialog\'s Cancel: closes it, saves nothing',
    'm-save-current-persona': 'saves a persona (settings)',
    'm-save-default-prompts': 'Settings > default prompts (top bar only)',
    'm-save-scheduled-job': 'job dialog: saves a scheduled job (Jobs view)',
    'm-save-team-agent-from-modal': 'team builder: saves a row of the unsaved draft',
    'm-save-team-template': 'saves a team template (settings)',
    'm-save-view': 'saves a custom view definition',
    'notes-save': 'history view: saves session notes',
    'task-edit-save': 'saves a board task',
    'team-save': 'team settings: saves the team\'s settings',
    'theme-import': 'theme editor (top bar only)',
    'theme-save': 'theme editor (top bar only)',
    'wf-save': 'Workflows view (dashboard nav only)',
}));
const RISKY = /(^|-)(restart|kill|rename|delete|reset|remove|stop|cancel)(-|$)/;
const MUST_REVIEW = /^m-launch-|(^|-)import(-|$)|(^|-)save(-|$)|^m-apply-settings$/;
const SECURITY_NAMED = ['agent-sleep', 'agent-archive', 'team-sleep', 'agent-move', 'group-move', 'goal-request', 'msg-edit-resubmit'];
const unclassified = [...registered.keys()].filter(n => (RISKY.test(n) || MUST_REVIEW.test(n)) && !DESTRUCTIVE_ACTIONS.has(n) && !NOT_POPOUT_RELEVANT.has(n));
assert.deepStrictEqual(unclassified, [], 'classify each: DESTRUCTIVE_ACTIONS in actions.js (a popout removes it) or NOT_POPOUT_RELEVANT here, with the reason');
for (const n of SECURITY_NAMED) assert.ok(DESTRUCTIVE_ACTIONS.has(n) || NOT_POPOUT_RELEVANT.has(n), `${n} is reviewed`);
for (const [n, why] of NOT_POPOUT_RELEVANT) {
    assert.ok(!DESTRUCTIVE_ACTIONS.has(n), `${n} is on both lists`);
    assert.ok(why.length > 10, `${n} needs its reason`);
}

// ── 4. send actions; every listed name is real ─────────────────────────
const unsent = [...registered.keys()].filter(n => /^(cmd|send-menu)-/.test(n) && !SEND_ACTIONS.has(n));
assert.deepStrictEqual(unsent, [], 'a send-type action missing from SEND_ACTIONS (a popout would not gate it)');
for (const [list, names] of [['DESTRUCTIVE_ACTIONS', DESTRUCTIVE_ACTIONS], ['SEND_ACTIONS', SEND_ACTIONS], ['NOT_POPOUT_RELEVANT', NOT_POPOUT_RELEVANT.keys()]]) {
    const stale = [...names].filter(n => !registered.has(n));
    assert.deepStrictEqual(stale, [], `${list} names an action nobody registers`);
}

console.log(`action_census: ok (${registered.size} actions in ${blocks} registrations, all named and registered; ` +
    `${DESTRUCTIVE_ACTIONS.size} destructive, ${SEND_ACTIONS.size} send, ${NOT_POPOUT_RELEVANT.size} reviewed not popout-relevant; no inline handler, no handler read)`);
