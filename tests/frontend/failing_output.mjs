// What run_cdp_suites.mjs prints for a failing attempt (#3609). The console shows the attempt's last
// lines (30 by default; CDP_FULL=1 prints them all), and a [FAIL] line above them would be cut off with
// the rest: a suite that checks several phases printed only its last phase's failures. So every [FAIL]
// line from the hidden part is printed too, once each and in order, above the last lines, under a note
// saying how many lines are hidden. results.json always keeps the whole output; this is only the console.

const FAIL_LINE = /\[FAIL\]/;

// failingOutput returns the lines to print for one failing attempt's output.
export function failingOutput(out, { keep = 30, full = false } = {}) {
  const lines = String(out).trim().split('\n');
  if (full || lines.length <= keep) return lines;
  const hidden = lines.slice(0, -keep);
  const shown = lines.slice(-keep);
  const inShown = new Set(shown.map(l => l.trim()));
  const seen = new Set();
  const fails = [];
  for (const l of hidden) {
    const t = l.trim();
    if (!FAIL_LINE.test(t) || seen.has(t) || inShown.has(t)) continue;
    seen.add(t);
    fails.push(l);
  }
  const note = `… ${hidden.length} earlier line${hidden.length === 1 ? '' : 's'} not shown (CDP_FULL=1 prints them; results.json has them)`;
  if (!fails.length) return [note, ...shown];
  return [`${note}; their [FAIL] lines:`, ...fails, '… the last lines:', ...shown];
}
