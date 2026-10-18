// The comparison half of an HTML-builders golden (#4895): a table of named cases, each the serialized markup one builder produced for one state, kept in a fixtures/*.json
// file and compared exactly (not by hash: when a refactor changes a builder, the first difference is printed with its surroundings, which a hash cannot give).
// CORAL_UPDATE_GOLDEN=1 rewrites the file. A case that is in the golden and not in the run (or the other way round) fails too: a builder that stopped being exercised is a change.
'use strict';
const fs = require('node:fs');
const path = require('node:path');

function firstDifference(a, b) {
    let i = 0;
    while (i < a.length && i < b.length && a[i] === b[i]) i++;
    const around = (s) => JSON.stringify(s.slice(Math.max(0, i - 60), i + 120));
    return `at offset ${i} (now ${a.length} chars, was ${b.length}):\n    now: ${around(a)}\n    was: ${around(b)}`;
}

/** Compare `cases` ({name: markup}) with the golden file; returns the list of problems (empty when identical). `about` is written into a new golden. */
function compareGolden(file, cases, about) {
    if (process.env.CORAL_UPDATE_GOLDEN) {
        fs.mkdirSync(path.dirname(file), { recursive: true });
        fs.writeFileSync(file, JSON.stringify({ about, cases }, null, 1) + '\n');
        console.log(`golden written: ${Object.keys(cases).length} cases`);
        return [];
    }
    const want = JSON.parse(fs.readFileSync(file, 'utf8')).cases;
    const problems = [];
    for (const k of Object.keys(want)) if (!(k in cases)) problems.push(`${k}: no longer rendered`);
    for (const k of Object.keys(cases)) if (!(k in want)) problems.push(`${k}: not in the golden`);
    for (const k of Object.keys(want)) if (k in cases && cases[k] !== want[k]) problems.push(`${k}: changed ${firstDifference(cases[k], want[k])}`);
    return problems;
}

module.exports = { compareGolden, firstDifference };
