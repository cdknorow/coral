// windows_known_reds.json lists the CDP suites that are skipped on Windows because they fail there for a Windows-only reason. Every skip is less coverage, so the list
// may shrink freely but never grow without a reviewed change to REVIEWED below (as the allowlists are): a new name fails here, and so does an entry with no reason.
//   node windows_known_reds.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));

// The reviewed list (2026-10-22, #6109, Security's review; UI/UX's #6155 fixes them and takes each name out of both places). Add a name here only with a review.
const REVIEWED = new Set([
  'settings_modal_trace_browser.test.js',
  'settings_text_budget_browser.test.js',
  'sleeping_agent_view_browser.test.js',
  'ui_audit_batch4_browser.test.js',
]);

function check(list, exists = f => fs.existsSync(path.join(here, f))) {
  const errs = [];
  for (const [name, reason] of Object.entries(list)) {
    if (name === '_about') continue;
    if (!REVIEWED.has(name)) errs.push(`${name} is not on the reviewed list: a new Windows skip needs a reviewed change to REVIEWED in windows_known_reds.test.mjs`);
    if (typeof reason !== 'string' || reason.trim().length < 20) errs.push(`${name} has no reason (a sentence saying what fails on Windows)`);
    if (!/_browser\.test\.m?js$|\.test\.m?js$/.test(name) || !exists(name)) errs.push(`${name} is not a suite in tests/frontend`);
  }
  return errs;
}

const list = JSON.parse(fs.readFileSync(path.join(here, 'windows_known_reds.json'), 'utf8'));
const errs = check(list);
assert.deepEqual(errs, [], errs.join('\n'));

// The check must be able to fail (a guard that cannot fail is no guard).
const base = { 'settings_text_budget_browser.test.js': 'the Terminal settings tab shows more words on Windows than its budget' };
assert.deepEqual(check(base, () => true), [], 'a reviewed name with a reason passes');
assert.equal(check({ ...base, 'new_suite_browser.test.js': 'a perfectly good sentence about a failure on Windows' }, () => true).length, 1, 'a new name is refused');
assert.equal(check({ 'settings_text_budget_browser.test.js': '' }, () => true).length, 1, 'an empty reason is refused');
assert.equal(check({ 'settings_text_budget_browser.test.js': 'too short' }, () => true).length, 1, 'a one-word reason is refused');
assert.equal(check(base, () => false).length, 1, 'a name that is no suite is refused');
console.log(`windows_known_reds: ok (${Object.keys(list).filter(k => k !== '_about').length} of ${REVIEWED.size} reviewed skips listed)`);
