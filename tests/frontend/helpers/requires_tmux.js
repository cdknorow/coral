'use strict';
// A suite about the tmux backend (a tmux pane's size, tmux's own window sizing) has nothing to run against where tmux is not installed: Coral on Windows
// runs agents on pty only. It says so and exits 0, so the run does not fail for a machine that cannot have the thing under test (#237).
const { spawnSync } = require('./child_process.js');

module.exports = function requireTmux(suite) {
    if (process.platform === 'win32' || spawnSync('tmux', ['-V']).error) {
        console.log(`${suite}: SKIPPED, it tests the tmux backend and tmux is not available here (Coral on Windows runs agents on pty only)`);
        process.exit(0);
    }
};
