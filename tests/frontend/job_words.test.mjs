// The Jobs screens' words (#6515, #6521): a schedule, a next run, a result, a step and a time limit as a person says them; what is not known is shown as it is. node job_words.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const w = await importUi('job_words');
for (const [cron, words] of [['0 * * * *', 'Every hour'], ['0 */6 * * *', 'Every 6 hours'], ['0 2 * * *', 'Every day at 02:00'], ['30 18 * * 1-5', 'Weekdays at 18:30'], ['0 9 * * 1', 'Mondays at 09:00'], ['0 9 * * 0', 'Sundays at 09:00'], ['0 9 * * 7', 'Sundays at 09:00'], ['0 8 * * 0,6', 'Weekends at 08:00'], ['*/15 * * * *', 'Every 15 minutes'], ['0 6 1 * *', 'Monthly on day 1 at 06:00'], ['0 0 1 1 *', 'Schedule: 0 0 1 1 *'], ['bad', 'Schedule: bad'], ['', '']]) assert.equal(w.cronWords(cron), words, cron);
const now = new Date(2026, 9, 23, 14, 0, 0);
assert.equal(w.atWords(new Date(2026, 9, 24, 2, 0).toISOString(), now), 'tomorrow 02:00');
assert.equal(w.awayWords(new Date(2026, 9, 23, 19, 0).toISOString(), now), 'in 5 h');
assert.equal(w.reasonWords({ error_msg: 'step 1 (Test) failed: exit code 3' }), 'step 2 (Test) exited with 3');
assert.equal(w.reasonWords({}), '');
assert.equal(w.whenWords(new Date(2026, 9, 23, 6, 36).toISOString(), now), 'today 06:36');
assert.equal(w.whenWords(new Date(2026, 9, 22, 2, 0).toISOString(), now), 'yesterday 02:00');
assert.equal(w.stepWords('step 1 (Test) failed: exit code 3'), 'step 2 (Test) failed: exit code 3', 'steps from 1');
assert.equal(w.stepWords('step 0 (A) and step 4 (B)'), 'step 1 (A) and step 5 (B)');
assert.equal(w.resultWords({ status: 'failed', error_msg: 'step 1 (Test) failed: exit code 3' }), 'Failed: step 2 (Test) exited with 3');
assert.equal(w.resultWords({ status: 'failed', error_msg: 'timed out' }), 'Failed: timed out');
assert.equal(w.resultWords({ status: 'failed' }), 'Failed');
assert.deepEqual(['completed', 'killed', 'running', 'pending'].map(s => w.resultWords({ status: s })), ['Passed', 'Stopped', 'Running', 'Waiting to start']);
assert.equal(w.lastWords(null), 'never run'); assert.equal(w.lastWords({ status: 'failed' }), 'last run failed'); assert.equal(w.lastWords({ status: 'completed' }), 'last run passed');
assert.deepEqual([3600, 7200, 600, 60, 90, 0].map(w.limitWords), ['1 hour', '2 hours', '10 minutes', '1 minute', '90 seconds', '']);
console.log('PASS job_words');
