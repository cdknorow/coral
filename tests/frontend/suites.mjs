// Every *.test.js / *.test.mjs file in this folder belongs to exactly one runner, so a suite can't be
// silently left out of every run (#3405: a CDP suite without the phrase the runner looked for never ran,
// while the runs reported all green).
//
//   cdp    run_cdp_suites.mjs, against an isolated Coral server and a headless Chrome: a file named
//          *_browser.test.js/mjs, or one that uses chrome-remote-interface.
//   server run_cdp_suites.mjs too (it gets the same isolated server), for suites that drive the server
//          without Chrome. Listed by name in SERVER_SUITES below.
//   node   npm run test:cloud: plain node, no server. Listed by name in package.json's test:cloud script.
//
// classifySuites reports the files that match no runner (unclassified), more than one (conflicts), and
// names listed in test:cloud or SERVER_SUITES with no such file (missing). run_cdp_suites.mjs refuses to
// run, and suite_census.test.mjs (part of test:cloud) fails, while any of them is non-empty.
import fs from 'node:fs';
import path from 'node:path';

export const SERVER_SUITES = ['agent_task_cli.test.js'];

const TEST_FILE = /\.test\.(js|mjs)$/;
const BROWSER_NAME = /_browser\.test\.(js|mjs)$/;

// nodeSuites returns the files test:cloud runs ("node <file>" entries).
export function nodeSuites(dir) {
  const pkg = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
  return [...(pkg.scripts['test:cloud'] || '').matchAll(/(?:^|\s)node\s+(\S+\.test\.(?:js|mjs))/g)].map(m => m[1]);
}

export function classifySuites(dir) {
  const files = fs.readdirSync(dir).filter(f => TEST_FILE.test(f)).sort();
  const node = new Set(nodeSuites(dir));
  const server = new Set(SERVER_SUITES);
  const out = { cdp: [], cdpByName: 0, server: [], node: [], unclassified: [], conflicts: [], missing: [] };
  for (const f of files) {
    const kinds = [];
    if (BROWSER_NAME.test(f) || fs.readFileSync(path.join(dir, f), 'utf8').includes('chrome-remote-interface')) kinds.push('cdp');
    if (server.has(f)) kinds.push('server');
    if (node.has(f)) kinds.push('node');
    if (kinds.length === 0) out.unclassified.push(f);
    else if (kinds.length > 1) out.conflicts.push(`${f} (${kinds.join(' + ')})`);
    else {
      out[kinds[0]].push(f);
      if (kinds[0] === 'cdp' && BROWSER_NAME.test(f)) out.cdpByName++;
    }
  }
  for (const f of [...node, ...server]) if (!files.includes(f)) out.missing.push(f);
  return out;
}

// problems returns one line per problem, or [] when every suite has exactly one runner.
export function problems(c) {
  const lines = [];
  for (const f of c.unclassified) {
    lines.push(`unclassified: ${f} — name it *_browser.test.js (CDP), add it to test:cloud in package.json (plain node), or to SERVER_SUITES in suites.mjs (needs the server, not Chrome)`);
  }
  for (const f of c.conflicts) lines.push(`in more than one runner: ${f}`);
  for (const f of c.missing) lines.push(`listed but no such file: ${f}`);
  return lines;
}
