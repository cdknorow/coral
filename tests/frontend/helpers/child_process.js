'use strict';
// node:child_process with windowsHide on by default (#204). On Windows a child process of a console-less parent (the runner
// started from a service, a scheduled task or an IDE) gets a console window of its own, so a run that starts hundreds of go,
// node, sqlite3 and git processes flashes hundreds of windows on the person's screen. Every suite and helper spawns through
// this module (child_process_hygiene.test.js keeps it so); a call that passes windowsHide itself keeps its value.
const cp = require('node:child_process');

const isOptions = v => v !== null && typeof v === 'object' && !Array.isArray(v);

// The options of spawn/spawnSync/execFile/execFileSync(file, [args], [options], [callback]): the first plain object after the
// file, or a new one put where it belongs (after the arguments array, before a callback).
function hidden(args) {
    const a = args.slice();
    let i = 1;
    if (Array.isArray(a[i])) i++;
    if (isOptions(a[i])) a[i] = { windowsHide: true, ...a[i] };
    else a.splice(i, 0, { windowsHide: true });
    return a;
}
const wrap = name => (...args) => cp[name](...hidden(args));

const spawn = wrap('spawn');
const spawnSync = wrap('spawnSync');
const execFile = wrap('execFile');
const execFileSync = wrap('execFileSync');

module.exports = { spawn, spawnSync, execFile, execFileSync, hidden };
