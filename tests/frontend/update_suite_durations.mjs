#!/usr/bin/env node
// Writes suite_durations.json (the order input for run_cdp_suites.mjs, see suite_order.mjs) from the results.json of one or more full runs:
//   node update_suite_durations.mjs <results.json> [<results.json> ...]
// A suite's duration is the median of its passing first attempts across the files; suites missing from the files keep their old value.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { durationsFromResults, loadDurations } from './suite_order.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.join(here, 'suite_durations.json');
const files = process.argv.slice(2);
if (!files.length) { console.error('usage: node update_suite_durations.mjs <results.json> [...]'); process.exit(2); }
const per = {};
for (const f of files) for (const [name, secs] of Object.entries(durationsFromResults(JSON.parse(fs.readFileSync(f, 'utf8'))))) (per[name] ||= []).push(secs);
const merged = { ...loadDurations(out) };
for (const [name, v] of Object.entries(per)) { v.sort((a, b) => a - b); merged[name] = v[Math.floor(v.length / 2)]; }
const sorted = Object.fromEntries(Object.entries(merged).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])));
fs.writeFileSync(out, JSON.stringify(sorted, null, 1) + '\n');
console.log(`wrote ${Object.keys(sorted).length} suites to ${out}; the slowest: ${Object.entries(sorted).slice(0, 5).map(([n, s]) => `${n} ${s}s`).join(', ')}`);
