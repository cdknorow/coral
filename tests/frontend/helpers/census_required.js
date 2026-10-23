// The scenes a census suite must have run, one small file each (fixtures/census_required/<suite>/<scene>.txt, the scene's exact name on its first line). A commit that adds a scene adds a file, so two
// commits that each add scenes never touch the same line (the lists were one array literal in each suite, and every batch of scene commits conflicted there: #6432, #6448, #6475, #6524, #6566, #6598).
// Which suite: 'keyboard', 'contrast' or 'phone'. Read by required() in helpers/phone_targets.js.
'use strict';
const fs = require('node:fs');
const path = require('node:path');

function requiredScenes(suite) {
    const dir = path.join(__dirname, '..', 'fixtures', 'census_required', suite);
    if (!fs.existsSync(dir)) throw new Error(`census_required: no scene folder for "${suite}" (${dir})`);
    const names = fs.readdirSync(dir).filter(f => f.endsWith('.txt')).sort().map(f => fs.readFileSync(path.join(dir, f), 'utf8').split('\n')[0].trim());
    if (!names.length || names.some(n => !n)) throw new Error(`census_required: "${suite}" has no scenes or an empty scene file`);
    if (new Set(names).size !== names.length) throw new Error(`census_required: "${suite}" names a scene twice`);
    return names;
}

module.exports = { requiredScenes };
