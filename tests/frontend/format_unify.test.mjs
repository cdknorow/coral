// formatDuration and formatCost (format.js, #4638): the views each wrote their own duration and cost text, and now share these with explicit options. Every old body is kept here verbatim
// as the reference, and each caller's option set must print exactly what its old function printed, over every kind of input (none, text, negative, fractions, edges of each unit).
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { formatDuration, formatCost } = await importUi('format');
const { formatDurationSec } = await importUi('utils');
const eventRow = await importUi('event_row');
const taskFormat = await importUi('task_format');
const sessionState = await importUi('session_state');

// ── the old bodies, verbatim ──
const oldEvent = ms => {
    const n = Number(ms);
    if (!Number.isFinite(n) || n < 0) return '';
    if (n < 1000) return `${Math.round(n)}ms`;
    if (n < 60000) return `${(n / 1000).toFixed(1)}s`;
    const totalSec = Math.round(n / 1000);
    if (totalSec < 3600) return `${Math.floor(totalSec / 60)}m ${totalSec % 60}s`;
    return `${Math.floor(totalSec / 3600)}h ${Math.floor((totalSec % 3600) / 60)}m`;
};
const oldScheduler = ms => {
    const s = Math.floor(ms / 1000);
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m ${s % 60}s`;
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
};
const oldWorkflows = sec => {
    if (sec < 60) return `${sec}s`;
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
};
const oldSec = sec => {
    const n = Number(sec);
    if (!Number.isFinite(n) || n <= 0) return '';
    if (n < 60) return `${Math.floor(n)}s`;
    const mins = Math.round(n / 60);
    if (mins < 60) return `${mins}m`;
    const h = Math.floor(mins / 60);
    const m = mins % 60;
    return m > 0 ? `${h}h ${m}m` : `${h}h`;
};
const oldCostDashboardDuration = diffMs => { // the part of cost_dashboard's _formatDuration after the date arithmetic
    const secs = Math.floor(diffMs / 1000);
    if (secs < 60) return secs + 's';
    const mins = Math.floor(secs / 60);
    const remSecs = secs % 60;
    if (mins < 60) return mins + 'm ' + remSecs + 's';
    const hrs = Math.floor(mins / 60);
    const remMins = mins % 60;
    return hrs + 'h ' + remMins + 'm';
};
const oldSubagents = ms => { // subagents' _formatDuration after its date checks: whole seconds rounded from the difference
    const secs = Math.round(ms / 1000);
    if (secs < 60) return `${secs}s`;
    const mins = Math.floor(secs / 60);
    if (mins < 60) return `${mins}m ${secs % 60}s`;
    return `${Math.floor(mins / 60)}h ${mins % 60}m`;
};
const oldCostDashboard = c => { if (c === 0) return '$0.00'; if (c < 0.01) return '$' + c.toFixed(4); return '$' + c.toFixed(2); };
const oldTask = (usd, precise) => {
    if (usd == null) return '$0.00';
    if (usd === 0) return '$0.00';
    if (precise) { const s = usd.toFixed(4); return '$' + s.replace(/0{1,2}$/, ''); }
    if (usd > 0 && usd < 0.01) return '<$0.01';
    return '$' + usd.toFixed(2);
};
const oldSession = c => { if (c < 0.01) return '$' + c.toFixed(4); return '$' + c.toFixed(2); };

// ── inputs: every edge of every unit ──
const num = [];
for (const base of [0, 1, 2, 9, 59, 60, 61, 89, 90, 119, 120, 3599, 3600, 3601, 3659, 3660, 3569, 3570, 7199, 7200, 86399, 86400, 250000]) for (const d of [-1, -0.5, 0, 0.4, 0.5, 0.6, 1]) num.push(base + d, base * 1000 + d * 1000, base * 1000 + d);
for (let i = 0; i < 4000; i++) num.push(i * 37, i * 1013 + 0.5, i * 7919);
const weird = [null, undefined, NaN, Infinity, -Infinity, 'abc', '', '90', '1.5', [], {}, true, -5, -0.001, 5e9];
const durations = [...new Set([...num, ...weird])];
const same = (name, got, want, x) => assert.strictEqual(got, want, `${name}(${JSON.stringify(x)}): ${got} != ${want}`);
const tryCall = (f, ...a) => { try { return f(...a); } catch (e) { return 'THROWS ' + e.constructor.name; } };

for (const x of durations) {
    same('event', tryCall(formatDuration, x, { shape: 'precise' }), tryCall(oldEvent, x), x);
    same('event export', tryCall(eventRow.formatDuration, x), tryCall(oldEvent, x), x);
    same('scheduler', tryCall(formatDuration, x), tryCall(oldScheduler, x), x);
    same('workflows', tryCall(formatDuration, x, { unit: 's', dropZeroSeconds: true }), tryCall(oldWorkflows, x), x);
    same('seconds', tryCall(formatDuration, x, { shape: 'rounded' }), tryCall(oldSec, x), x);
    same('seconds export', tryCall(formatDurationSec, x), tryCall(oldSec, x), x);
    same('cost dashboard', tryCall(formatDuration, x), tryCall(oldCostDashboardDuration, x), x);
    same('subagents', tryCall(formatDuration, Math.round(x / 1000), { unit: 's' }), tryCall(oldSubagents, x), x);
}

const costs = [];
for (const base of [0, 0.00001, 0.001, 0.0049, 0.005, 0.0099, 0.01, 0.0101, 0.5, 1, 1.5, 1.505, 1.9999, 9.995, 10, 99.999, 1234.5678, 1e6]) costs.push(base, -base);
for (let i = 0; i < 3000; i++) costs.push(i / 997, i * 1.37);
const costInputs = [...costs, null, undefined, NaN, Infinity, '0.5', '', {}, true];
for (const x of costInputs) {
    same('cost dashboard cost', tryCall(formatCost, x, { zeroAsTwo: true }), tryCall(oldCostDashboard, x), x);
    for (const precise of [false, true, undefined]) {
        same('task cost', tryCall(formatCost, x, { nullAsZero: true, zeroAsTwo: true, subCent: 'lt', precise }), tryCall(oldTask, x, precise), x);
        same('task cost export', tryCall(taskFormat._formatCost, x, precise), tryCall(oldTask, x, precise), x);
    }
    same('session cost', tryCall(formatCost, x), tryCall(oldSession, x), x);
    same('session cost export', tryCall(sessionState._formatCost, x), tryCall(oldSession, x), x);
}
console.log(`format_unify.test.mjs: ok (${durations.length} durations x 8 callers, ${costInputs.length} costs x 5 callers, byte-identical)`);
process.exit(0); // the pages the modules were loaded into keep timers running
