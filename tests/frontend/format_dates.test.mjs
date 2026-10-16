// The cached date formatters (#3866): they give exactly what Date.toLocale*String gave (same options), answer "Invalid Date" for a bad
// date like it did, and build one Intl.DateTimeFormat per format however many times they are called.
//   node format_dates.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const f = await importUi('format');
const tf = await importUi('task_format');
const dates = [new Date(2026, 9, 16, 10, 31, 45), new Date(2026, 0, 1, 0, 5), new Date(2025, 11, 31, 23, 59), new Date('2026-03-08T12:00:00Z')];
for (const d of dates) {
    assert.equal(f.formatHourMinute(d), d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }));
    assert.equal(f.formatMonthDayHourMinute(d), d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }));
    assert.equal(f.formatMonthDayTime(d), d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }));
    assert.equal(tf._formatTaskTime(d.toISOString()), f.formatMonthDayHourMinute(d));
    assert.equal(tf.formatTaskDate(d.toISOString()), f.formatMonthDayTime(d));
}
const bad = new Date('not a date');
assert.equal(f.formatHourMinute(bad), bad.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }), 'a bad date: the same text as before');
assert.equal(f.formatMonthDayHourMinute(bad), bad.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) + ' ' + bad.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }));
assert.equal(f.formatMonthDayTime(bad), bad.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }));
assert.equal(tf._formatTaskTime(''), '');

const Real = Intl.DateTimeFormat; let built = 0;
Intl.DateTimeFormat = function (...a) { built++; return new Real(...a); };
Intl.DateTimeFormat.prototype = Real.prototype;
for (let i = 0; i < 500; i++) { f.formatHourMinute(dates[0]); f.formatMonthDayHourMinute(dates[1]); f.formatMonthDayTime(dates[2]); }
Intl.DateTimeFormat = Real;
assert.ok(built <= 0, 'already built by the first checks: no new formatter for 1,500 more calls (built ' + built + ')');
console.log('format_dates: ok');
