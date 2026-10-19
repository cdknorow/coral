// Pure rendering regression: use the production detail renderer without a DOM.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../../coral-go/pkg/frontend/static/task_detail_html.js'), 'utf8');
const start = source.indexOf('function _taskDetailHeadHtml('); // the detail is built by helpers (#4702): from the first of them to the end of _taskDetailHtml
const end = source.length; // the helpers run to the end of task_detail_html.js (moved out of tasks.js, #4996)
const escape = value => String(value ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
const context = vm.createContext({ escapeHtml: escape, escapeAttr: escape, _priorityRank: {}, formatTaskDate: String, _verifyLine: () => '' }); // the detail's verification line (#4564) is drawn by tasks.js; none of these tasks has one
vm.runInContext(source.slice(start, end).replaceAll('export function', 'function'), context);
const candidate = { submitted_by: '<img src=x>', submitted_at: 'now', proposed_outcome: 'success', reason: 'Review <pending>', message: '<script>bad()</script>', artifacts: [{ name: 'build', content: '<img onerror=bad()>', revision: 'rev-42' }] };
const task = { id: 1, title: 'Build', status: 'in_progress', workflow: { completion_review: candidate } };
let html = context._taskDetailHtml(task, null);
assert.ok(html.includes('Completion review requested (slot occupied)'));
assert.ok(html.includes('not an accepted result'));
assert.ok(html.includes('rev-42'));
assert.ok(!html.includes('<script>') && !html.includes('<img'));
assert.ok(!html.includes('Completion artifacts'));
task.status = 'review_pending';
candidate.released_by = 'reviewer'; candidate.released_at = 'later'; candidate.release_reason = 'Independent work';
html = context._taskDetailHtml(task, null);
assert.ok(html.includes('Review pending (slot released)'));
assert.ok(html.includes('Slot released by reviewer'));
assert.ok(!html.includes('Finished'));
task.status = 'completed'; task.workflow.outcome = 'failed';
html = context._taskDetailHtml(task, null);
assert.ok(html.includes('Finished') && html.includes('Failed'));
assert.ok(html.includes('not an accepted result'), 'candidate history remains distinct from final outcome');
console.log('PASS completion review labels, retained evidence, outcome distinction, and escaping');
