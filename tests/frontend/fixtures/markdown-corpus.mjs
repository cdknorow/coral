// The shared markdown corpus: standalone, no import, no hub-only dependency. It is data plus checks over a `run(text, policy?)` function that renders in a real page and returns
// { ms, partial, tags, anchors, attrs, text, nodes, pwn }. The hub's chrome/markdown.mjs and the Coral inbox both run it against the SAME assets/markdown.js (same hash).
export const FORBIDDEN = ['script', 'style', 'iframe', 'object', 'embed', 'form', 'input', 'button', 'img', 'svg', 'math', 'link', 'meta', 'base', 'video', 'audio', 'source', 'canvas', 'details', 'textarea'];
// characters that must never reach a text node (escapes only: no invisible literal in this file)
export const HIDDEN = /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F-\u009F\u00AD\u034F\u061C\u180E\u200B\u200E\u200F\u2028\u2029\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF\u{E0000}-\u{E007F}]/u;
export const HIDDEN_CASES = ['\u2028', '\u2029', '\u00AD', '\u034F', '\u180E', '\u{E0041}', '\u{E007F}', '\u{E0000}', '\u200B', '\u202E', '\u2066', '\uFEFF'];
// timing bound (ms inside the page) for every pathological input below
export const BUDGET_MS = 4000;
export async function checkCorpus({ run, ok }) {
  let r;
  for (const ch of HIDDEN_CASES) {
    r = await run(`a${ch}b ${ch}[l${ch}abel](https://example.com/x)${ch}z`);
    ok(!HIDDEN.test(r.text) && r.text.startsWith('ab ') && r.anchors[0].text === 'label', `U+${ch.codePointAt(0).toString(16).toUpperCase()} is removed everywhere: ${JSON.stringify(r.text)}`);
  }
  r = await run('x\u{E0041}\u{E0042}y');
  ok(r.text === 'xy', 'tag characters carry no hidden text: ' + JSON.stringify(r.text));
  r = await run('\u2764\uFE0F and \u{1F468}\u200D\u{1F469}\u200D\u{1F467} and \u{1F44D}\u{1F3FD}');
  ok(r.text.includes('\u2764\uFE0F') && r.text.includes('\u{1F468}\u200D\u{1F469}\u200D\u{1F467}') && r.text.includes('\u{1F44D}\u{1F3FD}'), 'U+FE0F and the emoji joiner are kept: ' + JSON.stringify(r.text));
  r = await run('\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645');
  ok(r.text === '\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645', 'ZWNJ is kept (Persian spelling): ' + JSON.stringify(r.text));
  r = await run('\u0915\u094D\u200C\u0937 \u0915\u0631\u062F\u0649\u200C');
  ok(r.text.includes('\u0915\u094D\u200C\u0937') && r.text.includes('\u0631\u062F\u0649\u200C'), 'ZWNJ is kept in Indic and Kurdish text: ' + JSON.stringify(r.text));
  r = await run('a\u200Cb\u200Bc');
  ok(r.text === 'a\u200Cbc', 'ZWNJ kept, ZWSP stripped: ' + JSON.stringify(r.text));
  r = await run('a\u2028b\u2029c');
  ok(r.text === 'abc', 'line and paragraph separators are removed: ' + JSON.stringify(r.text));
  // pathological inputs: bounded work
  const big = [
    ['asterisks', '*'.repeat(250000)], ['brackets', '['.repeat(100000)], ['link openers', '[a](b '.repeat(40000)],
    ['soft hyphens', '\u00AD'.repeat(250000) + 'x'], ['tag characters', '\u{E0041}'.repeat(120000) + 'x'], ['line separators', '\u2028'.repeat(250000) + 'x'],
  ];
  for (const [name, text] of big) {
    r = await run(text);
    ok(r.ms < BUDGET_MS && r.nodes <= 4200 && !FORBIDDEN.some(t => r.tags[t]) && r.pwn === 'undefined', `pathological: ${name}: ${Math.round(r.ms)} ms, ${r.nodes} nodes`);
  }
  // the host link policy: it owns its links, adds no scheme, and a bad answer is plain text
  const pol = (name) => run('[x](coral-artifact:7)', name);
  r = await pol('owned');
  ok(r.anchors.length === 1 && r.anchors[0].href === '/artifacts/7' && r.anchors[0].target === null && r.anchors[0].rel === 'noopener noreferrer', 'a policy-owned same-origin link is an anchor without target: ' + JSON.stringify(r.anchors));
  for (const bad of ['javascript', 'protocol-relative', 'data', 'backslash', 'throws', 'refuse', 'junk', 'blank']) {
    r = await pol(bad);
    ok(r.anchors.length === 0 && r.text.startsWith('[x]'), `policy answer "${bad}" is refused: plain text, no anchor: ` + JSON.stringify(r.anchors));
  }
  r = await pol('defer');
  ok(r.anchors.length === 0, 'a policy that defers leaves an unknown scheme to the default policy, which refuses it');
  r = await run('[d](https://example.com/p)', 'defer');
  ok(r.anchors.length === 1 && r.anchors[0].target === '_blank', 'a deferred https link keeps the default policy');
}
